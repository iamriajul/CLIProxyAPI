package quota

import (
	"testing"

	coreauth "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/auth"
)

func TestCanonicalProvider(t *testing.T) {
	cases := map[string]string{
		"codex": "codex", " CODEX ": "codex",
		"claude": "claude", "antigravity": "antigravity", "devin": "devin",
		"kimi": "kimi", "kimi-ai": "kimi", "kimi.ai": "kimi",
		"meta": "meta", "muse": "meta",
		"opencode": "opencode", "opencode-go": "opencode",
		"zai": "zai", "glm": "zai", "xai": "xai", "grok": "xai",
		"gemini": "", "vertex": "", "openai-compatibility": "", "": "",
	}
	for input, want := range cases {
		if got := CanonicalProvider(input); got != want {
			t.Errorf("CanonicalProvider(%q) = %q, want %q", input, got, want)
		}
	}
}

func TestParseRefreshMode(t *testing.T) {
	for raw, want := range map[string]RefreshMode{
		"": RefreshAuto, "auto": RefreshAuto, "bogus": RefreshAuto,
		"live": RefreshLive, "force": RefreshLive, "true": RefreshLive, "1": RefreshLive,
		"cached": RefreshCached, "false": RefreshCached, "0": RefreshCached,
	} {
		if got := ParseRefreshMode(raw); got != want {
			t.Errorf("ParseRefreshMode(%q) = %v, want %v", raw, got, want)
		}
	}
}

func TestBearerTokenPrecedence(t *testing.T) {
	auth := &coreauth.Auth{
		Metadata:   map[string]any{"access_token": "meta-access", "api_key": "meta-key"},
		Attributes: map[string]string{"access_token": "attr-access", "api_key": "attr-key"},
	}
	if got := BearerToken(auth); got != "meta-access" {
		t.Fatalf("BearerToken = %q", got)
	}
	delete(auth.Metadata, "access_token")
	if got := BearerToken(auth); got != "attr-access" {
		t.Fatalf("BearerToken = %q", got)
	}
	delete(auth.Attributes, "access_token")
	if got := BearerToken(auth); got != "attr-key" {
		t.Fatalf("BearerToken = %q", got)
	}
	if got := BearerToken(nil); got != "" {
		t.Fatalf("nil BearerToken = %q", got)
	}
}

func TestRoundPercent(t *testing.T) {
	if got := RoundPercent(51); got != 51 {
		t.Fatalf("RoundPercent(51) = %v", got)
	}
	if got := RoundPercent(0.53 * 100); got != 53 {
		t.Fatalf("RoundPercent(0.53*100) = %v", got)
	}
}

func TestSlugify(t *testing.T) {
	cases := map[string]string{
		"Gemini Models":         "gemini-models",
		"Claude and GPT Models": "claude-and-gpt-models",
		"  spaces  ":            "spaces",
		"a__b!!c":               "a-b-c",
		"":                      "",
	}
	for input, want := range cases {
		if got := Slugify(input); got != want {
			t.Errorf("Slugify(%q) = %q, want %q", input, got, want)
		}
	}
}
