package main

import (
	"strings"
	"testing"
)

// Pins must survive regen: derivation must reproduce them even when models.dev
// omits the id or disagrees with npm. Regeneration previously derived routes
// from npm alone, which silently regressed gateway-verified lanes.
func TestOpencodeLanesPreserveGatewayPins(t *testing.T) {
	responsesOnly := opencodeLane{route: "responses", chatUnsupported: true}
	cases := []struct {
		id, npm string
		want    opencodeLane
	}{
		// Served only at /messages: the gateway refuses chat for it.
		{"minimax-m2.7", "@ai-sdk/anthropic", opencodeLane{route: "anthropic", chatUnsupported: true}},
		// Serves chat and /messages; the pin survives models.dev dropping its hint.
		{"minimax-m2.5", "", opencodeLane{route: "anthropic"}},
		// Unpinned anthropic hint: /messages for Claude callers, chat stays served.
		{"minimax-m3", "@ai-sdk/anthropic", opencodeLane{route: "anthropic"}},
		// Pinned Responses lane with no npm hint that still serves chat.
		{"deepseek-v4-flash", "", opencodeLane{route: "responses"}},
		// The muse-spark family, including revisions models.dev has not
		// published and billing variants.
		{"muse-spark-1.3-contributor", "@ai-sdk/openai", responsesOnly},
		{"muse-spark-1.3", "", responsesOnly},
		{"muse-spark-1.4-contributor", "", responsesOnly},
		{"muse-spark-9.9-preview-free", "", responsesOnly},
		// npm hints still apply to unpinned models.
		{"gpt-5.6-luna", "@ai-sdk/openai", responsesOnly},
		{"qwen3.8-flash", "@ai-sdk/anthropic", opencodeLane{route: "anthropic"}},
		{"glm-5.2", "", opencodeChatLane},
	}
	for _, c := range cases {
		if got := opencodeLaneFor(c.id, c.npm); got != c.want {
			t.Errorf("opencodeLaneFor(%q, %q) = %+v, want %+v", c.id, c.npm, got, c.want)
		}
	}
}

// Pins apply even when models.dev omits the id entirely.
func TestOpencodeRoutesFromCatalogAppliesPinsForAbsentIDs(t *testing.T) {
	lanes := opencodeRoutesFromCatalog([]byte(`{"opencode-go":{"models":{"glm-5.3":{"id":"glm-5.3"}}}}`))
	for id, want := range opencodePinnedLanes() {
		if got := lanes[id]; got != want {
			t.Errorf("lanes[%q] = %+v, want pinned %+v", id, got, want)
		}
	}
	if _, ok := lanes["glm-5.3"]; ok {
		t.Error("plain chat lane must stay out of the generated maps")
	}
}

// The rendered block must carry the chat-unsupported map and the prefix list,
// or regeneration would delete the rules that keep chat callers off refused
// lanes.
func TestRenderOpencodeRoutesEmitsChatUnsupportedAndPrefixes(t *testing.T) {
	lanes := map[string]opencodeLane{
		"minimax-m2.7":      {route: "anthropic", chatUnsupported: true},
		"deepseek-v4-flash": {route: "responses"},
		"gpt-6-luna":        {route: "responses", chatUnsupported: true},
		"glm-5.3":           opencodeChatLane,
	}
	out := renderOpencodeRoutes(lanes)
	section := func(name string) string {
		start := strings.Index(out, "var "+name)
		if start < 0 {
			t.Fatalf("rendered routes missing %s:\n%s", name, out)
		}
		end := strings.Index(out[start:], "}")
		return out[start : start+end]
	}
	if s := section("opencodeChatUnsupportedModels"); !strings.Contains(s, `"minimax-m2.7"`) || !strings.Contains(s, `"gpt-6-luna"`) || strings.Contains(s, `"deepseek-v4-flash"`) {
		t.Errorf("chat-unsupported map wrong:\n%s", s)
	}
	if s := section("opencodeAnthropicRouteModels"); !strings.Contains(s, `"minimax-m2.7"`) {
		t.Errorf("anthropic map wrong:\n%s", s)
	}
	if s := section("opencodeResponsesOnlyPrefixes"); !strings.Contains(s, `"muse-spark-"`) {
		t.Errorf("prefix list wrong:\n%s", s)
	}
	if strings.Contains(out, `"glm-5.3"`) {
		t.Error("plain chat lane must stay out of the generated maps (chat is the default)")
	}
	if out != renderOpencodeRoutes(lanes) {
		t.Error("render is not deterministic")
	}
}
