package api

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	claudemodels "github.com/router-for-me/CLIProxyAPI/v8/internal/client/claude/models"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/config"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/registry"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/websearch"
	sdkconfig "github.com/router-for-me/CLIProxyAPI/v8/sdk/config"
)

func TestClaudeModelsAdvertisesWebSearchOnlyToPaseo(t *testing.T) {
	const clientID = "test-claude-web-search-catalog"
	registry.GetGlobalRegistry().RegisterClient(clientID, "xai", []*registry.ModelInfo{
		{ID: "grok-4.6", DisplayName: "Grok 4.6", OwnedBy: "xai"},
	})
	registry.GetGlobalRegistry().RegisterClient(clientID+"-compat", "openai-compatibility", []*registry.ModelInfo{
		{ID: "qwen3", DisplayName: "Qwen 3", OwnedBy: "qwen"},
	})
	t.Cleanup(func() {
		registry.GetGlobalRegistry().UnregisterClient(clientID)
		registry.GetGlobalRegistry().UnregisterClient(clientID + "-compat")
	})

	disabled := newTestServer(t)
	enabled := newTestServerWithConfig(t, &config.Config{
		SDKConfig: sdkconfig.SDKConfig{
			APIKeys:   []string{"test-key"},
			WebSearch: websearch.Config{Enabled: true},
		},
	})

	grokID := claudemodels.EnsureClaudeModelIDPrefix("grok-4.6")
	qwenID := claudemodels.EnsureClaudeModelIDPrefix("qwen3")
	disabledBody := getClaudeModels(t, disabled, websearch.PaseoClaudeUserAgent, "")
	if got := claudeWebSearchBit(t, disabledBody, grokID); got != true {
		t.Fatalf("native grok with fallback off = %v, want true", got)
	}
	if got := claudeWebSearchBit(t, disabledBody, qwenID); got != false {
		t.Fatalf("compat qwen with fallback off = %v, want false", got)
	}

	official := getClaudeModels(t, disabled, "claude-cli/2.1.296", "")
	for _, model := range official {
		if _, ok := model["cpa_capabilities"]; ok {
			t.Fatalf("official client received cpa_capabilities on %v", model["id"])
		}
	}

	optIn := getClaudeModels(t, enabled, "claude-cli/2.1.296", "1")
	if got := claudeWebSearchBit(t, optIn, qwenID); got != true {
		t.Fatalf("compat qwen with fallback on = %v, want true", got)
	}
}

func getClaudeModels(t *testing.T, server *Server, userAgent, capabilities string) []map[string]any {
	t.Helper()
	req := httptest.NewRequest(http.MethodGet, "/v1/models", nil)
	if capabilities != "" {
		req.URL.RawQuery = websearch.ClaudeWebSearchCapabilityQuery + "=" + capabilities
	}
	req.Header.Set("Authorization", "Bearer test-key")
	req.Header.Set("Anthropic-Version", "2023-06-01")
	req.Header.Set("User-Agent", userAgent)
	rr := httptest.NewRecorder()
	server.engine.ServeHTTP(rr, req)
	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d, body=%s", rr.Code, rr.Body.String())
	}
	var resp struct {
		Data []map[string]any `json:"data"`
	}
	if err := json.Unmarshal(rr.Body.Bytes(), &resp); err != nil {
		t.Fatalf("decode models: %v body=%s", err, rr.Body.String())
	}
	return resp.Data
}

func claudeWebSearchBit(t *testing.T, models []map[string]any, id string) bool {
	t.Helper()
	for _, model := range models {
		gotID, _ := model["id"].(string)
		if gotID != id {
			continue
		}
		caps, _ := model["cpa_capabilities"].(map[string]any)
		bit, ok := caps[websearch.ClaudeWebSearchCapabilityKey].(bool)
		if !ok {
			t.Fatalf("model %s missing %s: %#v", id, websearch.ClaudeWebSearchCapabilityKey, model["cpa_capabilities"])
		}
		return bit
	}
	t.Fatalf("model %s not in catalog", id)
	return false
}
