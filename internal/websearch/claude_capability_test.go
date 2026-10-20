package websearch

import (
	"testing"

	"github.com/router-for-me/CLIProxyAPI/v8/internal/registry"
)

func TestRequestWantsClaudeWebSearchCapability(t *testing.T) {
	if !RequestWantsClaudeWebSearchCapability(PaseoClaudeUserAgent, "", "") {
		t.Fatal("paseo user agent must opt in")
	}
	if !RequestWantsClaudeWebSearchCapability("claude-cli/paseo/1.2", "", "") {
		t.Fatal("versioned paseo user agent must opt in")
	}
	if RequestWantsClaudeWebSearchCapability("claude-cli/2.1.296", "", "") {
		t.Fatal("official claude-cli user agent must not opt in")
	}
	if RequestWantsClaudeWebSearchCapability("claude-cli/paseoextra", "", "") {
		t.Fatal("user agent must match the paseo token, not a longer product")
	}
	if !RequestWantsClaudeWebSearchCapability("claude-cli/2.1.296", "Paseo", "") {
		t.Fatal("X-CPA-Client: paseo must opt in")
	}
	if !RequestWantsClaudeWebSearchCapability("", "", "1") {
		t.Fatal("cpa_capabilities=1 must opt in")
	}
	if !RequestWantsClaudeWebSearchCapability("", "", "true") {
		t.Fatal("cpa_capabilities=true must opt in")
	}
	if RequestWantsClaudeWebSearchCapability("", "", "0") {
		t.Fatal("cpa_capabilities=0 must not opt in")
	}
}

func TestClaudeWebSearchServable(t *testing.T) {
	if got := ClaudeWebSearchServable(Config{}, nil, "grok-4.6"); got != nil {
		t.Fatalf("no routes = %v, want nil", *got)
	}
	if got := ClaudeWebSearchServable(Config{}, []string{"", "  "}, "grok-4.6"); got != nil {
		t.Fatalf("blank routes = %v, want nil", *got)
	}

	assertServable := func(t *testing.T, cfg Config, providers []string, model string, want bool) {
		t.Helper()
		got := ClaudeWebSearchServable(cfg, providers, model)
		if got == nil {
			t.Fatalf("providers %v model %s = nil, want %v", providers, model, want)
		}
		if *got != want {
			t.Fatalf("providers %v model %s = %v, want %v", providers, model, *got, want)
		}
	}

	// Native routes execute the server tool without the fallback chain.
	assertServable(t, Config{}, []string{"claude"}, "claude-sonnet-4-6", true)
	assertServable(t, Config{}, []string{"codex"}, "gpt-5.4", true)
	assertServable(t, Config{}, []string{"xai"}, "grok-4.6", true)

	// A non-native route drops the tool unless fallback is configured.
	assertServable(t, Config{}, []string{"openai"}, "qwen3", false)
	assertServable(t, Config{}, []string{"openai-compatibility"}, "qwen3", false)
	assertServable(t, Config{Enabled: true}, []string{"kimi"}, "kimi-k2", true)

	// A native sibling suppresses fallback for the whole list, so a mixed
	// model is not servable even when fallback could cover the other route.
	assertServable(t, Config{Enabled: true}, []string{"codex", "openai"}, "gpt-5.4", false)
	assertServable(t, Config{Enabled: true}, []string{"codex", "xai"}, "shared", true)
}

func TestClaudeWebSearchServableGeminiFollowsModelCapability(t *testing.T) {
	const clientID = "test-claude-web-search-capability"
	registry.GetGlobalRegistry().RegisterClient(clientID, "gemini", []*registry.ModelInfo{
		{ID: "gemini-search-capable", SupportsWebSearch: true},
		{ID: "gemini-search-plain"},
	})
	t.Cleanup(func() { registry.GetGlobalRegistry().UnregisterClient(clientID) })

	capable := ClaudeWebSearchServable(Config{}, []string{"gemini"}, "gemini-search-capable")
	if capable == nil || !*capable {
		t.Fatal("capable gemini route must be servable without fallback")
	}
	plain := ClaudeWebSearchServable(Config{}, []string{"gemini"}, "gemini-search-plain")
	if plain == nil || *plain {
		t.Fatal("incapable gemini route must not be servable while fallback is off")
	}
	withFallback := ClaudeWebSearchServable(Config{Enabled: true}, []string{"gemini"}, "gemini-search-plain")
	if withFallback == nil || !*withFallback {
		t.Fatal("incapable gemini route must be servable once fallback is enabled")
	}
}

func TestAnnotateClaudeWebSearchOmitsUnknownAndKeepsOtherFields(t *testing.T) {
	models := []map[string]any{
		{"id": "grok-4.6", "display_name": "Grok"},
		{"id": "orphan", "display_name": "Orphan"},
	}
	AnnotateClaudeWebSearch(models, Config{}, func(id string) []string {
		if id == "grok-4.6" {
			return []string{"xai"}
		}
		return nil
	})
	caps, _ := models[0]["cpa_capabilities"].(map[string]any)
	if caps == nil || caps[ClaudeWebSearchCapabilityKey] != true {
		t.Fatalf("grok capabilities = %#v, want claude_web_search true", models[0]["cpa_capabilities"])
	}
	if _, ok := models[1]["cpa_capabilities"]; ok {
		t.Fatalf("orphan model leaked capabilities: %#v", models[1])
	}
	if models[0]["display_name"] != "Grok" {
		t.Fatalf("annotation rewrote display_name: %#v", models[0])
	}
}
