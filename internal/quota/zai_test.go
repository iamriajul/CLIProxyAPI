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

// Ported fixture: CPAMC tests/zaiQuota.test.ts quotaResponse().
const zaiTestPayload = `{
  "success": true,
  "code": 200,
  "data": {
    "level": "pro",
    "limits": [
      {"type": "CREDIT_LIMIT", "usage": 12000, "currentValue": 1438, "percentage": 11,
       "remaining": 10562, "nextResetTime": 1789500000000, "unit": 3, "number": 5},
      {"type": "CREDIT_LIMIT", "usage": 60000, "currentValue": 9000, "percentage": 15,
       "remaining": 51000, "nextResetTime": "2026-09-21T00:00:00.000Z", "unit": 6, "number": 1},
      {"type": "TIME_LIMIT", "usage": 100, "currentValue": 25, "percentage": 25,
       "remaining": 75, "nextResetTime": 1789500000, "unit": 3, "number": 5}
    ]
  }
}`

func TestZaiFetcher(t *testing.T) {
	const apiKey = "zai-provisioned-key"
	var gotAuth string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotAuth = r.Header.Get("Authorization")
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(zaiTestPayload))
	}))
	defer server.Close()

	previousURL := zaiQuotaURL
	zaiQuotaURL = server.URL
	defer func() { zaiQuotaURL = previousURL }()

	fetcher := NewZaiFetcher()
	if fetcher.Provider() != "zai" {
		t.Fatalf("provider = %q", fetcher.Provider())
	}
	auth := &coreauth.Auth{Provider: "zai", Attributes: map[string]string{"api_key": apiKey}}
	snapshot, err := fetcher.Fetch(context.Background(), FetchRequest{Auth: auth, Client: server.Client()})
	if err != nil {
		t.Fatalf("Fetch: %v", err)
	}
	// The key authorizes verbatim: no Bearer prefix.
	if gotAuth != apiKey {
		t.Fatalf("Authorization = %q, want verbatim key", gotAuth)
	}
	if snapshot == nil {
		t.Fatal("snapshot is nil")
	}
	if snapshot.Plan != "pro" {
		t.Fatalf("plan = %q", snapshot.Plan)
	}
	if len(snapshot.Windows) != 3 {
		t.Fatalf("windows = %+v", snapshot.Windows)
	}
	byName := map[string]Window{}
	for _, window := range snapshot.Windows {
		byName[window.Name] = window
	}
	// Exact used/limit ratio preferred over the server-rounded percent.
	credit5h := byName["credits/5h"]
	if credit5h.UsedPercent == nil || *credit5h.UsedPercent < 11.98 || *credit5h.UsedPercent > 11.99 {
		t.Fatalf("credits/5h used = %v", credit5h.UsedPercent)
	}
	if credit5h.ResetAt == nil || !credit5h.ResetAt.Equal(time.UnixMilli(1789500000000).UTC()) {
		t.Fatalf("credits/5h reset = %v", credit5h.ResetAt)
	}
	// Epoch seconds normalized to milliseconds.
	req5h := byName["requests/5h"]
	if req5h.UsedPercent == nil || *req5h.UsedPercent != 25 {
		t.Fatalf("requests/5h used = %v", req5h.UsedPercent)
	}
	if req5h.ResetAt == nil || !req5h.ResetAt.Equal(time.Unix(1789500000, 0).UTC()) {
		t.Fatalf("requests/5h reset = %v", req5h.ResetAt)
	}
	weekly, ok := byName["credits/7d"]
	if !ok || weekly.UsedPercent == nil || *weekly.UsedPercent != 15 {
		t.Fatalf("credits/7d = %+v", weekly)
	}
	// Sorted alphabetically by name.
	names := []string{snapshot.Windows[0].Name, snapshot.Windows[1].Name, snapshot.Windows[2].Name}
	if names[0] != "credits/5h" || names[1] != "credits/7d" || names[2] != "requests/5h" {
		t.Fatalf("window order = %v", names)
	}
}

func TestZaiFetcherErrors(t *testing.T) {
	fetcher := NewZaiFetcher()

	if _, err := fetcher.Fetch(context.Background(), FetchRequest{Client: http.DefaultClient}); err == nil ||
		!strings.Contains(err.Error(), "api key") {
		t.Fatalf("missing credential error = %v", err)
	}

	statusServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusUnauthorized)
	}))
	defer statusServer.Close()
	previousURL := zaiQuotaURL
	zaiQuotaURL = statusServer.URL
	defer func() { zaiQuotaURL = previousURL }()

	auth := &coreauth.Auth{Attributes: map[string]string{"api_key": "key"}}
	if _, err := fetcher.Fetch(context.Background(), FetchRequest{Auth: auth, Client: statusServer.Client()}); err == nil {
		t.Fatal("expected non-2xx error")
	}

	emptyServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"success": false, "data": {"limits": []}}`))
	}))
	defer emptyServer.Close()
	zaiQuotaURL = emptyServer.URL
	if _, err := fetcher.Fetch(context.Background(), FetchRequest{Auth: auth, Client: emptyServer.Client()}); err == nil {
		t.Fatal("expected empty-data error")
	}
}
