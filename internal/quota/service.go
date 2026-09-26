package quota

import (
	"context"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/router-for-me/CLIProxyAPI/v7/internal/config"
	runtimehelps "github.com/router-for-me/CLIProxyAPI/v7/internal/runtime/executor/helps"
	coreauth "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/auth"
	log "github.com/sirupsen/logrus"
)

const (
	// fetchTimeout bounds one upstream quota probe: one-shot per-credential
	// probes in the same shape as the management APICall timeout, tightened
	// for inference-path latency. Request cancellation still aborts earlier.
	// NOTE: this is an intentional timeout exception; the AGENTS.md
	// exception list is PR-guarded, so that one-line doc update must land
	// out-of-band via a maintainer.
	fetchTimeout = 30 * time.Second
	// cacheTTL bounds upstream pressure: at most one live fetch per
	// credential per TTL under steady polling.
	cacheTTL = 60 * time.Second
	// staleMax bounds stale fallback after fetch errors.
	staleMax = 10 * time.Minute
	// maxConcurrentFetches bounds concurrent probes to one provider. A gateway
	// listing ten accounts of the same provider must not open ten upstream
	// calls at once. Different providers run independently.
	maxConcurrentFetches = 2
)

// Service resolves live quota snapshots per credential with TTL caching,
// stale fallback, and a global concurrency bound. The zero Fetcher set
// serves nothing; Register adds providers.
type Service struct {
	globalProxy func() string

	mu       chan struct{}
	flight   sync.Mutex
	lanes    map[string]chan struct{}
	cache    *Cache
	fetchers map[string]Fetcher
	inflight map[string]*fetchCall
}

// fetchCall is one upstream probe shared by every caller that misses the
// cache for the same credential at the same time.
type fetchCall struct {
	done     chan struct{}
	snapshot *Snapshot
	ok       bool
}

// NewService builds a Service. globalProxy supplies the current global
// proxy URL per fetch (a func because server config is hot-swapped);
// it may be nil.
func NewService(globalProxy func() string) *Service {
	return &Service{
		globalProxy: globalProxy,
		cache:       NewCache(cacheTTL, staleMax),
		lanes:       make(map[string]chan struct{}),
		fetchers:    make(map[string]Fetcher),
		inflight:    make(map[string]*fetchCall),
	}
}

// Register adds a provider fetcher, replacing any previous one.
func (s *Service) Register(fetcher Fetcher) {
	if s == nil || fetcher == nil {
		return
	}
	if key := strings.ToLower(strings.TrimSpace(fetcher.Provider())); key != "" {
		s.fetchers[key] = fetcher
	}
}

// SupportedProvider reports whether live fetching exists for a provider key.
func (s *Service) SupportedProvider(provider string) bool {
	if s == nil {
		return false
	}
	_, ok := s.fetchers[CanonicalProvider(provider)]
	return ok && CanonicalProvider(provider) != ""
}

// Snapshot returns live quota for one credential under mode. It returns
// ok=false when no live data is available (unsupported provider, cache-only
// miss, fetch error without a fresh or stale snapshot, or cancellation);
// callers fall back to passive signal snapshots. Snapshots MUST be treated
// as immutable.
func (s *Service) Snapshot(ctx context.Context, auth *coreauth.Auth, mode RefreshMode) (*Snapshot, bool) {
	if s == nil || auth == nil {
		return nil, false
	}
	fetcher, ok := s.fetchers[CanonicalProvider(auth.Provider)]
	if !ok || fetcher == nil {
		return nil, false
	}
	key := quotaCacheKey(auth)
	if key == "" {
		return nil, false
	}

	if mode != RefreshLive {
		if snapshot, fresh, _ := s.cache.Get(key); fresh {
			return snapshot, true
		}
	}
	if mode == RefreshCached {
		return nil, false
	}

	if call, shared := s.begin(key); shared {
		return s.wait(ctx, call, key)
	} else {
		defer s.finish(key, call)
		select {
		case <-ctx.Done():
			if snapshot, fresh, stale := s.cache.Get(key); fresh || stale {
				call.snapshot, call.ok = snapshot, true
			}
			return nil, false
		case s.lane(auth.Provider) <- struct{}{}:
		}
		defer func() { <-s.lane(auth.Provider) }()

		snapshot, err := fetcher.Fetch(ctx, FetchRequest{Auth: auth, Client: s.clientFor(ctx, auth), Forced: mode == RefreshLive})
		if err == nil && snapshot != nil {
			s.cache.Put(key, snapshot)
			call.snapshot, call.ok = snapshot, true
			return snapshot, true
		}
		if err != nil {
			log.WithFields(log.Fields{"provider": auth.Provider}).WithError(err).Warn("live quota fetch failed")
		}
		if snapshot, fresh, stale := s.cache.Get(key); fresh || stale {
			call.snapshot, call.ok = snapshot, true
			return snapshot, true
		}
		return nil, false
	}
}

// quotaCacheKey identifies a credential without writing Auth.Index.
// EnsureIndex mutates the auth and races when one credential is queried
// concurrently. A preset index is reused; otherwise the id is enough,
// because live snapshots are process-local.
func quotaCacheKey(auth *coreauth.Auth) string {
	if auth == nil {
		return ""
	}
	if key := strings.TrimSpace(auth.Index); key != "" {
		return key
	}
	return strings.TrimSpace(auth.ID)
}

// lane returns the concurrency gate for one provider. The same channel is
// reused so acquire and release match.
func (s *Service) lane(provider string) chan struct{} {
	key := CanonicalProvider(provider)
	if key == "" {
		key = strings.ToLower(strings.TrimSpace(provider))
	}
	s.flight.Lock()
	defer s.flight.Unlock()
	lane := s.lanes[key]
	if lane == nil {
		lane = make(chan struct{}, maxConcurrentFetches)
		s.lanes[key] = lane
	}
	return lane
}

// begin starts a fetch or joins the one already running for key.
func (s *Service) begin(key string) (*fetchCall, bool) {
	s.flight.Lock()
	defer s.flight.Unlock()
	if call := s.inflight[key]; call != nil {
		return call, true
	}
	call := &fetchCall{done: make(chan struct{})}
	s.inflight[key] = call
	return call, false
}

func (s *Service) finish(key string, call *fetchCall) {
	s.flight.Lock()
	delete(s.inflight, key)
	s.flight.Unlock()
	close(call.done)
}

func (s *Service) wait(ctx context.Context, call *fetchCall, key string) (*Snapshot, bool) {
	select {
	case <-ctx.Done():
		if snapshot, fresh, stale := s.cache.Get(key); fresh || stale {
			return snapshot, true
		}
		return nil, false
	case <-call.done:
		return call.snapshot, call.ok
	}
}

// clientFor builds a proxy-aware client per fetch so hot-reloaded proxy
// configuration applies without restarting the service.
func (s *Service) clientFor(ctx context.Context, auth *coreauth.Auth) *http.Client {
	cfg := &config.Config{}
	if s != nil && s.globalProxy != nil {
		cfg.ProxyURL = s.globalProxy()
	}
	if strings.EqualFold(strings.TrimSpace(auth.Provider), "devin") {
		return runtimehelps.NewDevinHTTPClient(ctx, cfg, auth, fetchTimeout)
	}
	return runtimehelps.NewProxyAwareHTTPClient(ctx, cfg, auth, fetchTimeout)
}
