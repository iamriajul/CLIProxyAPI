package quota

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	coreauth "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/auth"
)

// Fixture mirrors CPAMC tests/kimiQuotaOrder.test.ts: a 5-hour limit plus the
// weekly usage summary, with protobuf-style units and string numbers.
const kimiTestPayload = `{
  "usage": {"used": "1", "limit": "100", "remaining": "99",
            "resetTime": "2099-08-06T13:59:23.136523Z"},
  "limits": [{
    "detail": {"used": "2", "limit": "100", "remaining": "98",
               "resetTime": "2099-07-31T06:59:23.136523Z"},
    "window": {"duration": 300, "timeUnit": "TIME_UNIT_MINUTE"}
  }]
}`

func TestKimiFetcher(t *testing.T) {
	var gotAuth string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotAuth = r.Header.Get("Authorization")
		_, _ = w.Write([]byte(kimiTestPayload))
	}))
	defer server.Close()

	previousURL := kimiUsageURL
	kimiUsageURL = server.URL
	defer func() { kimiUsageURL = previousURL }()

	fetcher := NewKimiFetcher()
	if fetcher.Provider() != "kimi" {
		t.Fatalf("provider = %q", fetcher.Provider())
	}
	auth := &coreauth.Auth{Attributes: map[string]string{"api_key": "kimi-key"}}
	snapshot, err := fetcher.Fetch(context.Background(), FetchRequest{Auth: auth, Client: server.Client()})
	if err != nil {
		t.Fatalf("Fetch: %v", err)
	}
	if gotAuth != "Bearer kimi-key" {
		t.Fatalf("Authorization = %q", gotAuth)
	}
	if snapshot == nil || len(snapshot.Windows) != 2 {
		t.Fatalf("snapshot = %+v", snapshot)
	}
	// Sorted alphabetically: the 5h duration token sorts before 7d.
	if snapshot.Windows[0].Name != "5h" || snapshot.Windows[1].Name != "7d" {
		t.Fatalf("windows = %+v", snapshot.Windows)
	}
	if snapshot.Windows[0].UsedPercent == nil || *snapshot.Windows[0].UsedPercent != 2 {
		t.Fatalf("5h used = %v", snapshot.Windows[0].UsedPercent)
	}
	if snapshot.Windows[1].UsedPercent == nil || *snapshot.Windows[1].UsedPercent != 1 {
		t.Fatalf("weekly used = %v", snapshot.Windows[1].UsedPercent)
	}
	wantLimit := time.Date(2099, 7, 31, 6, 59, 23, 136523000, time.UTC)
	if snapshot.Windows[0].ResetAt == nil || !snapshot.Windows[0].ResetAt.Equal(wantLimit) {
		t.Fatalf("5h reset = %v", snapshot.Windows[0].ResetAt)
	}
	wantSummary := time.Date(2099, 8, 6, 13, 59, 23, 136523000, time.UTC)
	if snapshot.Windows[1].ResetAt == nil || !snapshot.Windows[1].ResetAt.Equal(wantSummary) {
		t.Fatalf("weekly reset = %v", snapshot.Windows[1].ResetAt)
	}
}

func TestKimiFetcherRemainingConvertsToUsed(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// No used field: used derives as limit - remaining = 70.
		_, _ = w.Write([]byte(`{"limits": [{"name": "Daily", "limit": 100, "remaining": 30}]}`))
	}))
	defer server.Close()

	previousURL := kimiUsageURL
	kimiUsageURL = server.URL
	defer func() { kimiUsageURL = previousURL }()

	auth := &coreauth.Auth{Attributes: map[string]string{"api_key": "kimi-key"}}
	snapshot, err := NewKimiFetcher().Fetch(context.Background(), FetchRequest{Auth: auth, Client: server.Client()})
	if err != nil {
		t.Fatalf("Fetch: %v", err)
	}
	if snapshot == nil || len(snapshot.Windows) != 1 {
		t.Fatalf("snapshot = %+v", snapshot)
	}
	if snapshot.Windows[0].Name != "daily" {
		t.Fatalf("name = %q", snapshot.Windows[0].Name)
	}
	if snapshot.Windows[0].UsedPercent == nil || *snapshot.Windows[0].UsedPercent != 70 {
		t.Fatalf("used = %v (want 70 from remaining 30)", snapshot.Windows[0].UsedPercent)
	}
}

func TestKimiFetcherRelativeReset(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"usage": {"used": 50, "limit": 100, "resetIn": 3600}}`))
	}))
	defer server.Close()

	previousURL := kimiUsageURL
	kimiUsageURL = server.URL
	defer func() { kimiUsageURL = previousURL }()

	before := time.Now()
	auth := &coreauth.Auth{Attributes: map[string]string{"api_key": "kimi-key"}}
	snapshot, err := NewKimiFetcher().Fetch(context.Background(), FetchRequest{Auth: auth, Client: server.Client()})
	after := time.Now()
	if err != nil {
		t.Fatalf("Fetch: %v", err)
	}
	if snapshot == nil || len(snapshot.Windows) != 1 {
		t.Fatalf("snapshot = %+v", snapshot)
	}
	reset := snapshot.Windows[0].ResetAt
	if reset == nil || reset.Before(before.Add(time.Hour)) || reset.After(after.Add(time.Hour)) {
		t.Fatalf("relative reset = %v", reset)
	}
}

func TestKimiFetcherErrors(t *testing.T) {
	fetcher := NewKimiFetcher()

	if _, err := fetcher.Fetch(context.Background(), FetchRequest{Client: http.DefaultClient}); err == nil ||
		!strings.Contains(err.Error(), "access token") {
		t.Fatalf("missing credential error = %v", err)
	}

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusBadGateway)
	}))
	defer server.Close()
	previousURL := kimiUsageURL
	kimiUsageURL = server.URL
	defer func() { kimiUsageURL = previousURL }()

	auth := &coreauth.Auth{Attributes: map[string]string{"api_key": "kimi-key"}}
	if _, err := fetcher.Fetch(context.Background(), FetchRequest{Auth: auth, Client: server.Client()}); err == nil {
		t.Fatal("expected non-2xx error")
	}
}
