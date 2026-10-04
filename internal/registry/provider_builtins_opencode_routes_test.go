package registry

import "testing"

// TestOpencodeRoutesMatchLiveGateway pins the route data to what the OpenCode Go
// gateway actually served on 2026-10-04: every live model was sent a minimal
// request at /chat/completions, /responses and /messages, and a refused wire
// answered 400 ModelProtocolUnsupported. The routing must never send a caller
// to a wire the gateway refuses: chat only where chat is served, and a lane's
// route only where that wire is served. Re-probe and update this table when the
// gateway's roster changes.
func TestOpencodeRoutesMatchLiveGateway(t *testing.T) {
	cases := []struct {
		model                     string
		chat, responses, messages bool
	}{
		{"deepseek-flash", true, true, true},
		{"deepseek-v4-flash", true, true, true},
		{"deepseek-v4-flash-vision-exp", true, true, true},
		{"deepseek-v4-pro", true, true, true},
		{"deepseek-v4.1-flash", true, true, true},
		{"glm-5.1", true, false, false},
		{"glm-5.2", true, false, false},
		{"glm-5.3", true, false, false},
		{"glm-5.3-flash", true, false, false},
		{"gpt-5.6-luna", false, true, false},
		{"gpt-6-luna", false, true, false},
		{"grok-4.6", false, true, false},
		{"grok-4.7", false, true, false},
		{"hy3", true, false, false},
		{"hy4-preview", true, false, false},
		{"kimi-k2.6", true, false, false},
		{"kimi-k2.7-code", true, false, false},
		{"kimi-k3", true, false, true},
		{"longcat-2.0", true, false, false},
		{"longcat-2.5-preview-free", true, false, false},
		{"mimo-v2.5", true, false, false},
		{"mimo-v2.5-pro", true, false, false},
		{"mimo-v2.6-flash", true, false, false},
		{"mimo-v2.6-pro", true, false, false},
		{"minimax-m2.5", true, false, true},
		{"minimax-m2.7", false, false, true},
		{"minimax-m3", true, false, true},
		{"muse-spark-1.2-contributor", false, true, false},
		{"muse-spark-1.3-contributor", false, true, false},
		{"omen-alpha", true, false, false},
		{"qwen3.6-plus", true, false, true},
		{"qwen3.7-max", true, false, true},
		{"qwen3.7-plus", true, false, true},
		{"qwen3.8-flash", true, false, true},
		{"qwen3.8-max", true, false, true},
		{"space-bunny-free", true, false, true},
	}
	for _, c := range cases {
		if got := OpencodeServesChat(c.model); got != c.chat {
			t.Errorf("OpencodeServesChat(%q) = %v, gateway serves chat: %v", c.model, got, c.chat)
		}
		switch route := OpencodeUpstreamRoute(c.model); route {
		case "responses":
			if !c.responses {
				t.Errorf("%s routes to /responses, which the gateway refuses", c.model)
			}
		case "anthropic":
			if !c.messages {
				t.Errorf("%s routes to /messages, which the gateway refuses", c.model)
			}
		case "chat":
			if !c.chat {
				t.Errorf("%s has no route but the gateway refuses chat for it", c.model)
			}
		default:
			t.Errorf("%s: unknown route %q", c.model, route)
		}
	}
}

func TestOpencodeRouteHelpersStripThinkingSuffix(t *testing.T) {
	if got := OpencodeUpstreamRoute("MiniMax-M2.7(high)"); got != "anthropic" {
		t.Errorf("OpencodeUpstreamRoute(suffixed) = %q, want anthropic", got)
	}
	if OpencodeServesChat("muse-spark-1.4-contributor(low)") {
		t.Error("muse-spark family must stay off chat, including unpublished revisions")
	}
	if !OpencodeServesChat("glm-5.3(high)") {
		t.Error("plain chat lane must serve chat")
	}
}
