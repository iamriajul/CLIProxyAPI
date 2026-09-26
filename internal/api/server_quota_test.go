package api

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/router-for-me/CLIProxyAPI/v7/internal/quota"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/registry"
	"github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/auth"
)

func TestMaskInferenceEmail(t *testing.T) {
	cases := map[string]string{
		"jane.doe@example.com":    "j***e@example.com",
		"nazmul.iba.du@gmail.com": "n***u@gmail.com",
		"owner@example.com":       "o***r@example.com",
		"abcd@example.com":        "a***d@example.com",
		"abc@example.com":         "a***c@example.com",
		"a@example.com":           "a***@example.com",
		"ab@example.com":          "a***@example.com",
		"  spaced@example.com ":   "s***d@example.com",
		"no-at-sign":              "***sign",
		"@example.com":            "***.com",
		"user@":                   "***ser@",
		"":                        "",
	}
	for input, want := range cases {
		if got := maskInferenceEmail(input); got != want {
			t.Errorf("maskInferenceEmail(%q) = %q, want %q", input, got, want)
		}
	}
}

func TestMaskInferenceSecret(t *testing.T) {
	cases := map[string]string{
		"sk-abcdef123456": "***3456",
		"abcd":            "***",
		"ab":              "***",
		"":                "",
	}
	for input, want := range cases {
		if got := maskInferenceSecret(input); got != want {
			t.Errorf("maskInferenceSecret(%q) = %q, want %q", input, got, want)
		}
	}
}

func TestMaskInferenceEmailsInText(t *testing.T) {
	cases := map[string]string{
		"primary":                     "primary",
		"":                            "",
		"owner@example.com":           "o***r@example.com",
		"codex-owner@example.com-pro": "c***r@example.com-pro",
		"no email here":               "no email here",
	}
	for input, want := range cases {
		if got := maskInferenceEmailsInText(input); got != want {
			t.Errorf("maskInferenceEmailsInText(%q) = %q, want %q", input, got, want)
		}
	}
}

func TestMaskInferenceFileName(t *testing.T) {
	cases := map[string]string{
		"claude-nazmul.iba.du@gmail.com.json":                 "claude-n***u@gmail.com.json",
		"antigravity-iamriajulislamirfan@gmail.com.json":      "antigravity-i***n@gmail.com.json",
		"antigravity-it.riajul@gmail.com.json":                "antigravity-i***l@gmail.com.json",
		"antigravity-jannatulmakam3136@gmail.com.json":        "antigravity-j***6@gmail.com.json",
		"antigravity-riajulfamily@gmail.com.json":             "antigravity-r***y@gmail.com.json",
		"codex-kmriajulislami@gmail.com-pro.json":             "codex-k***i@gmail.com-pro.json",
		"xai-kmriajulislami@gmail.com.json":                   "xai-k***i@gmail.com.json",
		"meta-kmriajulislami_gmail.com-bad451d4a40585f9.json": "meta-k***i_gmail.com-bad451d4a40585f9.json",
		"opencode-1789311125517.json":                         "opencode-1789311125517.json",
		"zai-kmriajulislami@gmail.com.json":                   "zai-k***i@gmail.com.json",
		"meta-oauth.json":                                     "meta-oauth.json",
		"meta-0123456789abcdef.json":                          "meta-0123456789abcdef.json",
		"":                                                    "",
	}
	for input, want := range cases {
		if got := maskInferenceFileName(input); got != want {
			t.Errorf("maskInferenceFileName(%q) = %q, want %q", input, got, want)
		}
	}
}

func TestHandleInferenceQuota(t *testing.T) {
	server := newTestServer(t)
	manager := server.handlers.AuthManager

	observed := time.Date(2026, 9, 24, 12, 0, 0, 0, time.UTC)
	modelObserved := observed.Add(time.Hour)
	recoverAt := time.Now().Add(30 * time.Minute).Truncate(time.Second)
	oauthAuth := &auth.Auth{
		ID:       "quota-oauth-1",
		FileName: "codex-owner@example.com-pro.json",
		Provider: "codex",
		Label:    "codex-owner@example.com-pro",
		Status:   auth.StatusActive,
		Metadata: map[string]any{"email": "owner@example.com"},
		Quota: auth.QuotaState{
			ObservedAt: observed,
			Signals:    map[string]string{"X-Codex-Primary-Used-Percent": "58", "X-Codex-Plan-Type": "pro"},
		},
		ModelStates: map[string]*auth.ModelState{
			"quota-test-model": {
				Quota: auth.QuotaState{
					ObservedAt: modelObserved,
					Signals:    map[string]string{"X-Codex-Primary-Used-Percent": "61"},
				},
			},
		},
	}
	exhaustedAuth := &auth.Auth{
		ID:             "quota-oauth-2",
		Provider:       "codex",
		Status:         auth.StatusError,
		Unavailable:    true,
		NextRetryAfter: recoverAt,
		Metadata:       map[string]any{"email": "second@example.com"},
		Quota: auth.QuotaState{
			Exceeded:      true,
			Reason:        "quota",
			NextRecoverAt: recoverAt,
			ObservedAt:    observed,
			Signals:       map[string]string{"X-Codex-Primary-Used-Percent": "100"},
		},
	}
	keyAuth := &auth.Auth{
		ID:         "quota-key-1",
		FileName:   "codex-primary-key.json",
		Provider:   "codex",
		Status:     auth.StatusActive,
		Attributes: map[string]string{auth.AttributeAuthKind: auth.AuthKindAPIKey, auth.AttributeAPIKey: "sk-secret-1234"},
	}
	otherModelAuth := &auth.Auth{
		ID:       "quota-other-1",
		Provider: "codex",
		Status:   auth.StatusActive,
		Metadata: map[string]any{"email": "other@example.com"},
	}
	disabledAuth := &auth.Auth{
		ID:       "quota-disabled-1",
		Provider: "codex",
		Status:   auth.StatusActive,
		Disabled: true,
		Metadata: map[string]any{"email": "disabled@example.com"},
	}
	for _, item := range []*auth.Auth{oauthAuth, exhaustedAuth, keyAuth, otherModelAuth, disabledAuth} {
		if _, err := manager.Register(context.Background(), item); err != nil {
			t.Fatalf("failed to register auth %s: %v", item.ID, err)
		}
	}

	registryRef := registry.GetGlobalRegistry()
	registryRef.RegisterClient(oauthAuth.ID, "codex", []*registry.ModelInfo{{ID: "quota-test-model"}})
	registryRef.RegisterClient(exhaustedAuth.ID, "codex", []*registry.ModelInfo{{ID: "quota-test-model"}})
	registryRef.RegisterClient(keyAuth.ID, "codex", []*registry.ModelInfo{{ID: "quota-test-model"}})
	registryRef.RegisterClient(otherModelAuth.ID, "codex", []*registry.ModelInfo{{ID: "quota-other-model"}})
	registryRef.RegisterClient(disabledAuth.ID, "codex", []*registry.ModelInfo{{ID: "quota-test-model"}})
	t.Cleanup(func() {
		registryRef.UnregisterClient(oauthAuth.ID)
		registryRef.UnregisterClient(exhaustedAuth.ID)
		registryRef.UnregisterClient(keyAuth.ID)
		registryRef.UnregisterClient(otherModelAuth.ID)
		registryRef.UnregisterClient(disabledAuth.ID)
	})

	doRequest := func(target, key string) *httptest.ResponseRecorder {
		req := httptest.NewRequest(http.MethodGet, target, nil)
		if key != "" {
			req.Header.Set("Authorization", "Bearer "+key)
		}
		recorder := httptest.NewRecorder()
		server.engine.ServeHTTP(recorder, req)
		return recorder
	}

	if rec := doRequest("/v1/quota?model=quota-test-model", ""); rec.Code != http.StatusUnauthorized {
		t.Fatalf("missing key status = %d, want 401", rec.Code)
	}
	// Omitting the model parameter returns quota for all accounts.
	if rec := doRequest("/v1/quota", "test-key"); rec.Code != http.StatusOK {
		t.Fatalf("missing model status = %d, want 200, body = %s", rec.Code, rec.Body.String())
	} else {
		var all []map[string]any
		if err := json.Unmarshal(rec.Body.Bytes(), &all); err != nil {
			t.Fatalf("all-accounts response is not a JSON array: %v (%s)", err, rec.Body.String())
		}
		if len(all) != 4 {
			t.Fatalf("all accounts = %d, want 4 (disabled excluded): %s", len(all), rec.Body.String())
		}
	}
	if rec := doRequest("/v1/quota?model=quota-unknown-model", "test-key"); rec.Code != http.StatusNotFound {
		t.Fatalf("unknown model status = %d, want 404", rec.Code)
	}

	rec := doRequest("/v1/quota?model=quota-test-model", "test-key")
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", rec.Code, rec.Body.String())
	}
	body := rec.Body.String()
	for _, leaked := range []string{"owner@example.com", "second@example.com", "other@example.com", "disabled@example.com", "sk-secret-1234", "quota-oauth-1", "auth_index", "\"file\"", "\"signals\"", "\"status\""} {
		if strings.Contains(body, leaked) {
			t.Fatalf("response leaks %q: %s", leaked, body)
		}
	}

	var payload []map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &payload); err != nil {
		t.Fatalf("response is not a JSON array: %v (%s)", err, body)
	}
	if len(payload) != 3 {
		t.Fatalf("accounts = %d, want 3 (other-model and disabled excluded): %s", len(payload), body)
	}
	allowedKeys := map[string]bool{
		"provider": true, "provider_name": true, "name": true, "type": true, "plan": true,
		"description": true, "in_cooldown": true, "windows_observed_at": true, "windows": true,
		"reset_credits": true,
	}
	allowedWindowKeys := map[string]bool{
		"name": true, "used_percent": true, "reset_at": true, "status": true,
	}
	byName := make(map[string]map[string]any, len(payload))
	for _, account := range payload {
		for key := range account {
			if !allowedKeys[key] {
				t.Fatalf("account has non-minimal key %q: %s", key, body)
			}
		}
		for _, key := range []string{"provider", "type", "in_cooldown", "windows"} {
			if _, ok := account[key]; !ok {
				t.Fatalf("account misses required key %q: %s", key, body)
			}
		}
		if _, ok := account["description"]; ok {
			t.Fatalf("description reserved, must be absent for now: %s", body)
		}
		windows, ok := account["windows"].([]any)
		if !ok {
			t.Fatalf("windows = %#v, want array", account["windows"])
		}
		for _, raw := range windows {
			window, ok := raw.(map[string]any)
			if !ok {
				t.Fatalf("window = %#v, want object", raw)
			}
			for key := range window {
				if !allowedWindowKeys[key] {
					t.Fatalf("window has non-minimal key %q: %s", key, body)
				}
			}
		}
		name, _ := account["name"].(string)
		byName[name] = account
	}

	primary := byName["o***r@example.com"]
	if primary["type"] != "oauth" || primary["plan"] != "Pro 20x" || primary["provider_name"] != "Codex" {
		t.Fatalf("primary = %+v", primary)
	}
	if primary["in_cooldown"] != false {
		t.Fatalf("primary state = %+v", primary)
	}
	if primary["windows_observed_at"] != modelObserved.Format(time.RFC3339) {
		t.Fatalf("primary observed_at = %v", primary["windows_observed_at"])
	}
	primaryWindows := primary["windows"].([]any)
	if len(primaryWindows) != 1 {
		t.Fatalf("primary windows = %v, want merged single 5h", primaryWindows)
	}
	primaryWindow := primaryWindows[0].(map[string]any)
	if primaryWindow["name"] != "5h" || primaryWindow["used_percent"] != 61.0 {
		t.Fatalf("primary window = %v, want model observation winning", primaryWindow)
	}
	if _, ok := primaryWindow["windows_observed_at"]; ok {
		t.Fatalf("per-window timestamp must be gone: %v", primaryWindow)
	}

	exhausted := byName["s***d@example.com"]
	if exhausted["in_cooldown"] != true {
		t.Fatalf("exhausted = %+v", exhausted)
	}

	keyed := byName["codex-primary-key"]
	if keyed["type"] != "api" || keyed["in_cooldown"] != false || keyed["provider_name"] != "Codex" {
		t.Fatalf("key credential = %+v", keyed)
	}
	if windows := keyed["windows"].([]any); len(windows) != 0 {
		t.Fatalf("key windows = %v, want empty", windows)
	}
	if _, ok := keyed["windows_observed_at"]; ok {
		t.Fatalf("key observed_at present without observations: %+v", keyed)
	}
}

func TestInferenceQuotaPlanPrefersCodexHeader(t *testing.T) {
	credential := &auth.Auth{
		Provider: "codex",
		Quota: auth.QuotaState{Signals: map[string]string{
			"plan":               "Ambiguous",
			"X-Codex-Plan-Type":  "pro",
			"unrelated":          "x",
			"another-plan-alike": "y",
		}},
	}
	// Run repeatedly: map iteration order must not change the winner.
	for range 50 {
		if got := inferenceQuotaPlan(credential); got != "Pro 20x" {
			t.Fatalf("plan = %q, want Pro 20x", got)
		}
	}
	plain := &auth.Auth{
		Provider: "devin",
		Quota:    auth.QuotaState{Signals: map[string]string{"plan": "Pro"}},
	}
	if got := inferenceQuotaPlan(plain); got != "Pro" {
		t.Fatalf("plan = %q, want Pro", got)
	}
}

func TestInferenceQuotaAccountName(t *testing.T) {
	withEmail := &auth.Auth{
		FileName: "codex-owner@example.com-pro.json",
		Label:    "ignored-label",
		Metadata: map[string]any{"email": "owner@example.com"},
	}
	if got := inferenceQuotaAccountName(withEmail); got != "o***r@example.com" {
		t.Fatalf("email name = %q", got)
	}

	labelEmail := &auth.Auth{Label: "owner@example.com", FileName: "codex-owner@example.com-pro.json"}
	if got := inferenceQuotaAccountName(labelEmail); got != "o***r@example.com" {
		t.Fatalf("label email name = %q", got)
	}

	noEmail := &auth.Auth{FileName: "meta-kmriajulislami_gmail.com-bad451d4a40585f9.json", Provider: "meta"}
	if got := inferenceQuotaAccountName(noEmail); got != "meta-k***i_gmail.com-bad451d4a40585f9" {
		t.Fatalf("filename fallback = %q", got)
	}

	dotless := &auth.Auth{FileName: "codex-user@localhost.Json"}
	if got := inferenceQuotaAccountName(dotless); got != "codex-u***r@localhost" {
		t.Fatalf("dotless filename = %q", got)
	}
	neither := &auth.Auth{Provider: "codex"}
	if got := inferenceQuotaAccountName(neither); got != "" {
		t.Fatalf("empty name = %q", got)
	}
}

func TestInferenceQuotaProviderDisplayName(t *testing.T) {
	cases := map[string]string{
		"codex": "Codex", "claude": "Claude", "antigravity": "Antigravity",
		"xai": "xAI", "grok": "xAI", "zai": "Z.AI", "glm": "Z.AI",
		"opencode-go": "OpenCode", "kimi.ai": "Kimi", "meta": "Meta", "muse": "Meta",
		"gemini": "Gemini", "vertex": "Vertex AI", "aistudio": "AI Studio",
		"devin": "Devin", "custom": "Custom", "": "",
	}
	for input, want := range cases {
		if got := inferenceQuotaProviderDisplayName(input); got != want {
			t.Errorf("inferenceQuotaProviderDisplayName(%q) = %q, want %q", input, got, want)
		}
	}
}

func TestMapQuotaResets(t *testing.T) {
	later := time.Date(2026, 10, 15, 0, 0, 0, 0, time.UTC)
	sooner := time.Date(2026, 10, 1, 0, 0, 0, 0, time.UTC)
	got := mapQuotaResets(&quota.Resets{
		Available: 2,
		Credits: []quota.ResetCredit{
			{ExpiresAt: later},
			{ExpiresAt: sooner},
		},
	})
	if len(got) != 2 || !got[0].ExpiresAt.Equal(sooner) || !got[1].ExpiresAt.Equal(later) {
		t.Fatalf("resets = %+v, want soonest first", got)
	}
	// A count without expiries cannot be represented as {expires_at} objects.
	// Absence means unknown expiry, not zero credits.
	if got := mapQuotaResets(&quota.Resets{Available: 2}); got != nil {
		t.Fatalf("count-only = %+v, want nil", got)
	}
	if got := mapQuotaResets(nil); got != nil {
		t.Fatalf("nil = %+v, want nil", got)
	}
}
