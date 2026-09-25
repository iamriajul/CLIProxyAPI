package quota

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	coreauth "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/auth"
)

const antigravityTestQuotaFixture = `{
  "groups": [
    {
      "displayName": "Gemini Models",
      "description": "Models within this group: gemini-3-pro",
      "buckets": [
        {"bucketId": "gemini-5h", "displayName": "Five Hour Limit", "window": "5h", "remainingFraction": 0.75, "resetTime": "2026-09-25T14:00:00Z"},
        {"bucketId": "gemini-weekly", "displayName": "Weekly Limit", "window": "weekly", "remainingFraction": 0.4, "resetTime": "2026-09-28T00:00:00Z"},
        {"bucketId": "gemini-monthly", "displayName": "Monthly Limit", "window": "monthly", "remainingFraction": 1, "resetTime": "2026-10-01T00:00:00Z"},
        {"bucketId": "gemini-empty", "displayName": "Daily Limit", "window": "daily"}
      ]
    },
    {
      "displayName": "Claude and GPT Models",
      "buckets": [
        {"bucketId": "claude-5h", "displayName": "5-Hour Limit", "window": "five-hour", "remainingFraction": 0.5, "resetTime": "2026-09-25T18:30:00Z"},
        {"bucketId": "claude-weekly", "displayName": "Weekly Limit", "window": "week", "remainingFraction": 0.9, "resetTime": "2026-09-29T00:00:00Z"}
      ]
    }
  ]
}`

const antigravityTestSubscriptionFixture = `{
  "currentTier": {"id": "free-tier", "name": "Free"},
  "paidTier": {"id": "g1-pro-tier", "name": "Pro"}
}`

func antigravityTestServer(t *testing.T, quotaStatus int, quotaBody string) *httptest.Server {
	t.Helper()
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}
		if r.Header.Get("Content-Type") != "application/json" {
			http.Error(w, "bad content type", http.StatusBadRequest)
			return
		}
		if r.Header.Get("Authorization") != "Bearer test-token" {
			http.Error(w, "unauthorized", http.StatusUnauthorized)
			return
		}
		switch r.URL.Path {
		case "/quota":
			w.WriteHeader(quotaStatus)
			_, _ = w.Write([]byte(quotaBody))
		case "/subscription":
			_, _ = w.Write([]byte(antigravityTestSubscriptionFixture))
		default:
			http.NotFound(w, r)
		}
	}))
}

func withAntigravityURLs(t *testing.T, urls []string, subscription string) {
	t.Helper()
	oldURLs, oldSub := antigravityQuotaURLs, antigravitySubscriptionURL
	antigravityQuotaURLs, antigravitySubscriptionURL = urls, subscription
	t.Cleanup(func() {
		antigravityQuotaURLs, antigravitySubscriptionURL = oldURLs, oldSub
	})
}

func antigravityTestAuth() *coreauth.Auth {
	return &coreauth.Auth{
		Provider:   "antigravity",
		Attributes: map[string]string{"access_token": "test-token"},
		Metadata:   map[string]any{"project_id": "test-project"},
	}
}

func TestAntigravityFetcher(t *testing.T) {
	server := antigravityTestServer(t, http.StatusOK, antigravityTestQuotaFixture)
	defer server.Close()
	withAntigravityURLs(t, []string{server.URL + "/quota"}, server.URL+"/subscription")

	fetcher := NewAntigravityFetcher()
	if fetcher.Provider() != "antigravity" {
		t.Fatalf("provider = %q", fetcher.Provider())
	}
	snapshot, err := fetcher.Fetch(context.Background(), FetchRequest{Auth: antigravityTestAuth(), Client: server.Client()})
	if err != nil {
		t.Fatalf("Fetch: %v", err)
	}
	if snapshot == nil {
		t.Fatal("snapshot is nil")
	}
	if snapshot.Plan != "pro" {
		t.Fatalf("plan = %q, want pro", snapshot.Plan)
	}
	wantNames := []string{
		"claude-and-gpt-models/5h",
		"claude-and-gpt-models/7d",
		"gemini-models/5h",
		"gemini-models/7d",
		"gemini-models/monthly",
	}
	if len(snapshot.Windows) != len(wantNames) {
		t.Fatalf("windows = %+v, want %d windows", snapshot.Windows, len(wantNames))
	}
	for i, name := range wantNames {
		if snapshot.Windows[i].Name != name {
			t.Fatalf("window %d = %q, want %q", i, snapshot.Windows[i].Name, name)
		}
	}
	wantUsed := map[string]float64{
		"claude-and-gpt-models/5h": 50,
		"claude-and-gpt-models/7d": 10,
		"gemini-models/5h":         25,
		"gemini-models/7d":         60,
		"gemini-models/monthly":    0,
	}
	for _, window := range snapshot.Windows {
		if window.UsedPercent == nil || *window.UsedPercent != wantUsed[window.Name] {
			t.Fatalf("window %q used = %v, want %v", window.Name, window.UsedPercent, wantUsed[window.Name])
		}
		if window.ResetAt == nil {
			t.Fatalf("window %q has no reset", window.Name)
		}
	}
	weekly := snapshot.Windows[3]
	if got := weekly.ResetAt.UTC().Format("2006-01-02T15:04:05Z"); got != "2026-09-28T00:00:00Z" {
		t.Fatalf("gemini weekly reset = %s", got)
	}
}

func TestAntigravityFetcherFallback(t *testing.T) {
	var calls []string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls = append(calls, r.URL.Path)
		switch r.URL.Path {
		case "/first":
			http.Error(w, "boom", http.StatusInternalServerError)
		case "/second":
			_, _ = w.Write([]byte(antigravityTestQuotaFixture))
		case "/subscription":
			_, _ = w.Write([]byte(antigravityTestSubscriptionFixture))
		default:
			http.NotFound(w, r)
		}
	}))
	defer server.Close()
	withAntigravityURLs(t, []string{server.URL + "/first", server.URL + "/second"}, server.URL+"/subscription")

	snapshot, err := NewAntigravityFetcher().Fetch(context.Background(), FetchRequest{Auth: antigravityTestAuth(), Client: server.Client()})
	if err != nil {
		t.Fatalf("Fetch: %v", err)
	}
	if snapshot == nil || len(snapshot.Windows) != 5 {
		t.Fatalf("snapshot = %+v", snapshot)
	}
	if len(calls) < 2 || calls[0] != "/first" || calls[1] != "/second" {
		t.Fatalf("calls = %v, want fallback from /first to /second", calls)
	}
}

func TestAntigravityFetcherMissingProjectID(t *testing.T) {
	fetcher := NewAntigravityFetcher()
	auth := &coreauth.Auth{
		Provider:   "antigravity",
		Attributes: map[string]string{"access_token": "test-token"},
	}
	_, err := fetcher.Fetch(context.Background(), FetchRequest{Auth: auth, Client: http.DefaultClient})
	if err == nil || !strings.Contains(err.Error(), "project_id") {
		t.Fatalf("err = %v, want missing project_id error", err)
	}
}

func TestAntigravityFetcherEmptyGroups(t *testing.T) {
	server := antigravityTestServer(t, http.StatusOK, `{"groups": []}`)
	defer server.Close()
	withAntigravityURLs(t, []string{server.URL + "/quota"}, server.URL+"/subscription")

	_, err := NewAntigravityFetcher().Fetch(context.Background(), FetchRequest{Auth: antigravityTestAuth(), Client: server.Client()})
	if err == nil {
		t.Fatal("expected empty-groups error")
	}
}

func TestAntigravityFetcherSubscriptionFailureKeepsWindows(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/quota":
			_, _ = w.Write([]byte(antigravityTestQuotaFixture))
		case "/subscription":
			http.Error(w, "boom", http.StatusInternalServerError)
		default:
			http.NotFound(w, r)
		}
	}))
	defer server.Close()
	withAntigravityURLs(t, []string{server.URL + "/quota"}, server.URL+"/subscription")

	snapshot, err := NewAntigravityFetcher().Fetch(context.Background(), FetchRequest{Auth: antigravityTestAuth(), Client: server.Client()})
	if err != nil {
		t.Fatalf("Fetch: %v", err)
	}
	if snapshot == nil || len(snapshot.Windows) != 5 || snapshot.Plan != "" {
		t.Fatalf("snapshot = %+v, want windows with empty plan", snapshot)
	}
}
