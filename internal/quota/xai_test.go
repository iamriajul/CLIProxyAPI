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

// Fixtures mirror CPAMC tests/xaiPaidQuotaFallback.test.ts billing shapes.
const xaiWeeklyPayload = `{"config": {
  "currentPeriod": {"type": "weekly", "start": "2026-09-21T00:00:00Z", "end": "2026-09-28T00:00:00Z"},
  "creditUsagePercent": 25,
  "productUsage": [{"product": "grok", "usagePercent": 50}]
}}`

const xaiMonthlyPayload = `{"config": {
  "monthlyLimit": {"val": 10000},
  "used": {"val": 2500},
  "billingPeriodStart": "2026-09-01T00:00:00Z",
  "billingPeriodEnd": "2026-10-01T00:00:00Z"
}}`

func TestXaiFetcher(t *testing.T) {
	var gotAuth, gotUserID string
	mux := http.NewServeMux()
	mux.HandleFunc("/weekly", func(w http.ResponseWriter, r *http.Request) {
		gotAuth = r.Header.Get("Authorization")
		gotUserID = r.Header.Get("x-userid")
		_, _ = w.Write([]byte(xaiWeeklyPayload))
	})
	mux.HandleFunc("/monthly", func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(xaiMonthlyPayload))
	})
	server := httptest.NewServer(mux)
	defer server.Close()

	previousWeekly, previousMonthly, previousChat := xaiBillingWeeklyURL, xaiBillingMonthlyURL, xaiChatURL
	xaiBillingWeeklyURL = server.URL + "/weekly"
	xaiBillingMonthlyURL = server.URL + "/monthly"
	xaiChatURL = server.URL + "/chat"
	defer func() {
		xaiBillingWeeklyURL, xaiBillingMonthlyURL, xaiChatURL = previousWeekly, previousMonthly, previousChat
	}()

	fetcher := NewXaiFetcher()
	if fetcher.Provider() != "xai" {
		t.Fatalf("provider = %q", fetcher.Provider())
	}
	auth := &coreauth.Auth{
		Provider:   "xai",
		Metadata:   map[string]any{"access_token": "xai-token", "user_id": "user-123"},
		Attributes: map[string]string{},
	}
	snapshot, err := fetcher.Fetch(context.Background(), FetchRequest{Auth: auth, Client: server.Client()})
	if err != nil {
		t.Fatalf("Fetch: %v", err)
	}
	if gotAuth != "Bearer xai-token" {
		t.Fatalf("Authorization = %q", gotAuth)
	}
	if gotUserID != "user-123" {
		t.Fatalf("x-userid = %q", gotUserID)
	}
	if snapshot == nil {
		t.Fatal("snapshot is nil")
	}
	byName := map[string]Window{}
	for _, window := range snapshot.Windows {
		byName[window.Name] = window
	}
	// Included spend ratio: 2500/10000 = 25% used.
	monthly := byName["monthly"]
	if monthly.UsedPercent == nil || *monthly.UsedPercent != 25 {
		t.Fatalf("monthly used = %v", monthly.UsedPercent)
	}
	if monthly.ResetAt != nil {
		t.Fatalf("monthly carries a billing rollover, not a quota reset: %v", monthly.ResetAt)
	}
	weekly := byName["7d"]
	if weekly.UsedPercent == nil || *weekly.UsedPercent != 25 {
		t.Fatalf("weekly used = %v", weekly.UsedPercent)
	}
	wantReset := time.Date(2026, 9, 28, 0, 0, 0, 0, time.UTC)
	if weekly.ResetAt == nil || !weekly.ResetAt.Equal(wantReset) {
		t.Fatalf("weekly reset = %v", weekly.ResetAt)
	}
	product := byName["product/grok"]
	if product.UsedPercent == nil || *product.UsedPercent != 50 {
		t.Fatalf("product/grok used = %v", product.UsedPercent)
	}
	names := []string{}
	for _, window := range snapshot.Windows {
		names = append(names, window.Name)
	}
	want := []string{"7d", "monthly", "product/grok"}
	if strings.Join(names, ",") != strings.Join(want, ",") {
		t.Fatalf("window order = %v", names)
	}
}

func TestXaiFetcherPaidHealthFallback(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("/weekly", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusUnauthorized)
	})
	mux.HandleFunc("/monthly", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusUnauthorized)
	})
	mux.HandleFunc("/chat", func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"choices": []}`))
	})
	server := httptest.NewServer(mux)
	defer server.Close()

	previousWeekly, previousMonthly, previousChat := xaiBillingWeeklyURL, xaiBillingMonthlyURL, xaiChatURL
	xaiBillingWeeklyURL = server.URL + "/weekly"
	xaiBillingMonthlyURL = server.URL + "/monthly"
	xaiChatURL = server.URL + "/chat"
	defer func() {
		xaiBillingWeeklyURL, xaiBillingMonthlyURL, xaiChatURL = previousWeekly, previousMonthly, previousChat
	}()

	auth := &coreauth.Auth{Attributes: map[string]string{"access_token": "paid-token"}}
	snapshot, err := NewXaiFetcher().Fetch(context.Background(), FetchRequest{Auth: auth, Client: server.Client()})
	if err != nil {
		t.Fatalf("Fetch: %v", err)
	}
	if snapshot == nil || snapshot.Plan != "Paid" || len(snapshot.Windows) != 0 {
		t.Fatalf("paid snapshot = %+v", snapshot)
	}
}

func TestXaiFetcherErrors(t *testing.T) {
	fetcher := NewXaiFetcher()

	if _, err := fetcher.Fetch(context.Background(), FetchRequest{Client: http.DefaultClient}); err == nil ||
		!strings.Contains(err.Error(), "access token") {
		t.Fatalf("missing credential error = %v", err)
	}

	mux := http.NewServeMux()
	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
	})
	server := httptest.NewServer(mux)
	defer server.Close()
	previousWeekly, previousMonthly, previousChat := xaiBillingWeeklyURL, xaiBillingMonthlyURL, xaiChatURL
	xaiBillingWeeklyURL = server.URL + "/weekly"
	xaiBillingMonthlyURL = server.URL + "/monthly"
	xaiChatURL = server.URL + "/chat"
	defer func() {
		xaiBillingWeeklyURL, xaiBillingMonthlyURL, xaiChatURL = previousWeekly, previousMonthly, previousChat
	}()

	auth := &coreauth.Auth{Attributes: map[string]string{"access_token": "token"}}
	if _, err := fetcher.Fetch(context.Background(), FetchRequest{Auth: auth, Client: server.Client()}); err == nil {
		t.Fatal("expected error when billing and health both fail")
	}
}
