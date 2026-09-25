package quota

import (
	"context"
	"errors"
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
