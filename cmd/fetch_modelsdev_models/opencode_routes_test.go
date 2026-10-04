package main

import (
	"strings"
	"testing"
)

// Pins must survive regen: derivation must reproduce them even when models.dev
// omits the id or disagrees with npm. Regeneration previously derived routes
// from npm alone, which silently regressed gateway-verified pins (#887, #1617).
func TestOpencodeRoutesPreserveGatewayPins(t *testing.T) {
	cases := []struct{ id, npm, want string }{
		// npm would say anthropic; the gateway serves chat only (#1617).
		{"minimax-m2.7", "@ai-sdk/anthropic", "chat"},
		{"minimax-m3", "@ai-sdk/anthropic", "chat"},
		// Pinned responses lane with no npm hint at all.
		{"deepseek-v4-flash", "", "responses"},
		// Pinned anthropic lane.
		{"union-alpha", "", "anthropic"},
		// The muse-spark family, including revisions models.dev has not
		// published and billing variants.
		{"muse-spark-1.3-contributor", "@ai-sdk/openai", "responses"},
		{"muse-spark-1.3", "", "responses"},
		{"muse-spark-1.2", "", "responses"},
		{"muse-spark-1.4-contributor", "", "responses"},
		{"muse-spark-9.9-preview-free", "", "responses"},
		// npm hints still apply to unpinned models.
		{"gpt-5.6-luna", "@ai-sdk/openai", "responses"},
		{"qwen3.8-flash", "@ai-sdk/anthropic", "anthropic"},
		{"glm-5.2", "", "chat"},
	}
	for _, c := range cases {
		if got := opencodeRouteFor(c.id, c.npm); got != c.want {
			t.Errorf("opencodeRouteFor(%q, %q) = %q, want %q", c.id, c.npm, got, c.want)
		}
	}
}

// The rendered block must carry the prefix list, or regeneration would delete
// the muse-spark family rule.
func TestRenderOpencodeRoutesEmitsPrefixes(t *testing.T) {
	out := renderOpencodeRoutes(map[string]string{
		"union-alpha":       "anthropic",
		"deepseek-v4-flash": "responses",
		"muse-spark-1.3":    "responses",
		"minimax-m2.7":      "chat",
		"muse-spark-1.4":    "responses",
	})
	for _, want := range []string{
		"var opencodeResponsesRoutePrefixes = []string{",
		`"muse-spark-",`,
		`"union-alpha": true,`,
		`"deepseek-v4-flash": true,`,
	} {
		if !strings.Contains(out, want) {
			t.Errorf("rendered routes missing %q:\n%s", want, out)
		}
	}
	if strings.Contains(out, `"minimax-m2.7"`) {
		t.Error("chat-route model must stay out of the generated maps (chat is the default)")
	}
	if out != renderOpencodeRoutes(map[string]string{
		"union-alpha":       "anthropic",
		"deepseek-v4-flash": "responses",
		"muse-spark-1.3":    "responses",
		"minimax-m2.7":      "chat",
		"muse-spark-1.4":    "responses",
	}) {
		t.Error("render is not deterministic")
	}
}
