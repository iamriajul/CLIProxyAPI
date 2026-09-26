package quota

import (
	"context"
	"errors"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	coreauth "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/auth"
)

type fakeFetcher struct {
	provider string
	calls    atomic.Int64
	snapshot *Snapshot
	err      error
}

func (f *fakeFetcher) Provider() string { return f.provider }

func (f *fakeFetcher) Fetch(context.Context, FetchRequest) (*Snapshot, error) {
	f.calls.Add(1)
	if f.err != nil {
		return nil, f.err
	}
	return f.snapshot, nil
}

func testAuth(id, provider string) *coreauth.Auth {
	return &coreauth.Auth{ID: id, Provider: provider, Status: coreauth.StatusActive}
}

func TestServiceSnapshotCachesAndFallsBack(t *testing.T) {
	now := time.Date(2026, 9, 25, 10, 0, 0, 0, time.UTC)
	service := NewService(nil)
	service.cache.now = func() time.Time { return now }
	fetcher := &fakeFetcher{provider: "codex", snapshot: &Snapshot{
		Windows: []Window{{Name: "5h", UsedPercent: UsedPercent(10)}},
		Plan:    "pro",
	}}
	service.Register(fetcher)
	auth := testAuth("svc-auth", "codex")
	ctx := context.Background()

	snap, ok := service.Snapshot(ctx, auth, RefreshAuto)
	if !ok || snap.Plan != "pro" || !snap.ObservedAt.Equal(now) {
		t.Fatalf("snapshot = %+v, %v", snap, ok)
	}
	if fetcher.calls.Load() != 1 {
		t.Fatalf("calls = %d, want 1", fetcher.calls.Load())
	}

	// Cache hit within TTL: no new fetch.
	if _, ok := service.Snapshot(ctx, auth, RefreshAuto); !ok {
		t.Fatal("expected cache hit")
	}
	if fetcher.calls.Load() != 1 {
		t.Fatalf("calls = %d, want 1", fetcher.calls.Load())
	}

	// Force live bypasses the read but repopulates.
	now = now.Add(time.Minute)
	if _, ok := service.Snapshot(ctx, auth, RefreshLive); !ok {
		t.Fatal("expected live fetch")
	}
	if fetcher.calls.Load() != 2 {
		t.Fatalf("calls = %d, want 2", fetcher.calls.Load())
	}

	// Past TTL with fetch errors: stale fallback within horizon.
	fetcher.err = errors.New("upstream down")
	now = now.Add(2 * time.Minute)
	snap, ok = service.Snapshot(ctx, auth, RefreshAuto)
	if !ok || snap.Plan != "pro" {
		t.Fatalf("stale = %+v, %v", snap, ok)
	}

	// Past the stale horizon: miss.
	now = now.Add(time.Hour)
	if _, ok := service.Snapshot(ctx, auth, RefreshAuto); ok {
		t.Fatal("expected miss past stale horizon")
	}

	// Cache-only never fetches.
	fetcher.err = nil
	now = now.Add(time.Hour)
	service.cache.now = func() time.Time { return now }
	if _, ok := service.Snapshot(ctx, auth, RefreshCached); ok {
		t.Fatal("expected cache-only miss")
	}
	if fetcher.calls.Load() != 4 {
		t.Fatalf("calls = %d, want 4", fetcher.calls.Load())
	}
}

func TestServiceSnapshotUnsupportedAndCancel(t *testing.T) {
	service := NewService(nil)
	service.Register(&fakeFetcher{provider: "codex"})
	if service.SupportedProvider("codex") != true || service.SupportedProvider("gemini") != false {
		t.Fatal("SupportedProvider mismatch")
	}
	if _, ok := service.Snapshot(context.Background(), testAuth("a", "gemini"), RefreshAuto); ok {
		t.Fatal("expected miss for unsupported provider")
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, ok := service.Snapshot(ctx, testAuth("b", "codex"), RefreshLive); ok {
		t.Fatal("expected miss on cancelled context")
	}
}

func TestServiceSnapshotCoalescesConcurrentMisses(t *testing.T) {
	service := NewService(nil)
	started := make(chan struct{})
	release := make(chan struct{})
	var once sync.Once
	fetcher := &blockingFetcher{provider: "codex", started: started, release: release, once: &once}
	service.Register(fetcher)
	auth := testAuth("coalesce", "codex")

	const callers = 8
	var wg sync.WaitGroup
	okCount := atomic.Int64{}
	wg.Add(callers)
	for range callers {
		go func() {
			defer wg.Done()
			if _, ok := service.Snapshot(context.Background(), auth, RefreshAuto); ok {
				okCount.Add(1)
			}
		}()
	}
	<-started
	close(release)
	wg.Wait()
	if fetcher.calls.Load() != 1 {
		t.Fatalf("calls = %d, want 1 shared fetch", fetcher.calls.Load())
	}
	if okCount.Load() != callers {
		t.Fatalf("ok = %d, want %d", okCount.Load(), callers)
	}
}

type blockingFetcher struct {
	provider string
	started  chan struct{}
	release  chan struct{}
	once     *sync.Once
	calls    atomic.Int64
}

func (f *blockingFetcher) Provider() string { return f.provider }

func (f *blockingFetcher) Fetch(context.Context, FetchRequest) (*Snapshot, error) {
	f.calls.Add(1)
	f.once.Do(func() { close(f.started) })
	<-f.release
	return &Snapshot{Plan: "pro"}, nil
}

func TestServiceSnapshotLimitsProviderBurst(t *testing.T) {
	const accounts = 10
	service := NewService(nil)
	codex := &gatedFetcher{provider: "codex", held: make(chan struct{}, accounts), done: make(chan struct{})}
	claude := &gatedFetcher{provider: "claude", held: make(chan struct{}, 1), done: make(chan struct{})}
	service.Register(codex)
	service.Register(claude)
	var wg sync.WaitGroup
	wg.Add(accounts + 1)
	for i := range accounts {
		auth := testAuth("codex-"+string(rune('a'+i)), "codex")
		go func() {
			defer wg.Done()
			_, _ = service.Snapshot(context.Background(), auth, RefreshLive)
		}()
	}
	go func() {
		defer wg.Done()
		_, _ = service.Snapshot(context.Background(), testAuth("claude-1", "claude"), RefreshLive)
	}()

	for range maxConcurrentFetches {
		<-codex.held
	}
	<-claude.held
	if peak := codex.peak.Load(); peak > maxConcurrentFetches {
		t.Fatalf("codex peak = %d, want at most %d", peak, maxConcurrentFetches)
	}
	if claude.calls.Load() != 1 {
		t.Fatalf("claude calls = %d, want 1 while codex is capped", claude.calls.Load())
	}
	codex.release()
	claude.release()
	wg.Wait()
	if codex.calls.Load() != accounts {
		t.Fatalf("codex calls = %d, want %d", codex.calls.Load(), accounts)
	}
}

type gatedFetcher struct {
	provider string
	current  atomic.Int64
	peak     atomic.Int64
	calls    atomic.Int64
	once     sync.Once
	done     chan struct{}
	held     chan struct{}
}

func (f *gatedFetcher) Provider() string { return f.provider }

func (f *gatedFetcher) release() {
	f.once.Do(func() {
		if f.done == nil {
			f.done = make(chan struct{})
		}
		close(f.done)
	})
}

func (f *gatedFetcher) Fetch(context.Context, FetchRequest) (*Snapshot, error) {
	f.calls.Add(1)
	now := f.current.Add(1)
	for {
		peak := f.peak.Load()
		if now <= peak || f.peak.CompareAndSwap(peak, now) {
			break
		}
	}
	f.held <- struct{}{}
	<-f.done
	f.current.Add(-1)
	return &Snapshot{Plan: "pro"}, nil
}
