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

// Fixture mirrors CPAMC tests/opencodeQuota.test.ts usageResponse().
const opencodeTestPayload = `{"usage": {
  "rolling": {"percent": 42, "status": "ok", "resetsAt": "2026-09-14T00:00:00.000Z"},
  "weekly": {"percent": 70, "status": "rate-limited", "resetsAt": "2026-09-21T00:00:00.000Z"},
  "monthly": {"percent": 5, "status": "ok", "resetsAt": "2026-10-13T00:00:00.000Z"}
}}`

func TestOpenCodeFetcher(t *testing.T) {
	const apiKey = "opencode-key"
	var gotAuth, gotPath string
	mux := http.NewServeMux()
	mux.HandleFunc("/v1/usage", func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
		gotAuth = r.Header.Get("Authorization")
		_, _ = w.Write([]byte(opencodeTestPayload))
	})
	server := httptest.NewServer(mux)
	defer server.Close()

	fetcher := NewOpenCodeFetcher()
	if fetcher.Provider() != "opencode" {
		t.Fatalf("provider = %q", fetcher.Provider())
	}
	auth := &coreauth.Auth{
		Provider:   "opencode",
		Attributes: map[string]string{"api_key": apiKey, "base_url": server.URL},
	}
	snapshot, err := fetcher.Fetch(context.Background(), FetchRequest{Auth: auth, Client: server.Client()})
	if err != nil {
		t.Fatalf("Fetch: %v", err)
	}
	if gotPath != "/v1/usage" {
		t.Fatalf("path = %q", gotPath)
	}
	if gotAuth != "Bearer "+apiKey {
		t.Fatalf("Authorization = %q", gotAuth)
	}
	if snapshot == nil || len(snapshot.Windows) != 3 {
		t.Fatalf("snapshot = %+v", snapshot)
	}
	byName := map[string]Window{}
	for _, window := range snapshot.Windows {
		byName[window.Name] = window
	}
	fiveHour := byName["5h"]
	if fiveHour.UsedPercent == nil || *fiveHour.UsedPercent != 42 || fiveHour.Status != "ok" {
		t.Fatalf("5h = %+v", fiveHour)
	}
	if fiveHour.ResetAt == nil || !fiveHour.ResetAt.Equal(time.Date(2026, 9, 14, 0, 0, 0, 0, time.UTC)) {
		t.Fatalf("5h reset = %v", fiveHour.ResetAt)
	}
	weekly := byName["7d"]
	if weekly.UsedPercent == nil || *weekly.UsedPercent != 70 || weekly.Status != "rate-limited" {
		t.Fatalf("weekly = %+v", weekly)
	}
	monthly := byName["monthly"]
	if monthly.UsedPercent == nil || *monthly.UsedPercent != 5 {
		t.Fatalf("monthly = %+v", monthly)
	}
	names := []string{snapshot.Windows[0].Name, snapshot.Windows[1].Name, snapshot.Windows[2].Name}
	if strings.Join(names, ",") != "5h,7d,monthly" {
		t.Fatalf("window order = %v", names)
	}
}

func TestOpenCodeFetcherErrors(t *testing.T) {
	fetcher := NewOpenCodeFetcher()

	if _, err := fetcher.Fetch(context.Background(), FetchRequest{Client: http.DefaultClient}); err == nil ||
		!strings.Contains(err.Error(), "api key") {
		t.Fatalf("missing credential error = %v", err)
	}
	denied := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusForbidden)
	}))
	defer denied.Close()
	deniedAuth := &coreauth.Auth{Attributes: map[string]string{"api_key": "key", "base_url": denied.URL}}
	if _, err := fetcher.Fetch(context.Background(), FetchRequest{Auth: deniedAuth, Client: denied.Client()}); err == nil {
		t.Fatal("expected non-2xx error")
	}

	// All-or-nothing: a partial report is rejected, never partially surfaced.
	partial := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"usage": {
      "rolling": {"percent": 1, "status": "ok", "resetsAt": "2026-09-14T00:00:00.000Z"}
    }}`))
	}))
	defer partial.Close()
	partialAuth := &coreauth.Auth{Attributes: map[string]string{"api_key": "key", "base_url": partial.URL}}
	if _, err := fetcher.Fetch(context.Background(), FetchRequest{Auth: partialAuth, Client: partial.Client()}); err == nil {
		t.Fatal("expected partial-report error")
	}
}
