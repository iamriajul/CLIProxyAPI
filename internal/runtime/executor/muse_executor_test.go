package executor

import (
	"net/http"
	"testing"

	museauth "github.com/router-for-me/CLIProxyAPI/v7/internal/auth/muse"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/config"
	cliproxyauth "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/auth"
)

func TestMuseExecutorIdentifier(t *testing.T) {
	if got := NewMuseExecutor(&config.Config{}).Identifier(); got != "muse" {
		t.Fatalf("Identifier = %q, want muse", got)
	}
}

func TestMusePrepareRequestAddsVersionAndKey(t *testing.T) {
	executor := NewMuseExecutor(&config.Config{})
	auth := &cliproxyauth.Auth{
		Provider: "muse",
		Metadata: map[string]any{"muse_api_key": "LLM|test-key"},
	}
	req, _ := http.NewRequest(http.MethodPost, "https://api.meta.ai/v1/chat/completions", nil)
	if err := executor.PrepareRequest(req, auth); err != nil {
		t.Fatalf("PrepareRequest err = %v", err)
	}
	if got := req.Header.Get("Authorization"); got != "Bearer LLM|test-key" {
		t.Fatalf("Authorization = %q", got)
	}
	if got := req.Header.Get("x-api-version"); got != museauth.MuseAPIVersion {
		t.Fatalf("x-api-version = %q, want %q", got, museauth.MuseAPIVersion)
	}
}

func TestMusePrepareRequestCombinedCredential(t *testing.T) {
	executor := NewMuseExecutor(&config.Config{})
	combined := museauth.EncodeCombinedCredential("oauth-token", "LLM|combined-key")
	auth := &cliproxyauth.Auth{
		Provider: "muse",
		Metadata: map[string]any{"access_token": combined},
	}
	req, _ := http.NewRequest(http.MethodPost, "https://api.meta.ai/v1/chat/completions", nil)
	if err := executor.PrepareRequest(req, auth); err != nil {
		t.Fatalf("PrepareRequest err = %v", err)
	}
	if got := req.Header.Get("Authorization"); got != "Bearer LLM|combined-key" {
		t.Fatalf("Authorization = %q, want minted key", got)
	}
}

func TestNormalizeMuseToolsConvertsCustom(t *testing.T) {
	body := []byte(`{"model":"muse-spark-1.3","tools":[{"type":"custom","name":"apply_patch","description":"edit","parameters":{"properties":{"x":{"type":"string"}}}}]}`)
	out := normalizeMuseTools(body)
	if got := getToolType(out); got != "function" {
		t.Fatalf("tool type = %q, want function", got)
	}
}

func getToolType(body []byte) string {
	// Minimal extraction without extra deps in test.
	for i := 0; i+8 < len(body); i++ {
		if string(body[i:i+8]) == `"type":"` {
			rest := string(body[i+8:])
			end := 0
			for end < len(rest) && rest[end] != '"' {
				end++
			}
			return rest[:end]
		}
	}
	return ""
}

func TestMuseRefreshReusesMintedKey(t *testing.T) {
	executor := NewMuseExecutor(&config.Config{})
	auth := &cliproxyauth.Auth{
		Provider: "muse",
		Metadata: map[string]any{"muse_api_key": "LLM|existing", "access_token": "oauth"},
	}
	refreshed, err := executor.Refresh(t.Context(), auth)
	if err != nil {
		t.Fatalf("Refresh err = %v", err)
	}
	if refreshed != auth {
		t.Fatalf("refresh should return same auth when key present")
	}
}
