package handlers

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/router-for-me/CLIProxyAPI/v8/internal/websearch"
	coreauth "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/auth"
)

func borrowedManager(entries ...*coreauth.Auth) *coreauth.Manager {
	manager := coreauth.NewManager(nil, nil, nil)
	for _, auth := range entries {
		if _, errRegister := manager.Register(context.Background(), auth); errRegister != nil {
			panic(errRegister)
		}
	}
	return manager
}
func TestBorrowedSearchCredentialsSkipUnavailableSessions(t *testing.T) {
	manager := borrowedManager(
		&coreauth.Auth{ID: "claude-1", Provider: "claude", Disabled: true, Metadata: map[string]any{"access_token": "disabled-tok"}},
		&coreauth.Auth{ID: "gemini-1", Provider: "gemini", Metadata: map[string]any{"access_token": "gem-tok"}},
		&coreauth.Auth{ID: "xai-1", Provider: "xai", Unavailable: true, Metadata: map[string]any{"access_token": "cooled-tok"}},
	)
	borrowed := borrowedSearchCredentials(manager, []string{"anthropic", "gemini", "xai"})
	if len(borrowed.auths) != 1 {
		t.Fatalf("borrowed = %d sessions, want only the usable one", len(borrowed.auths))
	}
	if borrowed.auths[0].ID != "gemini-1" {
		t.Fatalf("borrowed the wrong session: %+v", borrowed.auths[0])
	}
}

func TestApplyBorrowedCredentialsNeverOverridesConfiguredKeys(t *testing.T) {
	manager := borrowedManager(&coreauth.Auth{ID: "claude-1", Provider: "claude", Metadata: map[string]any{"access_token": "borrowed"}})
	cfg := websearch.Config{AnthropicAPIKey: "explicit"}
	// The explicit key stands: borrowing exists to fill a gap, not to
	// override an operator's deliberate choice.
	if applyBorrowedSearchCredentials(&cfg, manager, []string{"anthropic"}) {
		t.Fatal("no change should be reported when the key is already configured")
	}
	if cfg.AnthropicAPIKey != "explicit" {
		t.Fatalf("configured key was overwritten: %q", cfg.AnthropicAPIKey)
	}
}

func TestApplyBorrowedCredentialsFillsEmptyKey(t *testing.T) {
	manager := borrowedManager(
		&coreauth.Auth{ID: "claude-1", Provider: "claude", Metadata: map[string]any{"access_token": " borrowed "}},
		&coreauth.Auth{ID: "kimi-1", Provider: "kimi", Metadata: map[string]any{"api_key": "kimi-tok"}},
	)
	cfg := websearch.Config{}
	if !applyBorrowedSearchCredentials(&cfg, manager, []string{"anthropic", "kimi", "gemini"}) {
		t.Fatal("expected a change to be reported")
	}
	if cfg.AnthropicAPIKey != "borrowed" {
		t.Fatalf("anthropic key = %q, want the trimmed session token", cfg.AnthropicAPIKey)
	}
	// A provider name the map does not cover must not be invented for.
	if cfg.GeminiAPIKey != "" {
		t.Fatalf("gemini key = %q, want it left empty without a session", cfg.GeminiAPIKey)
	}
}

// Claude and Codex register their token into TokenStorage and leave
// Metadata holding only identity fields, so borrowing must read the
// backing auth file. Reading Metadata alone would find no credential and
// silently leave search ungrounded for exactly those providers.
func TestBorrowedCredentialsReadStoredAuthFile(t *testing.T) {
	dir := t.TempDir()
	credential := filepath.Join(dir, "claude-user.json")
	if errWrite := os.WriteFile(credential, []byte(`{"type":"claude","email":"u@example.com","access_token":"sk-file-token"}`), 0o600); errWrite != nil {
		t.Fatalf("write credential: %v", errWrite)
	}
	manager := borrowedManager(&coreauth.Auth{
		ID:       "claude-file",
		Provider: "claude",
		FileName: credential,
		// Metadata deliberately mirrors the real shape: identity only.
		Metadata: map[string]any{"email": "u@example.com"},
	})
	cfg := websearch.Config{}
	if !applyBorrowedSearchCredentials(&cfg, manager, []string{"anthropic"}) {
		t.Fatal("expected the stored credential to be borrowed")
	}
	if cfg.AnthropicAPIKey != "sk-file-token" {
		t.Fatalf("anthropic key = %q, want the token from the auth file", cfg.AnthropicAPIKey)
	}
}

// A credential that cannot be read must not be used: borrowing is a
// best-effort fill, not a reason to send a blank or malformed token.
func TestBorrowedCredentialsRejectUnreadableFiles(t *testing.T) {
	dir := t.TempDir()
	malformed := filepath.Join(dir, "bad.json")
	if errWrite := os.WriteFile(malformed, []byte(`not json`), 0o600); errWrite != nil {
		t.Fatalf("write credential: %v", errWrite)
	}
	manager := borrowedManager(&coreauth.Auth{ID: "claude-bad", Provider: "claude", FileName: malformed})
	cfg := websearch.Config{}
	if applyBorrowedSearchCredentials(&cfg, manager, []string{"anthropic"}) {
		t.Fatal("an unreadable credential must not be borrowed")
	}
	if cfg.AnthropicAPIKey != "" {
		t.Fatalf("anthropic key = %q, want it left empty", cfg.AnthropicAPIKey)
	}
}

func TestBorrowedCredentialsTolerateMissingManager(t *testing.T) {
	if got := borrowedSearchCredentials(nil, []string{"anthropic"}); len(got.auths) != 0 {
		t.Fatalf("borrowed = %+v, want nothing without a manager", got.auths)
	}
	cfg := websearch.Config{}
	if applyBorrowedSearchCredentials(&cfg, nil, []string{"anthropic"}) {
		t.Fatal("no change should be reported without a manager")
	}
}

// The request handlers pass route provider names, not search IDs. Matching
// on "anthropic" made the map unreachable in production: a Claude login
// would never be borrowed, which is the whole point of the feature.
func TestBorrowedCredentialsMatchRouteNames(t *testing.T) {
	dir := t.TempDir()
	credential := filepath.Join(dir, "claude-user.json")
	if errWrite := os.WriteFile(credential, []byte(`{"type":"claude","access_token":"sk-route"}`), 0o600); errWrite != nil {
		t.Fatalf("write credential: %v", errWrite)
	}
	manager := borrowedManager(&coreauth.Auth{
		ID:       "claude-1",
		Provider: "claude",
		FileName: credential,
		Metadata: map[string]any{"email": "u@example.com"},
	})
	for _, route := range []string{"claude", "claude-code", "anthropic"} {
		cfg := websearch.Config{}
		if !applyBorrowedSearchCredentials(&cfg, manager, []string{route}) {
			t.Fatalf("route %q borrowed nothing", route)
		}
		if cfg.AnthropicAPIKey != "sk-route" {
			t.Fatalf("route %q: anthropic key = %q, want the Claude session token", route, cfg.AnthropicAPIKey)
		}
	}
	// An unrelated route must not borrow a Claude session.
	cfg := websearch.Config{}
	if applyBorrowedSearchCredentials(&cfg, manager, []string{"openai-compatible-kimi"}) {
		t.Fatal("an unrelated route must not borrow a Claude credential")
	}
}

// Antigravity sessions ground through Gemini, so they fill the Gemini key.
func TestBorrowedCredentialsRouteAntigravityToGemini(t *testing.T) {
	dir := t.TempDir()
	credential := filepath.Join(dir, "ag.json")
	if errWrite := os.WriteFile(credential, []byte(`{"type":"antigravity","access_token":"ag-token"}`), 0o600); errWrite != nil {
		t.Fatalf("write credential: %v", errWrite)
	}
	manager := borrowedManager(&coreauth.Auth{ID: "ag-1", Provider: "antigravity", FileName: credential})
	cfg := websearch.Config{}
	if !applyBorrowedSearchCredentials(&cfg, manager, []string{"antigravity"}) {
		t.Fatal("expected the antigravity session to be borrowed")
	}
	if cfg.GeminiAPIKey != "ag-token" {
		t.Fatalf("gemini key = %q, want the antigravity session token", cfg.GeminiAPIKey)
	}
}

// A namespaced route must resolve to its own provider, not the generic
// "openai" entry, and must keep scanning when an earlier prefix match has
// no live session.
func TestBorrowedCredentialsPreferNamespacedRouteOverGeneric(t *testing.T) {
	dir := t.TempDir()
	kimiFile := filepath.Join(dir, "kimi.json")
	if errWrite := os.WriteFile(kimiFile, []byte(`{"type":"kimi","access_token":"kimi-tok"}`), 0o600); errWrite != nil {
		t.Fatalf("write credential: %v", errWrite)
	}
	codexFile := filepath.Join(dir, "codex.json")
	if errWrite := os.WriteFile(codexFile, []byte(`{"type":"codex","access_token":"codex-tok"}`), 0o600); errWrite != nil {
		t.Fatalf("write credential: %v", errWrite)
	}

	// With both sessions present, the namespaced Kimi route borrows Kimi.
	both := borrowedManager(
		&coreauth.Auth{ID: "kimi-1", Provider: "kimi", FileName: kimiFile},
		&coreauth.Auth{ID: "codex-1", Provider: "codex", FileName: codexFile},
	)
	cfg := websearch.Config{}
	if !applyBorrowedSearchCredentials(&cfg, both, []string{"openai-compatible-kimi"}) {
		t.Fatal("expected the namespaced route to borrow a credential")
	}
	if cfg.KimiAPIKey != "kimi-tok" {
		t.Fatalf("kimi key = %q, want the Kimi session", cfg.KimiAPIKey)
	}
	if cfg.CodexAPIKey != "" {
		t.Fatalf("codex key = %q, want the generic openai entry not to win", cfg.CodexAPIKey)
	}

	// With only Codex present, the generic entry is the fallback.
	codexOnly := borrowedManager(&coreauth.Auth{ID: "codex-1", Provider: "codex", FileName: codexFile})
	cfg2 := websearch.Config{}
	if !applyBorrowedSearchCredentials(&cfg2, codexOnly, []string{"openai-compatible-kimi"}) {
		t.Fatal("expected a fallback borrow when no Kimi session exists")
	}
	if cfg2.CodexAPIKey != "codex-tok" {
		t.Fatalf("codex key = %q, want the generic fallback", cfg2.CodexAPIKey)
	}
}
