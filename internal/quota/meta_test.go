package quota

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	coreauth "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/auth"
)

// Fixture mirrors CPAMC tests/fixtures/metaQuota.ts (synthetic credentials).
const metaTestPayload = `{
  "api_key": "LLM|fixture-only|not-a-real-key",
  "base_url": "https://api.meta.ai/v1",
  "is_subs_active": true,
  "subs_tier_id": "fixture-tier",
  "subs_tier_name": "Muse Code Everyday Usage",
  "subs_usage": {
    "window": {"used_percent": 2, "window_duration_mins": 300, "resets_at": 1789678120},
    "weekly": {"used_percent": 0, "resets_at": 1789948800},
    "tier": "fixture-tier"
  }
}`

func TestMetaFetcher(t *testing.T) {
	const dcaToken = "dca:fixture-token"
	var gotAuth, gotVersion, gotBody string
	var gotMethod string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotMethod = r.Method
		gotAuth = r.Header.Get("Authorization")
		gotVersion = r.Header.Get("x-api-version")
		raw, _ := io.ReadAll(r.Body)
		gotBody = string(raw)
		_, _ = w.Write([]byte(metaTestPayload))
	}))
	defer server.Close()

	previousURL := metaMuseQuotaURL
	metaMuseQuotaURL = server.URL
	defer func() { metaMuseQuotaURL = previousURL }()

	fetcher := NewMetaFetcher()
	if fetcher.Provider() != "meta" {
		t.Fatalf("provider = %q", fetcher.Provider())
	}
	auth := &coreauth.Auth{Attributes: map[string]string{"dca_token": dcaToken}}
	snapshot, err := fetcher.Fetch(context.Background(), FetchRequest{Auth: auth, Client: server.Client()})
	if err != nil {
		t.Fatalf("Fetch: %v", err)
	}
	if gotMethod != http.MethodPost {
		t.Fatalf("method = %q", gotMethod)
	}
	if gotAuth != "Bearer "+dcaToken {
		t.Fatalf("Authorization = %q", gotAuth)
	}
	if gotVersion != "1.0.0" {
		t.Fatalf("x-api-version = %q", gotVersion)
	}
	if gotBody != "{}" {
		t.Fatalf("body = %q", gotBody)
	}
	if snapshot == nil {
		t.Fatal("snapshot is nil")
	}
	if snapshot.Plan != "Muse Code Everyday Usage" {
		t.Fatalf("plan = %q", snapshot.Plan)
	}
	if len(snapshot.Windows) != 2 {
		t.Fatalf("windows = %+v", snapshot.Windows)
	}
	byName := map[string]Window{}
	for _, window := range snapshot.Windows {
		byName[window.Name] = window
	}
	rolling := byName["window"]
	if rolling.UsedPercent == nil || *rolling.UsedPercent != 2 {
		t.Fatalf("window used = %v", rolling.UsedPercent)
	}
	if rolling.ResetAt == nil || !rolling.ResetAt.Equal(time.Unix(1789678120, 0).UTC()) {
		t.Fatalf("window reset = %v", rolling.ResetAt)
	}
	weekly := byName["7d"]
	if weekly.UsedPercent == nil || *weekly.UsedPercent != 0 {
		t.Fatalf("weekly used = %v", weekly.UsedPercent)
	}
	if weekly.ResetAt == nil || !weekly.ResetAt.Equal(time.Unix(1789948800, 0).UTC()) {
		t.Fatalf("weekly reset = %v", weekly.ResetAt)
	}
}

func TestMetaFetcherRejectsMintedKey(t *testing.T) {
	fetcher := NewMetaFetcher()
	// The minted LLM key is never a valid quota credential.
	auth := &coreauth.Auth{Attributes: map[string]string{"api_key": "LLM|minted|not-a-real-key"}}
	if _, err := fetcher.Fetch(context.Background(), FetchRequest{Auth: auth, Client: http.DefaultClient}); err == nil ||
		!strings.Contains(err.Error(), "dca_token") {
		t.Fatalf("minted-key error = %v", err)
	}
	if _, err := fetcher.Fetch(context.Background(), FetchRequest{Client: http.DefaultClient}); err == nil {
		t.Fatal("expected missing-token error")
	}
}

func TestMetaFetcherErrors(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusUnauthorized)
	}))
	defer server.Close()
	previousURL := metaMuseQuotaURL
	metaMuseQuotaURL = server.URL
	defer func() { metaMuseQuotaURL = previousURL }()

	auth := &coreauth.Auth{Attributes: map[string]string{"dca_token": "dca:fixture-token"}}
	if _, err := NewMetaFetcher().Fetch(context.Background(), FetchRequest{Auth: auth, Client: server.Client()}); err == nil {
		t.Fatal("expected non-2xx error")
	}

	invalidServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`["not", "an", "object"]`))
	}))
	defer invalidServer.Close()
	metaMuseQuotaURL = invalidServer.URL
	if _, err := NewMetaFetcher().Fetch(context.Background(), FetchRequest{Auth: auth, Client: invalidServer.Client()}); err == nil {
		t.Fatal("expected invalid-response error")
	}
}
