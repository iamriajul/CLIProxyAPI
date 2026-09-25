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

// Ported from the CPAMC codexQuota test's current-usage fixture: a weekly
// primary window plus one additional weekly limit.
const codexTestCurrentUsage = `{"plan_type":"pro",` +
	`"rate_limit":{"allowed":true,"limit_reached":false,` +
	`"primary_window":{"used_percent":1,"limit_window_seconds":604800,"reset_after_seconds":601888,"reset_at":1785902974},` +
	`"secondary_window":null},` +
	`"code_review_rate_limit":null,` +
	`"additional_rate_limits":[{"limit_name":"GPT-5.3-Codex-Spark","metered_feature":"codex_bengalfox",` +
	`"rate_limit":{"allowed":true,"limit_reached":false,` +
	`"primary_window":{"used_percent":0,"limit_window_seconds":604800,"reset_after_seconds":602111,"reset_at":1785903197},` +
	`"secondary_window":null}}],` +
	`"rate_limit_reset_credits":{"available_count":1,"applicable_available_count":0}}`

func codexTestServer(t *testing.T, body string, status int, seen *http.Request, seenAccount *string) *httptest.Server {
	t.Helper()
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if seen != nil {
			*seen = *r
		}
		if seenAccount != nil {
			*seenAccount = r.Header.Get(codexAccountHeader)
		}
		if r.URL.Path != codexUsagePath {
			http.NotFound(w, r)
			return
		}
		w.WriteHeader(status)
		_, _ = w.Write([]byte(body))
	}))
}

func codexTestAuth(server *httptest.Server) *coreauth.Auth {
	return &coreauth.Auth{
		Provider: "codex",
		Attributes: map[string]string{
			"access_token":       "codex-test-token",
			"base_url":           server.URL,
			"chatgpt_account_id": "acct-test",
		},
	}
}

func TestCodexFetcher(t *testing.T) {
	var seen http.Request
	var account string
	server := codexTestServer(t, codexTestCurrentUsage, http.StatusOK, &seen, &account)
	defer server.Close()

	fetcher := NewCodexFetcher()
	if fetcher.Provider() != "codex" {
		t.Fatalf("provider = %q", fetcher.Provider())
	}
	snapshot, err := fetcher.Fetch(context.Background(), FetchRequest{Auth: codexTestAuth(server), Client: server.Client()})
	if err != nil {
		t.Fatalf("Fetch: %v", err)
	}
	if snapshot == nil {
		t.Fatal("snapshot is nil")
	}
	if len(snapshot.Windows) != 2 {
		t.Fatalf("windows = %+v", snapshot.Windows)
	}
	// Weekly-classified primaries sort before the grouped window.
	if snapshot.Windows[0].Name != "7d" || *snapshot.Windows[0].UsedPercent != 1 {
		t.Fatalf("windows[0] = %+v", snapshot.Windows[0])
	}
	if snapshot.Windows[1].Name != "gpt-5-3-codex-spark/7d" || *snapshot.Windows[1].UsedPercent != 0 {
		t.Fatalf("windows[1] = %+v", snapshot.Windows[1])
	}
	if !snapshot.Windows[0].ResetAt.Equal(time.Unix(1785902974, 0).UTC()) {
		t.Fatalf("7d reset = %v", snapshot.Windows[0].ResetAt)
	}
	if snapshot.Plan != "Pro 20x" {
		t.Fatalf("plan = %q", snapshot.Plan)
	}
	if account != "acct-test" {
		t.Fatalf("account header = %q", account)
	}
	if got := seen.Header.Get("Authorization"); got != "Bearer codex-test-token" {
		t.Fatalf("authorization = %q", got)
	}
}

func TestCodexFetcherClassification(t *testing.T) {
	usage := `{"plan_type":"Team",` +
		`"rate_limit":{"primary_window":{"used_percent":50,"limit_window_seconds":18000,"reset_after_seconds":3600},` +
		`"secondary_window":{"used_percent":25,"limit_window_seconds":2592000,"reset_after_seconds":7200}},` +
		`"code_review_rate_limit":{"primary_window":{"used_percent":10,"limit_window_seconds":18000,"reset_after_seconds":100},` +
		`"secondary_window":{"used_percent":5,"limit_window_seconds":604800,"reset_after_seconds":200}},` +
		`"additional_rate_limits":[{"limit_name":"Extra Jobs","rate_limit":` +
		`{"primary_window":{"used_percent":7,"limit_window_seconds":18000,"reset_after_seconds":300}}}]}`
	server := codexTestServer(t, usage, http.StatusOK, nil, nil)
	defer server.Close()

	before := time.Now().Truncate(time.Second)
	snapshot, err := NewCodexFetcher().Fetch(context.Background(), FetchRequest{Auth: codexTestAuth(server), Client: server.Client()})
	after := time.Now().Truncate(time.Second).Add(2 * time.Second)
	if err != nil {
		t.Fatalf("Fetch: %v", err)
	}
	if snapshot == nil {
		t.Fatal("snapshot is nil")
	}
	var names []string
	used := map[string]float64{}
	for _, window := range snapshot.Windows {
		names = append(names, window.Name)
		used[window.Name] = *window.UsedPercent
		if window.ResetAt == nil {
			t.Fatalf("%s has no reset", window.Name)
		}
	}
	want := []string{"5h", "monthly", "code-review/5h", "code-review/7d", "extra-jobs/5h"}
	if strings.Join(names, ",") != strings.Join(want, ",") {
		t.Fatalf("windows = %v, want %v", names, want)
	}
	for name, percent := range map[string]float64{"5h": 50, "monthly": 25, "code-review/7d": 5, "extra-jobs/5h": 7} {
		if used[name] != percent {
			t.Fatalf("%s used = %v, want %v", name, used[name], percent)
		}
	}
	// Relative resets resolve against fetch time, truncated to seconds.
	fiveHour := snapshot.Windows[0]
	if fiveHour.ResetAt.Before(before.Add(3600*time.Second)) || fiveHour.ResetAt.After(after.Add(3600*time.Second)) {
		t.Fatalf("5h reset = %v", fiveHour.ResetAt)
	}
	if snapshot.Plan != "Team" {
		t.Fatalf("plan = %q", snapshot.Plan)
	}
}

func TestCodexFetcherLimitReached(t *testing.T) {
	// A reached limit without used_percent but with a reset reads as consumed.
	usage := `{"rate_limit":{"limit_reached":true,` +
		`"primary_window":{"limit_window_seconds":18000,"reset_after_seconds":60}},` +
		`"additional_rate_limits":[{"limit_name":"Ghost","rate_limit":{"primary_window":{}}}]}`
	server := codexTestServer(t, usage, http.StatusOK, nil, nil)
	defer server.Close()

	snapshot, err := NewCodexFetcher().Fetch(context.Background(), FetchRequest{Auth: codexTestAuth(server), Client: server.Client()})
	if err != nil {
		t.Fatalf("Fetch: %v", err)
	}
	// The empty additional window is a phantom and must be skipped.
	if snapshot == nil || len(snapshot.Windows) != 1 {
		t.Fatalf("snapshot = %+v", snapshot)
	}
	if snapshot.Windows[0].Name != "5h" || *snapshot.Windows[0].UsedPercent != 100 {
		t.Fatalf("window = %+v", snapshot.Windows[0])
	}
}

func TestCodexFetcherErrors(t *testing.T) {
	server := codexTestServer(t, `{"rate_limit":null}`, http.StatusOK, nil, nil)
	defer server.Close()
	fetcher := NewCodexFetcher()

	if _, err := fetcher.Fetch(context.Background(), FetchRequest{Auth: &coreauth.Auth{}, Client: server.Client()}); err == nil {
		t.Fatal("expected missing-token error")
	} else if strings.Contains(err.Error(), "codex-test-token") {
		t.Fatalf("error leaks credential: %v", err)
	}

	malformed := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{not json`))
	}))
	defer malformed.Close()
	auth := &coreauth.Auth{
		Attributes: map[string]string{"access_token": "codex-test-token", "base_url": malformed.URL},
	}
	if _, err := fetcher.Fetch(context.Background(), FetchRequest{Auth: auth, Client: malformed.Client()}); err == nil {
		t.Fatal("expected malformed-payload error")
	} else if strings.Contains(err.Error(), "codex-test-token") {
		t.Fatalf("error leaks credential: %v", err)
	}

	// Valid JSON with zero parseable windows is no data, not an error.
	empty, err := fetcher.Fetch(context.Background(), FetchRequest{Auth: codexTestAuth(server), Client: server.Client()})
	if err != nil {
		t.Fatalf("empty usage: %v", err)
	}
	if empty != nil {
		t.Fatalf("empty usage = %+v, want nil", empty)
	}
}
