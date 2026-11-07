package api

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/router-for-me/CLIProxyAPI/v8/internal/config"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/registry"
	runtimeexecutor "github.com/router-for-me/CLIProxyAPI/v8/internal/runtime/executor"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/sessionroute"
	_ "github.com/router-for-me/CLIProxyAPI/v8/internal/translator"
	coreauth "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/auth"
)

func newSessionRouteTestServer(t *testing.T) *Server {
	t.Helper()
	sessionroute.Default().Reset()
	t.Cleanup(sessionroute.Default().Reset)
	return newTestServer(t)
}

func getSessionRoute(t *testing.T, server *Server, session string) *httptest.ResponseRecorder {
	t.Helper()
	return getTPS(t, server, "/v1/session-route?session="+session, "test-key")
}

func decodeSessionRoute(t *testing.T, rec *httptest.ResponseRecorder) sessionRouteResponse {
	t.Helper()
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200, body = %s", rec.Code, rec.Body.String())
	}
	var payload sessionRouteResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &payload); err != nil {
		t.Fatalf("decode %s: %v", rec.Body.String(), err)
	}
	return payload
}

// TestSessionRouteFollowsRealFailover drives Claude requests through the real
// /v1/messages handler, conductor and Claude executor against a fake upstream.
// The session is served by account A until A answers 429; the conductor then
// fails over to B within the same request, and the endpoint must report B as
// the serving account with a quota switch from A.
func TestSessionRouteFollowsRealFailover(t *testing.T) {
	server := newSessionRouteTestServer(t)
	const model = "claude-session-route-test"
	sessionUUID := fmt.Sprintf("0b6c2f1e-%04d-4a5b-9c8d-%012d", time.Now().UnixNano()%10000, time.Now().UnixNano()%1_000_000_000_000)

	var rateLimitA atomic.Bool
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		account := r.Header.Get("X-Api-Key")
		if account == "" {
			account = strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer ")
		}
		if account == "key-a" && rateLimitA.Load() {
			w.Header().Set("Content-Type", "application/json")
			w.Header().Set("Retry-After", "3600")
			w.WriteHeader(http.StatusTooManyRequests)
			_, _ = w.Write([]byte(`{"type":"error","error":{"type":"rate_limit_error","message":"rate limited"}}`))
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = fmt.Fprintf(w, `{"id":"msg_1","type":"message","role":"assistant","model":%q,"content":[{"type":"text","text":"ok"}],"stop_reason":"end_turn","usage":{"input_tokens":3,"output_tokens":2}}`, model)
	}))
	defer upstream.Close()

	manager := server.handlers.AuthManager
	manager.RegisterExecutor(runtimeexecutor.NewClaudeExecutor(&config.Config{}))
	for i, account := range []string{"a", "b"} {
		id := "sr-auth-" + account
		registry.GetGlobalRegistry().RegisterClient(id, "claude", []*registry.ModelInfo{{ID: model}})
		t.Cleanup(func() { registry.GetGlobalRegistry().UnregisterClient(id) })
		if _, err := manager.Register(context.Background(), &coreauth.Auth{
			ID: id, Provider: "claude", Status: coreauth.StatusActive,
			Attributes: map[string]string{"priority": fmt.Sprint(4 - i), "base_url": upstream.URL, "api_key": "key-" + account},
			Metadata:   map[string]any{"email": "user-" + account + "@example.com"},
		}); err != nil {
			t.Fatal(err)
		}
	}

	send := func() {
		t.Helper()
		body := fmt.Sprintf(`{"model":%q,"max_tokens":16,"messages":[{"role":"user","content":"hi"}]}`, model)
		req := httptest.NewRequest(http.MethodPost, "/v1/messages", strings.NewReader(body))
		req.Header.Set("Authorization", "Bearer test-key")
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("X-Claude-Code-Session-Id", sessionUUID)
		rec := httptest.NewRecorder()
		server.engine.ServeHTTP(rec, req)
		if rec.Code != http.StatusOK {
			t.Fatalf("messages status = %d, body = %s", rec.Code, rec.Body.String())
		}
	}
	// Usage records reach the tracker through the asynchronous usage bus.
	waitForRoute := func(want func(sessionRouteResponse) bool) sessionRouteResponse {
		t.Helper()
		var last *httptest.ResponseRecorder
		deadline := time.Now().Add(5 * time.Second)
		for time.Now().Before(deadline) {
			last = getSessionRoute(t, server, "claude:"+sessionUUID)
			if last.Code == http.StatusOK {
				if got := decodeSessionRoute(t, last); want(got) {
					return got
				}
			}
			time.Sleep(10 * time.Millisecond)
		}
		t.Fatalf("route never reached the expected state; last = %d %s", last.Code, last.Body.String())
		return sessionRouteResponse{}
	}

	send()
	first := waitForRoute(func(r sessionRouteResponse) bool { return r.Account.Name != "" })
	if first.Account.Name == "" || strings.Contains(first.Account.Name, "user-a") {
		t.Fatalf("account name = %q, want the masked label of account a", first.Account.Name)
	}
	if first.Account.Provider != "claude" || first.Account.Type != "api" || len(first.Switches) != 0 {
		t.Fatalf("first route = %+v, want account a, api, no switches", first)
	}
	accountA := first.Account.Name

	rateLimitA.Store(true)
	send()
	second := waitForRoute(func(r sessionRouteResponse) bool { return len(r.Switches) == 1 })
	sw := second.Switches[0]
	if sw.From.Name != accountA || sw.To.Name == accountA || second.Account.Name != sw.To.Name {
		t.Fatalf("switch = %+v, account = %+v, want a -> b", sw, second.Account)
	}
	if sw.Reason != string(sessionroute.ReasonQuota) || sw.StatusCode != http.StatusTooManyRequests {
		t.Fatalf("switch reason = %s/%d, want quota/429", sw.Reason, sw.StatusCode)
	}
	if !sw.From.InCooldown {
		t.Fatalf("from = %+v, want account a in cooldown after its 429", sw.From)
	}
	// The 429 carried Retry-After: 3600, so the cooldown's end is known and reported.
	if sw.From.CooldownUntil == nil || sw.From.CooldownUntil.Before(time.Now().Add(50*time.Minute)) {
		t.Fatalf("from cooldown_until = %v, want about an hour ahead", sw.From.CooldownUntil)
	}
	if sw.To.CooldownUntil != nil {
		t.Fatalf("to cooldown_until = %v, want none for a healthy account", sw.To.CooldownUntil)
	}
	if body := getSessionRoute(t, server, "claude:"+sessionUUID).Body.String(); strings.Contains(body, "user-a") || strings.Contains(body, "user-b") || strings.Contains(body, "sr-auth-") {
		t.Fatalf("body = %s, must carry only masked labels, never emails or auth ids", body)
	}
}

func TestSessionRouteErrors(t *testing.T) {
	server := newSessionRouteTestServer(t)
	requireTPSError(t, getTPS(t, server, "/v1/session-route", "test-key"), http.StatusBadRequest)
	requireTPSError(t, getSessionRoute(t, server, "claude:unknown"), http.StatusNotFound)
	if rec := getTPS(t, server, "/v1/session-route?session=claude:x", "wrong-key"); rec.Code != http.StatusUnauthorized {
		t.Fatalf("bad key status = %d, want 401", rec.Code)
	}
}

// TestSessionRouteRemovedAccountKeepsProvider covers a switch whose account
// was later removed: the label falls back to the provider, never to an id.
func TestSessionRouteRemovedAccountKeepsProvider(t *testing.T) {
	server := newSessionRouteTestServer(t)
	now := time.Now()
	sessionroute.Default().Observe(sessionroute.Observation{SessionID: "codex:s1", AuthID: "gone-a", Provider: "codex", Model: "gpt-5.6", At: now})
	sessionroute.Default().Observe(sessionroute.Observation{SessionID: "codex:s1", AuthID: "gone-b", Provider: "codex", Model: "gpt-5.6", At: now.Add(time.Second)})

	rec := getSessionRoute(t, server, "codex:s1")
	got := decodeSessionRoute(t, rec)
	if got.Account.ProviderName != "Codex" || got.Account.Name != "" || len(got.Switches) != 1 || got.Switches[0].Reason != "rebind" {
		t.Fatalf("route = %+v, want provider-only labels and one rebind", got)
	}
	if strings.Contains(rec.Body.String(), "gone-") {
		t.Fatalf("body = %s, must not expose auth ids", rec.Body.String())
	}
}

func TestSessionRouteSchemaParity(t *testing.T) {
	raw, err := os.ReadFile(filepath.Join("..", "..", "api", "v1-session-route.schema.json"))
	if err != nil {
		t.Fatalf("read schema: %v", err)
	}
	type object struct {
		Required   []string       `json:"required"`
		Properties map[string]any `json:"properties"`
	}
	var schema struct {
		object
		Defs map[string]object `json:"$defs"`
	}
	if err := json.Unmarshal(raw, &schema); err != nil {
		t.Fatalf("schema is not valid JSON: %v", err)
	}
	check := func(name string, got object, structType reflect.Type) {
		t.Helper()
		wantRequired, wantFields := jsonTagSets(structType)
		if !equalStringSets(got.Required, wantRequired) {
			t.Fatalf("%s required = %v, want %v", name, got.Required, wantRequired)
		}
		var gotFields []string
		for field := range got.Properties {
			gotFields = append(gotFields, field)
		}
		if !equalStringSets(gotFields, wantFields) {
			t.Fatalf("%s properties = %v, want %v", name, gotFields, wantFields)
		}
	}
	check("root", schema.object, reflect.TypeOf(sessionRouteResponse{}))
	check("account", schema.Defs["account"], reflect.TypeOf(sessionRouteAccount{}))
	check("switch", schema.Defs["switch"], reflect.TypeOf(sessionRouteSwitch{}))
}
