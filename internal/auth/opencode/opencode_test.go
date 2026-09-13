package opencode

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestNormalizeBaseURL(t *testing.T) {
	if got := NormalizeBaseURL(""); got != OpenCodeGoAPIBaseURL {
		t.Fatalf("empty = %q", got)
	}
	if got := NormalizeBaseURL("https://opencode.ai/zen/go"); got != OpenCodeGoAPIBaseURL {
		t.Fatalf("bare = %q", got)
	}
	if got := NormalizeBaseURL("https://opencode.ai/zen/go/v1/"); got != OpenCodeGoAPIBaseURL {
		t.Fatalf("trailing slash = %q", got)
	}
	if got := UsageURL(""); got != OpenCodeGoAPIBaseURL+"/usage" {
		t.Fatalf("usage = %q", got)
	}
}

func TestValidateKey(t *testing.T) {
	var gotAuth, gotUA string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotAuth = r.Header.Get("Authorization")
		gotUA = r.Header.Get("User-Agent")
		if !strings.HasSuffix(r.URL.Path, "/models") {
			t.Errorf("path = %q, want */models", r.URL.Path)
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{"data": []any{map[string]any{"id": "glm-5.2"}}})
	}))
	defer server.Close()

	// Point validation at the test server by overriding through base URL shape:
	// NormalizeBaseURL appends /v1, so serve stripping it is unnecessary —
	// instead exercise ValidateKey against a server whose path already ends in
	// /v1/models by passing the server URL with a /v1 suffix.
	auth := NewOpenCodeAuth(nil)
	if err := auth.ValidateKey(context.Background(), server.URL+"/v1", "sk-test"); err != nil {
		t.Fatalf("ValidateKey err = %v", err)
	}
	if gotAuth != "Bearer sk-test" {
		t.Fatalf("Authorization = %q", gotAuth)
	}
	if gotUA == "" {
		t.Fatalf("User-Agent should be set")
	}

	denied := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusUnauthorized)
	}))
	defer denied.Close()
	if err := NewOpenCodeAuth(nil).ValidateKey(context.Background(), denied.URL+"/v1", "bad"); err == nil {
		t.Fatalf("expected rejection for 401")
	}
	if err := NewOpenCodeAuth(nil).ValidateKey(context.Background(), "", ""); err == nil {
		t.Fatalf("expected error for empty key")
	}
}

func TestOpenCodeCreds(t *testing.T) {
	if got := OpenCodeCreds(map[string]any{"api_key": "sk-x"}, nil); got != "sk-x" {
		t.Fatalf("creds = %q", got)
	}
	if got := OpenCodeCreds(nil, map[string]string{"api_key": "sk-y"}); got != "sk-y" {
		t.Fatalf("attr creds = %q", got)
	}
	if got := OpenCodeCreds(nil, nil); got != "" {
		t.Fatalf("empty creds = %q", got)
	}
}
