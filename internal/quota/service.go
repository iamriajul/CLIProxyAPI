package quota

import (
	"context"
	"net/http"
	"strings"
	"time"

	"github.com/router-for-me/CLIProxyAPI/v7/internal/config"
	runtimehelps "github.com/router-for-me/CLIProxyAPI/v7/internal/runtime/executor/helps"
	coreauth "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/auth"
	log "github.com/sirupsen/logrus"
)

const (
	// fetchTimeout bounds one upstream quota probe. This is an intentional
	// timeout exception (see AGENTS.md): one-shot per-credential probes in
	// the same shape as the management APICall timeout, tightened for
	// inference-path latency. Request cancellation still aborts earlier.
	fetchTimeout = 30 * time.Second
	// cacheTTL bounds upstream pressure: at most one live fetch per
	// credential per TTL under steady polling.
	cacheTTL = 60 * time.Second
	// staleMax bounds stale fallback after fetch errors.
	staleMax = 10 * time.Minute
	// maxConcurrentFetches bounds global concurrent upstream probes.
	maxConcurrentFetches = 8
)

// Service resolves live quota snapshots per credential with TTL caching,
// stale fallback, and a global concurrency bound. The zero Fetcher set
// serves nothing; Register adds providers.
type Service struct {
	globalProxy func() string

	mu       chan struct{}
	cache    *Cache
	fetchers map[string]Fetcher
}

// NewService builds a Service. globalProxy supplies the current global
// proxy URL per fetch (a func because server config is hot-swapped);
// it may be nil.
func NewService(globalProxy func() string) *Service {
	return &Service{
		globalProxy: globalProxy,
		mu:          make(chan struct{}, maxConcurrentFetches),
		cache:       NewCache(cacheTTL, staleMax),
		fetchers:    make(map[string]Fetcher),
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
	key := strings.TrimSpace(auth.EnsureIndex())
	if key == "" {
		key = strings.TrimSpace(auth.ID)
	}

	if mode != RefreshLive {
		if snapshot, fresh, _ := s.cache.Get(key); fresh {
			return snapshot, true
		}
	}
	if mode == RefreshCached {
		return nil, false
	}

	select {
	case <-ctx.Done():
		return nil, false
	case s.mu <- struct{}{}:
	}
	defer func() { <-s.mu }()

	snapshot, err := fetcher.Fetch(ctx, FetchRequest{Auth: auth, Client: s.clientFor(ctx, auth)})
	if err == nil && snapshot != nil {
		s.cache.Put(key, snapshot)
		return snapshot, true
	}
	if err != nil {
		log.WithFields(log.Fields{"provider": auth.Provider}).WithError(err).Warn("live quota fetch failed")
	}
	if snapshot, fresh, stale := s.cache.Get(key); fresh || stale {
		return snapshot, true
	}
	return nil, false
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
