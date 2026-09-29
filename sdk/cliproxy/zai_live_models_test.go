package cliproxy

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	internalregistry "github.com/router-for-me/CLIProxyAPI/v8/internal/registry"
	coreauth "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/auth"
	"github.com/router-for-me/CLIProxyAPI/v8/sdk/config"
)

// zaiDiscoveryCapturedPayload is the same captured response the registry
// converter is pinned to, re-serialized compactly so the runtime path is
// exercised against the same fields. The registry fixture is the verbatim
// record; this one exists only so the two runtime tests that need a response
// body do not depend on the other package's test data.
const zaiDiscoveryCapturedPayload = `{"models":[
  {"slug":"glm-5.3","display_name":"glm-5.3","description":"Z.ai's latest flagship model",
   "context_window":1048576,"max_context_window":1048576,"effective_context_window_percent":95,
   "input_modalities":["text"],
   "supported_reasoning_levels":[{"effort":"low"},{"effort":"high"},{"effort":"max"}],
   "supports_parallel_tool_calls":true,"supports_reasoning_summaries":true,
   "truncation_policy":{"limit":10000,"mode":"bytes"},"visibility":"list"},
  {"slug":"glm-5-turbo","display_name":"glm-5-turbo","description":"Agent-optimized model",
   "context_window":204800,"max_context_window":204800,"effective_context_window_percent":95,
   "input_modalities":["text"],
   "supported_reasoning_levels":[],
   "supports_parallel_tool_calls":true,"supports_reasoning_summaries":true,
   "truncation_policy":{"limit":10000,"mode":"bytes"},"visibility":"list"}
]}`

func zaiDiscoveryAuth(id, key string) *coreauth.Auth {
	return &coreauth.Auth{
		ID:       id,
		Provider: "zai",
		Status:   coreauth.StatusActive,
		Metadata: map[string]any{"access_token": key},
	}
}

// newZaiDiscoveryService points the credential at a test server standing in for
// the Z.AI origin, and returns the service, the server, and a hit counter.
func newZaiDiscoveryService(t *testing.T, handler http.HandlerFunc) (*Service, *httptest.Server, *int) {
	t.Helper()
	hits := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits++
		w.Header().Set("Content-Type", "application/json")
		handler(w, r)
	}))
	t.Cleanup(server.Close)

	previousNow := zaiLiveModelsNowFunc
	zaiLiveModelsNowFunc = time.Now
	t.Cleanup(func() { zaiLiveModelsNowFunc = previousNow })
	resetZaiLiveModelsLedgerForTest()
	t.Cleanup(resetZaiLiveModelsLedgerForTest)
	return &Service{cfg: &config.Config{}}, server, &hits
}

// zaiAuthForServer builds a credential whose recorded base URL is the test
// server's protocol base, the same shape an imported Z.AI credential has.
func zaiAuthForServer(id, key, serverURL string) *coreauth.Auth {
	auth := zaiDiscoveryAuth(id, key)
	auth.Attributes = map[string]string{"base_url": serverURL + "/api/anthropic"}
	return auth
}

// zaiOriginForTest resolves a test server URL to the origin the credential
// records, matching what a real imported credential looks like.
func zaiOriginForTest(rawURL string) string {
	origin, _ := zaiOriginFromBaseURL(rawURL)
	return origin
}

// TestZaiLiveDiscoveryRegistersDiscoveredLanes pins the runtime path end to
// end: a credential registering for the first time publishes the offline
// snapshot immediately, then the discovered plan lanes supersede it. Skipping
// that second registration would leave clients on a stale list, which is the
// defect live discovery exists to remove.
func TestZaiLiveDiscoveryRegistersDiscoveredLanes(t *testing.T) {
	var gotPath, gotAuth string
	service, server, _ := newZaiDiscoveryService(t, func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
		gotAuth = r.Header.Get("Authorization")
		_, _ = w.Write([]byte(zaiDiscoveryCapturedPayload))
	})
	auth := zaiAuthForServer("auth-zai-discovery", "ak-1.2", server.URL)

	registry := GlobalModelRegistry()
	registry.UnregisterClient(auth.ID)
	t.Cleanup(func() { registry.UnregisterClient(auth.ID) })

	service.registerModelsForAuth(context.Background(), auth)

	models := registry.GetModelsForClient(auth.ID)
	ids := make([]string, 0, len(models))
	for _, model := range models {
		ids = append(ids, model.ID)
	}
	if len(ids) != 2 || ids[0] != "glm-5-turbo" || ids[1] != "glm-5.3" {
		t.Fatalf("registered lanes = %v, want the discovered plan lanes", ids)
	}
	if gotPath != "/api/v1/models" {
		t.Fatalf("discovery path = %q, want /api/v1/models", gotPath)
	}
	// Z.AI rejects the Bearer prefix, so the key must go out verbatim.
	if gotAuth != "ak-1.2" {
		t.Fatalf("Authorization = %q, want the key verbatim", gotAuth)
	}
	var flagship *internalregistry.ModelInfo
	for _, model := range models {
		if model != nil && model.ID == "glm-5.3" {
			flagship = model
		}
	}
	if flagship == nil || flagship.ContextLength != 1048576*95/100 {
		t.Fatalf("flagship context length = %d, want the effective window", flagship.ContextLength)
	}
}

// TestZaiLiveDiscoveryRejectedKeyKeepsOfflineLanes pins the 200-with-error-body
// case at the level that matters: Z.AI answering HTTP 200 with an error body
// must not empty the lane, and must not be retried on every registration.
func TestZaiLiveDiscoveryRejectedKeyKeepsOfflineLanes(t *testing.T) {
	service, server, hits := newZaiDiscoveryService(t, func(w http.ResponseWriter, r *http.Request) {
		// Deliberately HTTP 200: this is what Z.AI really answers.
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"code":401,"msg":"token expired or incorrect","success":false}`))
	})
	auth := zaiAuthForServer("auth-zai-rejected", "ak-bad.bad", server.URL)

	registry := GlobalModelRegistry()
	registry.UnregisterClient(auth.ID)
	t.Cleanup(func() { registry.UnregisterClient(auth.ID) })

	service.registerModelsForAuth(context.Background(), auth)
	first := len(registry.GetModelsForClient(auth.ID))
	if first == 0 {
		t.Fatal("a rejected credential unregistered the lane instead of leaving it on the offline snapshot")
	}
	if first < 7 {
		t.Fatalf("registered %d lanes, want the offline snapshot's 7", first)
	}

	// A re-registration inside the suppression window must not spend another
	// request to be told the same key is bad.
	service.registerModelsForAuth(context.Background(), auth)
	if *hits != 1 {
		t.Fatalf("discovery attempts = %d, want 1 (the rejection must suppress retries)", *hits)
	}
	if got := len(registry.GetModelsForClient(auth.ID)); got != first {
		t.Fatalf("lane count changed after a suppressed retry: %d -> %d", first, got)
	}
}

// TestZaiLiveDiscoveryMissingCredentialSkipsNetwork pins that a credential with
// no key never reaches the network, and the lane stays on the offline snapshot
// rather than being emptied.
func TestZaiLiveDiscoveryMissingCredentialSkipsNetwork(t *testing.T) {
	service, server, hits := newZaiDiscoveryService(t, func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(zaiDiscoveryCapturedPayload))
	})
	auth := zaiAuthForServer("auth-zai-nokey", "", server.URL)

	registry := GlobalModelRegistry()
	registry.UnregisterClient(auth.ID)
	t.Cleanup(func() { registry.UnregisterClient(auth.ID) })

	service.registerModelsForAuth(context.Background(), auth)
	if *hits != 0 {
		t.Fatalf("discovery requests = %d, want 0 without a credential", *hits)
	}
	if got := len(registry.GetModelsForClient(auth.ID)); got < 7 {
		t.Fatalf("registered %d lanes, want the offline snapshot's 7", got)
	}
	if _, err := service.RefreshZaiLiveModelsForAuth(context.Background(), auth); err == nil {
		t.Fatal("RefreshZaiLiveModelsForAuth reported success without a credential")
	}
}

// TestZaiLiveDiscoveryTransportFailureKeepsOfflineLanes pins that an upstream
// fault degrades to the offline catalog rather than to an empty lane, and that
// the short fault suppression is used instead of the credential one.
func TestZaiLiveDiscoveryTransportFailureKeepsOfflineLanes(t *testing.T) {
	service, server, _ := newZaiDiscoveryService(t, func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusBadGateway)
		_, _ = w.Write([]byte("upstream down"))
	})
	auth := zaiAuthForServer("auth-zai-transport", "ak-1.2", server.URL)

	registry := GlobalModelRegistry()
	registry.UnregisterClient(auth.ID)
	t.Cleanup(func() { registry.UnregisterClient(auth.ID) })

	service.registerModelsForAuth(context.Background(), auth)
	if got := len(registry.GetModelsForClient(auth.ID)); got < 7 {
		t.Fatalf("registered %d lanes, want the offline snapshot's 7", got)
	}
	// A transport fault is not a credential verdict, so the short window must
	// have been used: advancing past it must permit another attempt.
	zaiLiveModelsNowFunc = func() time.Time { return time.Now().Add(2 * time.Minute) }
	changed, err := service.RefreshZaiLiveModelsForAuth(context.Background(), auth)
	if err == nil {
		t.Fatal("a failing endpoint reported a successful discovery")
	}
	if changed {
		t.Fatal("a failing discovery reported a lane change")
	}
}

// TestZaiLiveDiscoveryChangeReregistersOnce pins that a genuine lane change is
// re-registered exactly once, while an unchanged roster is not re-registered at
// all — otherwise every periodic re-registration would churn the registry.
func TestZaiLiveDiscoveryChangeReregistersOnce(t *testing.T) {
	service, server, hits := newZaiDiscoveryService(t, func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(zaiDiscoveryCapturedPayload))
	})
	auth := zaiAuthForServer("auth-zai-steady", "ak-1.2", server.URL)

	registry := GlobalModelRegistry()
	registry.UnregisterClient(auth.ID)
	t.Cleanup(func() { registry.UnregisterClient(auth.ID) })

	changed, err := service.RefreshZaiLiveModelsForAuth(context.Background(), auth)
	if err != nil || !changed {
		t.Fatalf("first discovery = (%v, %v), want (true, nil)", changed, err)
	}
	// Inside the TTL the roster is served from the store, so an unchanged lane
	// set must not produce a second network call at all.
	changed, err = service.RefreshZaiLiveModelsForAuth(context.Background(), auth)
	if err != nil || changed {
		t.Fatalf("second discovery = (%v, %v), want (false, nil) inside the TTL", changed, err)
	}
	if *hits != 1 {
		t.Fatalf("discovery requests = %d, want 1 inside the TTL", *hits)
	}

	// Past the TTL the provider is asked again, and an identical roster must
	// not report a change.
	zaiLiveModelsNowFunc = func() time.Time { return time.Now().Add(2 * time.Hour) }
	changed, err = service.RefreshZaiLiveModelsForAuth(context.Background(), auth)
	if err != nil || changed {
		t.Fatalf("post-TTL discovery = (%v, %v), want (false, nil) for an unchanged roster", changed, err)
	}
	if *hits != 2 {
		t.Fatalf("discovery requests = %d, want 2 after the TTL expired", *hits)
	}

	// A genuinely different roster must report a change.
	zaiLiveModelsNowFunc = func() time.Time { return time.Now().Add(4 * time.Hour) }
	changed2 := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"models":[{"slug":"glm-9","display_name":"glm-9","context_window":262144,"input_modalities":["text"],"output_modalities":["text"],"visibility":"list"}]}`))
	}))
	t.Cleanup(changed2.Close)
	auth.Attributes["base_url"] = zaiOriginForTest(changed2.URL)
	changed, err = service.RefreshZaiLiveModelsForAuth(context.Background(), auth)
	if err != nil || !changed {
		t.Fatalf("changed roster = (%v, %v), want (true, nil)", changed, err)
	}
	models := internalregistry.GetZaiModelsForCredential(auth.ID, "ak-1.2")
	if len(models) != 1 || models[0].ID != "glm-9" {
		t.Fatalf("credential lanes = %d entries, want the new roster", len(models))
	}
}

// TestZaiOriginFromBaseURL pins that every base URL shape a Z.AI credential
// records resolves to the discovery origin: the protocol base URLs share an
// origin with discovery, and a proxied endpoint stays on the proxy.
func TestZaiOriginFromBaseURL(t *testing.T) {
	for base, want := range map[string]string{
		"https://api.z.ai":                    "https://api.z.ai",
		"https://api.z.ai/":                   "https://api.z.ai",
		"https://api.z.ai/api/anthropic":      "https://api.z.ai",
		"https://api.z.ai/api/coding/paas/v4": "https://api.z.ai",
		"http://127.0.0.1:8080":               "http://127.0.0.1:8080",
		"http://127.0.0.1:8080/proxy/zai":     "http://127.0.0.1:8080",
	} {
		got, ok := zaiOriginFromBaseURL(base)
		if !ok || got != want {
			t.Fatalf("base %q resolved to %q/%v, want %q", base, got, ok, want)
		}
	}
	// A base URL that is not a URL is refused rather than guessed at, so the
	// documented origin is used instead of a malformed request target.
	for _, base := range []string{"", "not-a-url", "https://", "://api.z.ai"} {
		if got, ok := zaiOriginFromBaseURL(base); ok {
			t.Fatalf("base %q resolved to %q, want rejection", base, got)
		}
	}

	auth := zaiDiscoveryAuth("auth-zai-origin", "ak-1.2")
	auth.Attributes = map[string]string{"base_url": "https://api.z.ai/api/anthropic"}
	if got := zaiLiveModelsFetchURL(auth); got != "https://api.z.ai/api/v1/models" {
		t.Fatalf("fetch url = %q, want the discovery endpoint on the Z.AI origin", got)
	}
	// With no base URL recorded, the documented origin is used.
	if got := zaiLiveModelsFetchURL(zaiDiscoveryAuth("auth-zai-noorigin", "ak-1.2")); got != "https://api.z.ai/api/v1/models" {
		t.Fatalf("default fetch url = %q", got)
	}
}

// TestZaiLiveDiscoveryStatusRowShape pins that the management payload keeps its
// existing shape now that it reports two catalog sources, so the TUI card and
// the schema test keep working unchanged.
func TestZaiLiveDiscoveryStatusRowShape(t *testing.T) {
	status := internalregistry.GetModelsDevStatus()
	status.Providers = append(status.Providers, internalregistry.ZaiLiveModelsStatus().Providers...)
	if len(status.Providers) != 2 {
		t.Fatalf("providers = %d, want opencode-go and zai-coding-plan rows", len(status.Providers))
	}
	encoded, err := json.Marshal(status)
	if err != nil {
		t.Fatalf("marshal status: %v", err)
	}
	var decoded struct {
		Providers []struct {
			ID        string  `json:"id"`
			Source    string  `json:"source"`
			Models    int     `json:"models"`
			FetchedAt *string `json:"fetched_at"`
			LastError *string `json:"last_error"`
		} `json:"providers"`
	}
	if err := json.Unmarshal(encoded, &decoded); err != nil {
		t.Fatalf("unmarshal status: %v", err)
	}
	for _, provider := range decoded.Providers {
		if provider.Source != "fallback" && provider.Source != "live" {
			t.Fatalf("%s source = %q, want fallback or live", provider.ID, provider.Source)
		}
		if provider.Models <= 0 {
			t.Fatalf("%s reported %d models", provider.ID, provider.Models)
		}
	}
}
