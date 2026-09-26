package api

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"reflect"
	"sort"
	"strings"
	"testing"
	"time"

	"github.com/router-for-me/CLIProxyAPI/v7/internal/quota"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/registry"
	"github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/auth"
)

// TestHandleInferenceQuota_Contract locks the wire shape external integrations
// build against: top-level array, required keys, JSON types, merged windows
// with per-source observation times, and cooldown/description semantics.
func TestHandleInferenceQuota_Contract(t *testing.T) {
	server := newTestServer(t)
	manager := server.handlers.AuthManager

	observed := time.Date(2026, 9, 24, 12, 0, 0, 0, time.UTC)
	modelObserved := observed.Add(time.Hour)
	credential := &auth.Auth{
		ID:       "quota-contract-1",
		Provider: "codex",
		Label:    "contract@example.com",
		Status:   auth.StatusActive,
		Metadata: map[string]any{"email": "contract@example.com"},
		Quota: auth.QuotaState{
			ObservedAt: observed,
			Signals: map[string]string{
				"X-Codex-Secondary-Used-Percent":   "35",
				"X-Codex-Secondary-Window-Minutes": "10080",
			},
		},
		ModelStates: map[string]*auth.ModelState{
			"quota-contract-model": {
				Quota: auth.QuotaState{
					ObservedAt: modelObserved,
					Signals:    map[string]string{"X-Codex-Primary-Used-Percent": "99"},
				},
			},
		},
	}
	if _, err := manager.Register(context.Background(), credential); err != nil {
		t.Fatalf("failed to register auth: %v", err)
	}
	registryRef := registry.GetGlobalRegistry()
	registryRef.RegisterClient(credential.ID, "codex", []*registry.ModelInfo{{ID: "quota-contract-model"}})
	t.Cleanup(func() { registryRef.UnregisterClient(credential.ID) })

	req := httptest.NewRequest(http.MethodGet, "/v1/quota?model=quota-contract-model", nil)
	req.Header.Set("Authorization", "Bearer test-key")
	rec := httptest.NewRecorder()
	server.engine.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", rec.Code, rec.Body.String())
	}

	var payload []map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &payload); err != nil {
		t.Fatalf("response is not a JSON array: %v", err)
	}
	if len(payload) != 1 {
		t.Fatalf("accounts = %d, want 1", len(payload))
	}
	account := payload[0]
	for _, key := range []string{"provider", "provider_name", "name", "type"} {
		if _, ok := account[key].(string); !ok {
			t.Fatalf("account %q = %#v, want string", key, account[key])
		}
	}
	if account["type"] != "oauth" || account["name"] != "co***ct@example.com" || account["provider_name"] != "Codex" {
		t.Fatalf("account = %+v", account)
	}
	if _, ok := account["description"]; ok {
		t.Fatalf("description reserved, must be absent for now: %+v", account)
	}
	if inCooldown, ok := account["in_cooldown"].(bool); !ok || inCooldown {
		t.Fatalf("in_cooldown = %#v, want false", account["in_cooldown"])
	}
	observedRaw, ok := account["windows_observed_at"].(string)
	if !ok {
		t.Fatalf("windows_observed_at = %#v, want RFC 3339 string", account["windows_observed_at"])
	}
	if parsed, err := time.Parse(time.RFC3339, observedRaw); err != nil || !parsed.Equal(modelObserved) {
		t.Fatalf("windows_observed_at = %q, want newest observation", observedRaw)
	}
	windows, ok := account["windows"].([]any)
	if !ok || len(windows) != 2 {
		t.Fatalf("windows = %#v, want merged 5h + 7d", account["windows"])
	}
	byName := make(map[string]map[string]any, len(windows))
	for _, raw := range windows {
		window, ok := raw.(map[string]any)
		if !ok {
			t.Fatalf("window = %#v, want object", raw)
		}
		name, ok := window["name"].(string)
		if !ok || name == "" {
			t.Fatalf("window name = %#v", window["name"])
		}
		byName[name] = window
		if used, ok := window["used_percent"].(float64); !ok {
			t.Fatalf("%s used_percent = %#v, want number", name, window["used_percent"])
		} else if used < 0 || used > 100 {
			t.Fatalf("%s used_percent = %v, want 0-100", name, used)
		}
		if _, ok := window["windows_observed_at"]; ok {
			t.Fatalf("%s carries per-window timestamp, want account-level only", name)
		}
	}
	if byName["5h"]["used_percent"] != 99.0 {
		t.Fatalf("5h window = %+v, want model observation", byName["5h"])
	}
	if byName["7d"]["used_percent"] != 35.0 {
		t.Fatalf("7d window = %+v, want credential observation", byName["7d"])
	}
}

// TestHandleInferenceQuota_CooldownFlag locks the cooldown flag semantics:
// active is free, future timers block, expired timers count as recovered,
// and flag-only states block indefinitely.
func TestHandleInferenceQuota_CooldownFlag(t *testing.T) {
	now := time.Date(2026, 9, 25, 10, 0, 0, 0, time.UTC)
	future := now.Add(30 * time.Minute)
	past := now.Add(-time.Hour)

	activeAuth := &auth.Auth{ID: "cool-active", Provider: "codex", Status: auth.StatusActive}
	if inCooldown, recoverAt := inferenceQuotaCooldown(activeAuth, nil, now); inCooldown || !recoverAt.IsZero() {
		t.Fatalf("active = (%v, %v)", inCooldown, recoverAt)
	}

	timedAuth := &auth.Auth{
		ID: "cool-timed", Provider: "codex", Status: auth.StatusError,
		Unavailable: true, NextRetryAfter: future,
		Quota: auth.QuotaState{Exceeded: true, Reason: "quota", NextRecoverAt: future},
	}
	inCooldown, recoverAt := inferenceQuotaCooldown(timedAuth, nil, now)
	if !inCooldown || !recoverAt.Equal(future) {
		t.Fatalf("timed = (%v, %v)", inCooldown, recoverAt)
	}

	expiredAuth := &auth.Auth{
		ID: "cool-expired", Provider: "codex", Status: auth.StatusError,
		Unavailable: true, NextRetryAfter: past,
		Quota: auth.QuotaState{Exceeded: true, Reason: "quota", NextRecoverAt: past},
	}
	if inCooldown, _ := inferenceQuotaCooldown(expiredAuth, nil, now); inCooldown {
		t.Fatal("expired timers must count as recovered")
	}

	indefiniteAuth := &auth.Auth{
		ID: "cool-indefinite", Provider: "codex", Status: auth.StatusError, Unavailable: true,
	}
	inCooldown, recoverAt = inferenceQuotaCooldown(indefiniteAuth, nil, now)
	if !inCooldown || !recoverAt.IsZero() {
		t.Fatalf("indefinite = (%v, %v)", inCooldown, recoverAt)
	}

	modelBlocked := &auth.ModelState{Unavailable: true, NextRetryAfter: future}
	if inCooldown, _ := inferenceQuotaCooldown(activeAuth, modelBlocked, now); !inCooldown {
		t.Fatal("model-level block must raise the account flag")
	}
}

// TestHandleInferenceQuota_ErrorEnvelope locks the endpoint-wide error shape:
// every failure on this route is {"error": string}, matching the v1 auth
// middleware, so clients can branch on status and read one field.
func TestHandleInferenceQuota_ErrorEnvelope(t *testing.T) {
	server := newTestServer(t)
	cases := []struct {
		name   string
		target string
		key    string
		status int
	}{
		{name: "unauthorized", target: "/v1/quota?model=quota-contract-model", key: "", status: http.StatusUnauthorized},
		{name: "not found", target: "/v1/quota?model=quota-never-registered", key: "test-key", status: http.StatusNotFound},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			req := httptest.NewRequest(http.MethodGet, tc.target, nil)
			if tc.key != "" {
				req.Header.Set("Authorization", "Bearer "+tc.key)
			}
			rec := httptest.NewRecorder()
			server.engine.ServeHTTP(rec, req)
			if rec.Code != tc.status {
				t.Fatalf("status = %d, want %d (%s)", rec.Code, tc.status, rec.Body.String())
			}
			var payload map[string]any
			if err := json.Unmarshal(rec.Body.Bytes(), &payload); err != nil {
				t.Fatalf("failed to decode error: %v", err)
			}
			if len(payload) != 1 {
				t.Fatalf("error keys = %v, want exactly [error]", payload)
			}
			message, ok := payload["error"].(string)
			if !ok || strings.TrimSpace(message) == "" {
				t.Fatalf("error = %#v, want non-empty string", payload["error"])
			}
		})
	}
}

// TestHandleInferenceQuota_NoModelReturnsAllAccounts locks the fallback when
// the model query parameter is omitted: quota for every non-disabled account
// is returned instead of a 400.
func TestHandleInferenceQuota_NoModelReturnsAllAccounts(t *testing.T) {
	server := newTestServer(t)
	manager := server.handlers.AuthManager
	credential := &auth.Auth{
		ID:       "quota-nomodel-1",
		Provider: "codex",
		Status:   auth.StatusActive,
		Metadata: map[string]any{"email": "nomodel@example.com"},
	}
	if _, err := manager.Register(context.Background(), credential); err != nil {
		t.Fatalf("failed to register auth: %v", err)
	}
	req := httptest.NewRequest(http.MethodGet, "/v1/quota", nil)
	req.Header.Set("Authorization", "Bearer test-key")
	rec := httptest.NewRecorder()
	server.engine.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200 (%s)", rec.Code, rec.Body.String())
	}
	var payload []map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &payload); err != nil {
		t.Fatalf("response is not a JSON array: %v", err)
	}
	if len(payload) != 1 {
		t.Fatalf("accounts = %d, want 1", len(payload))
	}
}

// TestInferenceQuotaSchemaParity guards the published contract
// (api/v1-quota.schema.json) against struct drift in either direction:
// every JSON field must be documented, and every documented required field
// must exist.
func TestInferenceQuotaSchemaParity(t *testing.T) {
	schemaPath := filepath.Join("..", "..", "api", "v1-quota.schema.json")
	raw, err := os.ReadFile(schemaPath)
	if err != nil {
		t.Fatalf("failed to read schema: %v", err)
	}
	var schema struct {
		Type  string `json:"type"`
		Items struct {
			Ref string `json:"$ref"`
		} `json:"items"`
		Defs map[string]struct {
			Required   []string       `json:"required"`
			Properties map[string]any `json:"properties"`
		} `json:"$defs"`
	}
	if err := json.Unmarshal(raw, &schema); err != nil {
		t.Fatalf("schema is not valid JSON: %v", err)
	}
	if schema.Type != "array" || schema.Items.Ref != "#/$defs/account" {
		t.Fatalf("schema root = (%q, %q), want array of account", schema.Type, schema.Items.Ref)
	}

	check := func(name string, value any, required []string, properties map[string]any) {
		t.Helper()
		wantRequired, wantFields := jsonTagSets(reflect.TypeOf(value))
		if !equalStringSets(required, wantRequired) {
			t.Fatalf("%s required = %v, want %v", name, required, wantRequired)
		}
		var gotFields []string
		for field := range properties {
			gotFields = append(gotFields, field)
		}
		if !equalStringSets(gotFields, wantFields) {
			t.Fatalf("%s properties = %v, want %v", name, gotFields, wantFields)
		}
	}
	check("account", inferenceQuotaAccount{}, schema.Defs["account"].Required, schema.Defs["account"].Properties)
	check("window", inferenceQuotaWindow{}, schema.Defs["window"].Required, schema.Defs["window"].Properties)
}

func jsonTagSets(structType reflect.Type) (required, fields []string) {
	for i := range structType.NumField() {
		tag := structType.Field(i).Tag.Get("json")
		name, options, _ := strings.Cut(tag, ",")
		if name == "" || name == "-" {
			continue
		}
		fields = append(fields, name)
		if !strings.Contains(options, "omitempty") {
			required = append(required, name)
		}
	}
	sort.Strings(required)
	sort.Strings(fields)
	return required, fields
}

func equalStringSets(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	counts := make(map[string]int, len(a))
	for _, value := range a {
		counts[value]++
	}
	for _, value := range b {
		counts[value]--
		if counts[value] < 0 {
			return false
		}
	}
	return true
}

type quotaContractFakeFetcher struct {
	calls int
	snap  *quota.Snapshot
	err   error
}

func (f *quotaContractFakeFetcher) Provider() string { return "codex" }

func (f *quotaContractFakeFetcher) Fetch(context.Context, quota.FetchRequest) (*quota.Snapshot, error) {
	f.calls++
	if f.err != nil {
		return nil, f.err
	}
	return f.snap, nil
}

// TestHandleInferenceQuota_LiveWins locks live-first integration across four
// phases: error with empty cache degrades to passive, success serves live,
// ?refresh=cached keeps passive, and error with fresh cache serves the cache.
func TestHandleInferenceQuota_LiveWins(t *testing.T) {
	server := newTestServer(t)
	manager := server.handlers.AuthManager
	observed := time.Date(2026, 9, 24, 12, 0, 0, 0, time.UTC)
	credential := &auth.Auth{
		ID: "quota-live-1", Provider: "codex", Status: auth.StatusActive,
		Metadata: map[string]any{"email": "live@example.com"},
		Quota: auth.QuotaState{
			ObservedAt: observed,
			Signals:    map[string]string{"X-Codex-Primary-Used-Percent": "10"},
		},
	}
	if _, err := manager.Register(context.Background(), credential); err != nil {
		t.Fatalf("failed to register auth: %v", err)
	}
	registryRef := registry.GetGlobalRegistry()
	registryRef.RegisterClient(credential.ID, "codex", []*registry.ModelInfo{{ID: "quota-live-model"}})
	t.Cleanup(func() { registryRef.UnregisterClient(credential.ID) })

	fetcher := &quotaContractFakeFetcher{
		err: errors.New("upstream down"),
		snap: &quota.Snapshot{
			Windows: []quota.Window{{Name: "5h", UsedPercent: quota.UsedPercent(77)}},
			Plan:    "team",
		},
	}
	server.quotaService.Register(fetcher)

	get := func(target string) []map[string]any {
		t.Helper()
		req := httptest.NewRequest(http.MethodGet, target, nil)
		req.Header.Set("Authorization", "Bearer test-key")
		rec := httptest.NewRecorder()
		server.engine.ServeHTTP(rec, req)
		if rec.Code != http.StatusOK {
			t.Fatalf("%s status = %d (%s)", target, rec.Code, rec.Body.String())
		}
		var payload []map[string]any
		if err := json.Unmarshal(rec.Body.Bytes(), &payload); err != nil {
			t.Fatalf("failed to decode %s: %v", target, err)
		}
		return payload
	}
	used := func(payload []map[string]any) float64 {
		t.Helper()
		windows := payload[0]["windows"].([]any)
		if len(windows) != 1 {
			t.Fatalf("windows = %v, want 1", windows)
		}
		return windows[0].(map[string]any)["used_percent"].(float64)
	}
	if got := used(get("/v1/quota?model=quota-live-model")); got != 10.0 {
		t.Fatalf("error+empty cache used = %v, want passive 10", got)
	}
	if fetcher.calls != 1 {
		t.Fatalf("calls = %d, want 1 fetch attempt", fetcher.calls)
	}
	fetcher.err = nil
	live := get("/v1/quota?model=quota-live-model")
	if live[0]["plan"] != "team" || used(live) != 77.0 {
		t.Fatalf("live = %+v, want live snapshot", live[0])
	}
	if got := used(get("/v1/quota?model=quota-live-model&refresh=cached")); got != 77.0 {
		t.Fatalf("cached used = %v, want fresh cached live 77", got)
	}
	if fetcher.calls != 2 {
		t.Fatalf("calls = %d, want no fetch on cached mode", fetcher.calls)
	}
	fetcher.err = errors.New("upstream down")
	if got := used(get("/v1/quota?model=quota-live-model&refresh=live")); got != 77.0 {
		t.Fatalf("error+fresh cache used = %v, want cached live 77", got)
	}
}
