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

const (
	claudeTestFiveHourReset = "2026-07-27T10:00:00.000000+00:00"
	claudeTestSevenDayReset = "2026-07-28T10:00:00.000000+00:00"
)

func claudeTestServer(t *testing.T, usageBody string, usageStatus int, profileBody string, profileStatus int, seen *http.Request) *httptest.Server {
	t.Helper()
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if seen != nil {
			*seen = *r
		}
		switch r.URL.Path {
		case claudeUsagePath:
			w.WriteHeader(usageStatus)
			_, _ = w.Write([]byte(usageBody))
		case claudeProfilePath:
			w.WriteHeader(profileStatus)
			_, _ = w.Write([]byte(profileBody))
		default:
			http.NotFound(w, r)
		}
	}))
}

func claudeTestAuth(server *httptest.Server) *coreauth.Auth {
	return &coreauth.Auth{
		Provider:   "claude",
		Attributes: map[string]string{"access_token": "claude-test-token", "base_url": server.URL},
	}
}

func claudeWindowByName(t *testing.T, snapshot *Snapshot, name string) Window {
	t.Helper()
	for _, window := range snapshot.Windows {
		if window.Name == name {
			return window
		}
	}
	t.Fatalf("window %q missing in %+v", name, snapshot.Windows)
	return Window{}
}

func TestClaudeFetcher(t *testing.T) {
	usage := `{"five_hour":{"utilization":10,"resets_at":"` + claudeTestFiveHourReset + `"},` +
		`"seven_day":{"utilization":20,"resets_at":"` + claudeTestSevenDayReset + `"},` +
		`"seven_day_opus":{"utilization":30,"resets_at":"` + claudeTestSevenDayReset + `"},` +
		`"limits":[{"kind":"weekly_scoped","group":"weekly","percent":64,"resets_at":"` + claudeTestFiveHourReset + `",` +
		`"is_active":true,"scope":{"model":{"id":null,"display_name":"Fable"}}}]}`
	profile := `{"account":{"has_claude_max":false,"has_claude_pro":true},` +
		`"organization":{"organization_type":"personal","subscription_status":"active"}}`
	var seen http.Request
	server := claudeTestServer(t, usage, http.StatusOK, profile, http.StatusOK, &seen)
	defer server.Close()

	fetcher := NewClaudeFetcher()
	if fetcher.Provider() != "claude" {
		t.Fatalf("provider = %q", fetcher.Provider())
	}
	snapshot, err := fetcher.Fetch(context.Background(), FetchRequest{Auth: claudeTestAuth(server), Client: server.Client()})
	if err != nil {
		t.Fatalf("Fetch: %v", err)
	}
	if snapshot == nil {
		t.Fatal("snapshot is nil")
	}
	if len(snapshot.Windows) != 4 {
		t.Fatalf("windows = %+v", snapshot.Windows)
	}
	for i, want := range []string{"5h", "7d", "fable/7d", "opus/7d"} {
		if snapshot.Windows[i].Name != want {
			t.Fatalf("windows[%d].Name = %q, want %q (%+v)", i, snapshot.Windows[i].Name, want, snapshot.Windows)
		}
	}
	fiveHour := claudeWindowByName(t, snapshot, "5h")
	if *fiveHour.UsedPercent != 10 {
		t.Fatalf("5h used = %v", *fiveHour.UsedPercent)
	}
	if !fiveHour.ResetAt.Equal(time.Date(2026, 7, 27, 10, 0, 0, 0, time.UTC)) {
		t.Fatalf("5h reset = %v", fiveHour.ResetAt)
	}
	fable := claudeWindowByName(t, snapshot, "fable/7d")
	if *fable.UsedPercent != 64 {
		t.Fatalf("fable/7d used = %v", *fable.UsedPercent)
	}
	opus := claudeWindowByName(t, snapshot, "opus/7d")
	if *opus.UsedPercent != 30 {
		t.Fatalf("opus/7d used = %v", *opus.UsedPercent)
	}
	if snapshot.Plan != "Pro" {
		t.Fatalf("plan = %q", snapshot.Plan)
	}
	if got := seen.Header.Get("Authorization"); got != "Bearer claude-test-token" {
		t.Fatalf("authorization = %q", got)
	}
	if got := seen.Header.Get("anthropic-beta"); got != "oauth-2025-04-20" {
		t.Fatalf("anthropic-beta = %q", got)
	}
}

func TestClaudeFetcherLegacyFable(t *testing.T) {
	usage := `{"five_hour":{"utilization":10,"resets_at":null},` +
		`"iguana_necktie":{"utilization":41,"resets_at":"` + claudeTestSevenDayReset + `"}}`
	server := claudeTestServer(t, usage, http.StatusOK, `{}`, http.StatusOK, nil)
	defer server.Close()

	snapshot, err := NewClaudeFetcher().Fetch(context.Background(), FetchRequest{Auth: claudeTestAuth(server), Client: server.Client()})
	if err != nil {
		t.Fatalf("Fetch: %v", err)
	}
	if snapshot == nil || len(snapshot.Windows) != 2 {
		t.Fatalf("snapshot = %+v", snapshot)
	}
	fable := claudeWindowByName(t, snapshot, "fable/7d")
	if *fable.UsedPercent != 41 {
		t.Fatalf("fable/7d used = %v", *fable.UsedPercent)
	}
	if snapshot.Windows[0].Name != "5h" || snapshot.Windows[1].Name != "fable/7d" {
		t.Fatalf("order = %+v", snapshot.Windows)
	}
}

func TestClaudeFetcherPlanTypes(t *testing.T) {
	usage := `{"five_hour":{"utilization":12.345,"resets_at":null}}`
	server := claudeTestServer(t, usage, http.StatusOK, "", http.StatusOK, nil)
	defer server.Close()
	// Rewrite the profile handler body per subtest via a mutable variable.
	var current string
	server.Config.Handler = http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case claudeUsagePath:
			_, _ = w.Write([]byte(usage))
		case claudeProfilePath:
			_, _ = w.Write([]byte(current))
		default:
			http.NotFound(w, r)
		}
	})

	cases := []struct{ profile, plan string }{
		{`{"organization":{"organization_type":"claude_team","subscription_status":"active"}}`, "Team"},
		{`{"account":{"has_claude_max":true}}`, "Max"},
		{`{"account":{"has_claude_max":1}}`, "Max"},
		{`{"account":{"has_claude_pro":"yes"}}`, "Pro"},
		{`{"account":{"has_claude_max":false,"has_claude_pro":false}}`, "Free"},
		{`{}`, ""},
	}
	for _, tc := range cases {
		current = tc.profile
		snapshot, err := NewClaudeFetcher().Fetch(context.Background(), FetchRequest{Auth: claudeTestAuth(server), Client: server.Client()})
		if err != nil {
			t.Fatalf("profile %s: Fetch: %v", tc.profile, err)
		}
		if snapshot == nil || snapshot.Plan != tc.plan {
			t.Fatalf("profile %s: plan = %+v", tc.profile, snapshot)
		}
		if tc.profile == `{}` {
			// Rounding: 12.345 reads back at two decimals.
			if *snapshot.Windows[0].UsedPercent != 12.35 {
				t.Fatalf("used = %v", *snapshot.Windows[0].UsedPercent)
			}
		}
	}
}

func TestClaudeFetcherProfileFailureKeepsWindows(t *testing.T) {
	usage := `{"seven_day":{"utilization":20,"resets_at":"` + claudeTestSevenDayReset + `"}}`
	server := claudeTestServer(t, usage, http.StatusOK, `boom`, http.StatusInternalServerError, nil)
	defer server.Close()

	snapshot, err := NewClaudeFetcher().Fetch(context.Background(), FetchRequest{Auth: claudeTestAuth(server), Client: server.Client()})
	if err != nil {
		t.Fatalf("Fetch: %v", err)
	}
	if snapshot == nil || len(snapshot.Windows) != 1 || snapshot.Windows[0].Name != "7d" {
		t.Fatalf("snapshot = %+v", snapshot)
	}
	if snapshot.Plan != "" {
		t.Fatalf("plan = %q", snapshot.Plan)
	}
}

func TestClaudeFetcherErrors(t *testing.T) {
	server := claudeTestServer(t, `{}`, http.StatusOK, `{}`, http.StatusOK, nil)
	defer server.Close()
	fetcher := NewClaudeFetcher()

	if _, err := fetcher.Fetch(context.Background(), FetchRequest{Auth: &coreauth.Auth{}, Client: server.Client()}); err == nil {
		t.Fatal("expected missing-token error")
	} else if strings.Contains(err.Error(), "claude-test-token") {
		t.Fatalf("error leaks credential: %v", err)
	}

	malformed := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{not json`))
	}))
	defer malformed.Close()
	auth := &coreauth.Auth{
		Attributes: map[string]string{"access_token": "claude-test-token", "base_url": malformed.URL},
	}
	if _, err := fetcher.Fetch(context.Background(), FetchRequest{Auth: auth, Client: malformed.Client()}); err == nil {
		t.Fatal("expected malformed-payload error")
	} else if strings.Contains(err.Error(), "claude-test-token") {
		t.Fatalf("error leaks credential: %v", err)
	}

	failing := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "nope", http.StatusUnauthorized)
	}))
	defer failing.Close()
	failingAuth := &coreauth.Auth{
		Attributes: map[string]string{"access_token": "claude-test-token", "base_url": failing.URL},
	}
	if _, err := fetcher.Fetch(context.Background(), FetchRequest{Auth: failingAuth, Client: failing.Client()}); err == nil {
		t.Fatal("expected status error")
	}

	empty, err := fetcher.Fetch(context.Background(), FetchRequest{Auth: claudeTestAuth(server), Client: server.Client()})
	if err != nil {
		t.Fatalf("empty usage: %v", err)
	}
	if empty != nil {
		t.Fatalf("empty usage = %+v, want nil", empty)
	}
}
