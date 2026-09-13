package executor

import (
	"net/http"
	"testing"

	"github.com/router-for-me/CLIProxyAPI/v7/internal/config"
	cliproxyauth "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/auth"
)

func TestResolveMuseCloakModeDefaultsAuto(t *testing.T) {
	if got := resolveMuseCloakMode(&config.Config{}, &cliproxyauth.Auth{}); got != museCloakModeAuto {
		t.Fatalf("mode = %q, want auto", got)
	}
	if got := resolveMuseCloakMode(nil, nil); got != museCloakModeAuto {
		t.Fatalf("nil mode = %q, want auto", got)
	}
}

func TestResolveMuseCloakModePrefersCredential(t *testing.T) {
	auth := &cliproxyauth.Auth{
		Metadata: map[string]any{"cloak_mode": "always"},
	}
	if got := resolveMuseCloakMode(&config.Config{}, auth); got != museCloakModeAlways {
		t.Fatalf("mode = %q, want always", got)
	}
	auth.Attributes = map[string]string{"cloak_mode": "never"}
	if got := resolveMuseCloakMode(&config.Config{}, auth); got != museCloakModeNever {
		t.Fatalf("attribute mode = %q, want never", got)
	}
	// Unknown values fall back to auto rather than failing closed into never.
	auth.Attributes["cloak_mode"] = "bogus"
	delete(auth.Metadata, "cloak_mode")
	if got := resolveMuseCloakMode(&config.Config{}, auth); got != museCloakModeAuto {
		t.Fatalf("bogus mode = %q, want auto", got)
	}
}

func TestResolveMuseCloakModeGlobalDisableForcesNever(t *testing.T) {
	cfg := &config.Config{DisableMuseCloakMode: true}
	auth := &cliproxyauth.Auth{
		Attributes: map[string]string{"cloak_mode": "always"},
		Metadata:   map[string]any{"cloak_mode": "always"},
	}
	if got := resolveMuseCloakMode(cfg, auth); got != museCloakModeNever {
		t.Fatalf("disabled mode = %q, want never", got)
	}
}

func TestDetectMuseNativeRequest(t *testing.T) {
	native := http.Header{"User-Agent": []string{"muse-code/1.2.3"}}
	if !detectMuseNativeRequest(native) {
		t.Fatalf("muse UA should count as native")
	}
	for name, headers := range map[string]http.Header{
		"empty":       {},
		"claude code": {"User-Agent": []string{"claude-cli/2.1.258 (external, cli)"}},
		"go default":  {"User-Agent": []string{"Go-http-client/2.0"}},
		"codex":       {"User-Agent": []string{"codex_cli_rs/0.114.0"}},
	} {
		if detectMuseNativeRequest(headers) {
			t.Fatalf("%s should not count as native", name)
		}
	}
}

func TestMuseShouldCloakContract(t *testing.T) {
	if museShouldCloak("never", false) {
		t.Fatalf("never must not cloak")
	}
	if !museShouldCloak("always", true) {
		t.Fatalf("always must cloak even native clients")
	}
	if !museShouldCloak("auto", false) {
		t.Fatalf("auto must cloak foreign harnesses")
	}
	if museShouldCloak("auto", true) {
		t.Fatalf("auto must not cloak native Muse clients")
	}
	if !museShouldCloak("", false) {
		t.Fatalf("empty mode must behave as auto")
	}
}

func TestApplyMuseCloakHeaders(t *testing.T) {
	newRequest := func() *http.Request {
		req, _ := http.NewRequest(http.MethodPost, "https://api.meta.ai/v1/chat/completions", nil)
		return req
	}

	cloaked := newRequest()
	applyMuseCloakHeaders(cloaked, true)
	if got := cloaked.Header.Get("User-Agent"); got != museUserAgent {
		t.Fatalf("cloaked UA = %q, want %q", got, museUserAgent)
	}
	if got := cloaked.Header.Get("x-api-version"); got != "1.0.0" {
		t.Fatalf("cloaked x-api-version = %q, want 1.0.0", got)
	}

	passthrough := newRequest()
	applyMuseCloakHeaders(passthrough, false)
	if got := passthrough.Header.Get("User-Agent"); got != "" {
		t.Fatalf("passthrough UA = %q, want empty", got)
	}
	if got := passthrough.Header.Get("x-api-version"); got != "1.0.0" {
		t.Fatalf("passthrough x-api-version = %q, want 1.0.0", got)
	}

	// Cloaking repairs a version header dropped by custom expansion.
	stripped := newRequest()
	stripped.Header.Del("x-api-version")
	applyMuseCloakHeaders(stripped, true)
	if got := stripped.Header.Get("x-api-version"); got != "1.0.0" {
		t.Fatalf("repaired x-api-version = %q, want 1.0.0", got)
	}
}
