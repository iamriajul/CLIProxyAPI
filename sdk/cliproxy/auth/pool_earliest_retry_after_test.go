package auth

import (
	"context"
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/router-for-me/CLIProxyAPI/v8/internal/registry"
	cliproxyexecutor "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/executor"
)

type poolRetryAfterCase struct {
	// preCool marks the "soon" credential as cooling before the request;
	// otherwise it is attempted first and returns its own T+3h 429.
	preCool bool
	// suffixed makes the late upstream message already carry its own
	// RETRY_AFTER text, as a passthrough upstream error would.
	suffixed     bool
	requestRetry int
}

// newPoolRetryAfterManager registers two Codex credentials for one model.
// "a-soon" recovers at about T+3h; "b-late" answers with an upstream
// usage-limit 429 whose reset is T+44h.
func newPoolRetryAfterManager(t *testing.T, model string, tc poolRetryAfterCase) (*Manager, time.Time) {
	t.Helper()
	withQuotaCooldownEnabled(t)
	manager := NewManager(nil, &RoundRobinSelector{}, nil)
	manager.SetRetryConfig(tc.requestRetry, 30*time.Second, 0)
	soonID, lateID := "pool-a-soon-"+t.Name(), "pool-b-late-"+t.Name()
	for _, candidate := range []*Auth{
		{ID: soonID, Provider: "codex", Status: StatusActive},
		{ID: lateID, Provider: "codex", Status: StatusActive},
	} {
		registry.GetGlobalRegistry().RegisterClient(candidate.ID, "codex", []*registry.ModelInfo{{ID: model}})
		id := candidate.ID
		t.Cleanup(func() { registry.GetGlobalRegistry().UnregisterClient(id) })
		if _, errRegister := manager.Register(context.Background(), candidate); errRegister != nil {
			t.Fatal(errRegister)
		}
	}
	soonWait, lateWait := 3*time.Hour, 44*time.Hour
	soonReset, lateReset := time.Now().Add(soonWait), time.Now().Add(lateWait)
	if tc.preCool {
		manager.MarkResult(context.Background(), Result{
			AuthID: soonID, Provider: "codex", Model: model,
			Error:      &Error{HTTPStatus: http.StatusTooManyRequests, Message: "usage limit"},
			RetryAfter: &soonWait,
		})
	}
	execute := func(_ context.Context, selected *Auth, _ cliproxyexecutor.Request, _ cliproxyexecutor.Options) (cliproxyexecutor.Response, error) {
		wait, reset := lateWait, lateReset
		if selected.ID == soonID {
			if tc.preCool {
				t.Errorf("cooling credential %s must not be attempted", soonID)
			}
			wait, reset = soonWait, soonReset
		}
		message := "The usage limit has been reached"
		if tc.suffixed {
			message += " " + FormatRetryAfterSuffix(reset)
		}
		return cliproxyexecutor.Response{}, streamQuotaError{
			customStatusError: customStatusError{
				code:       http.StatusTooManyRequests,
				msg:        `{"error":{"type":"usage_limit_reached","message":"` + message + `"}}`,
				retryAfter: &wait,
			},
			credentialScoped: true,
		}
	}
	manager.RegisterExecutor(&customStreamMockExecutor{
		identifier:              "codex",
		mockCustomErrorExecutor: mockCustomErrorExecutor{identifier: "codex", executeFn: execute},
		streamFn: func(ctx context.Context, selected *Auth, req cliproxyexecutor.Request, opts cliproxyexecutor.Options) (*cliproxyexecutor.StreamResult, error) {
			_, errExecute := execute(ctx, selected, req, opts)
			return nil, errExecute
		},
	})
	return manager, soonReset
}

func retryAfterInstants(text string) []string {
	var out []string
	for rest := text; ; {
		idx := strings.Index(rest, "RETRY_AFTER:")
		if idx < 0 {
			return out
		}
		rest = rest[idx+len("RETRY_AFTER:"):]
		end := 0
		for end < len(rest) && rest[end] != '"' && rest[end] != ' ' && rest[end] != ')' && rest[end] != '\\' {
			end++
		}
		out = append(out, rest[:end])
	}
}

func assertNearInstant(t *testing.T, label string, got, want time.Time) {
	t.Helper()
	if diff := got.Sub(want); diff < -2*time.Second || diff > 2*time.Second {
		t.Fatalf("%s = %s, want about %s", label, got.UTC().Format(time.RFC3339), want.UTC().Format(time.RFC3339))
	}
}

// When every credential that serves the model is exhausted, the client must be
// told the pool's earliest recovery, not the last upstream error's own reset.
func TestManager_PoolExhaustedReportsEarliestRecovery(t *testing.T) {
	const model = "pool-retry-model"
	for _, path := range []string{"execute", "stream"} {
		for _, tc := range []poolRetryAfterCase{
			{preCool: true}, {preCool: true, suffixed: true},
			{preCool: false}, {preCool: false, suffixed: true},
			{preCool: true, requestRetry: 2}, {preCool: false, suffixed: true, requestRetry: 2},
		} {
			name := fmt.Sprintf("%s/precool=%t/suffixed=%t/retry=%d", path, tc.preCool, tc.suffixed, tc.requestRetry)
			t.Run(name, func(t *testing.T) {
				manager, soonReset := newPoolRetryAfterManager(t, model, tc)
				var errRun error
				if path == "execute" {
					_, errRun = manager.Execute(context.Background(), []string{"codex"}, cliproxyexecutor.Request{Model: model}, cliproxyexecutor.Options{})
				} else {
					_, errRun = manager.ExecuteStream(context.Background(), []string{"codex"}, cliproxyexecutor.Request{Model: model}, cliproxyexecutor.Options{Stream: true})
				}
				if errRun == nil {
					t.Fatal("expected the exhausted pool to fail")
				}
				rec, ok := EarliestRecoveryTime(errRun)
				if !ok {
					t.Fatalf("expected a recovery instant on %v", errRun)
				}
				assertNearInstant(t, "EarliestRecoveryTime", rec, soonReset)
				text := errRun.Error()
				instants := retryAfterInstants(text)
				if len(instants) != 1 {
					t.Fatalf("want exactly one RETRY_AFTER, got %v in %q", instants, text)
				}
				at, errParse := time.Parse(time.RFC3339, instants[0])
				if errParse != nil {
					t.Fatalf("parse %q: %v", instants[0], errParse)
				}
				assertNearInstant(t, "RETRY_AFTER in error text", at, soonReset)
				header := SafeResponseHeaders(errRun).Get("Retry-After")
				seconds, errAtoi := strconv.Atoi(header)
				if errAtoi != nil {
					t.Fatalf("Retry-After header = %q, want seconds", header)
				}
				assertNearInstant(t, "Retry-After header", time.Now().Add(time.Duration(seconds)*time.Second), soonReset)
				if !strings.Contains(text, "The usage limit has been reached") {
					t.Fatalf("upstream message lost: %q", text)
				}
			})
		}
	}
}

// A single credential keeps its own upstream reset: there is no pool to
// aggregate over.
func TestManager_SingleCredentialKeepsUpstreamRecovery(t *testing.T) {
	withQuotaCooldownEnabled(t)
	const model = "pool-single-model"
	manager := NewManager(nil, &RoundRobinSelector{}, nil)
	manager.SetRetryConfig(0, 30*time.Second, 0)
	id := "pool-single-" + t.Name()
	registry.GetGlobalRegistry().RegisterClient(id, "codex", []*registry.ModelInfo{{ID: model}})
	t.Cleanup(func() { registry.GetGlobalRegistry().UnregisterClient(id) })
	if _, errRegister := manager.Register(context.Background(), &Auth{ID: id, Provider: "codex", Status: StatusActive}); errRegister != nil {
		t.Fatal(errRegister)
	}
	wait := 44 * time.Hour
	reset := time.Now().Add(wait)
	manager.RegisterExecutor(&mockCustomErrorExecutor{
		identifier: "codex",
		executeFn: func(context.Context, *Auth, cliproxyexecutor.Request, cliproxyexecutor.Options) (cliproxyexecutor.Response, error) {
			return cliproxyexecutor.Response{}, streamQuotaError{
				customStatusError: customStatusError{code: http.StatusTooManyRequests, msg: "The usage limit has been reached", retryAfter: &wait},
				credentialScoped:  true,
			}
		},
	})
	_, errExecute := manager.Execute(context.Background(), []string{"codex"}, cliproxyexecutor.Request{Model: model}, cliproxyexecutor.Options{})
	rec, ok := EarliestRecoveryTime(errExecute)
	if !ok {
		t.Fatalf("expected a recovery instant on %v", errExecute)
	}
	assertNearInstant(t, "EarliestRecoveryTime", rec, reset)
	if _, okPool := manager.poolEarliestRecovery(context.Background(), []string{"codex"}, model, cliproxyexecutor.Options{}); okPool {
		t.Fatal("a single-credential pool must not report a pool instant")
	}
}

// The pool instant only counts credentials that serve the model and will
// recover on their own; any usable credential means the pool is not exhausted.
func TestManager_PoolEarliestRecoveryCredentialStates(t *testing.T) {
	withQuotaCooldownEnabled(t)
	const model, otherModel = "pool-state-model", "pool-state-other"
	type extra struct {
		serves  bool
		prepare func(m *Manager, id string)
	}
	cool := func(wait time.Duration) func(*Manager, string) {
		return func(m *Manager, id string) {
			m.MarkResult(context.Background(), Result{
				AuthID: id, Provider: "codex", Model: model,
				Error: &Error{HTTPStatus: http.StatusTooManyRequests, Message: "usage limit"}, RetryAfter: &wait,
			})
		}
	}
	mutate := func(fn func(a *Auth)) func(*Manager, string) {
		return func(m *Manager, id string) {
			m.mu.Lock()
			fn(m.auths[id])
			m.mu.Unlock()
		}
	}
	soonWait, lateWait := 3*time.Hour, 44*time.Hour
	for _, tc := range []struct {
		name   string
		extras []extra
		// wantWait is the expected pool instant; zero means no pool instant.
		wantWait time.Duration
	}{
		{name: "two cooling credentials", extras: []extra{{serves: true, prepare: cool(soonWait)}, {serves: true, prepare: cool(lateWait)}}, wantWait: soonWait},
		{name: "disabled status credential is ignored", extras: []extra{
			{serves: true, prepare: cool(lateWait)},
			{serves: true, prepare: func(m *Manager, id string) {
				cool(time.Minute)(m, id)
				mutate(func(a *Auth) { a.Status = StatusDisabled })(m, id)
			}},
			{serves: true, prepare: cool(soonWait)},
		}, wantWait: soonWait},
		{name: "disabled flag credential is ignored", extras: []extra{
			{serves: true, prepare: cool(lateWait)},
			{serves: true, prepare: func(m *Manager, id string) {
				cool(time.Minute)(m, id)
				mutate(func(a *Auth) { a.Disabled = true })(m, id)
			}},
			{serves: true, prepare: cool(soonWait)},
		}, wantWait: soonWait},
		{name: "credential that does not serve the model is ignored", extras: []extra{
			{serves: true, prepare: cool(lateWait)},
			{serves: false, prepare: mutate(func(a *Auth) {
				a.Unavailable = true
				a.NextRetryAfter = time.Now().Add(time.Minute)
			})},
			{serves: true, prepare: cool(soonWait)},
		}, wantWait: soonWait},
		{name: "never-recovering credential does not set the instant", extras: []extra{
			{serves: true, prepare: cool(lateWait)},
			{serves: true, prepare: mutate(func(a *Auth) {
				a.ModelStates = map[string]*ModelState{model: {Unavailable: true}}
			})},
		}, wantWait: lateWait},
		{name: "quota window already past its reset means the pool is not exhausted", extras: []extra{
			{serves: true, prepare: cool(lateWait)},
			{serves: true, prepare: mutate(func(a *Auth) {
				past := time.Now().Add(-time.Minute)
				a.ModelStates = map[string]*ModelState{model: {Unavailable: true, NextRetryAfter: past, Quota: QuotaState{Exceeded: true, NextRecoverAt: past}}}
			})},
		}},
		{name: "only never-recovering credentials", extras: []extra{
			{serves: true, prepare: mutate(func(a *Auth) { a.ModelStates = map[string]*ModelState{model: {Unavailable: true}} })},
			{serves: true, prepare: mutate(func(a *Auth) { a.ModelStates = map[string]*ModelState{model: {Unavailable: true}} })},
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			manager := NewManager(nil, &RoundRobinSelector{}, nil)
			manager.RegisterExecutor(&mockCustomErrorExecutor{identifier: "codex"})
			start := time.Now()
			for i, ex := range tc.extras {
				id := fmt.Sprintf("pool-state-%d-%s", i, t.Name())
				served := []*registry.ModelInfo{{ID: otherModel}}
				if ex.serves {
					served = append(served, &registry.ModelInfo{ID: model})
				}
				registry.GetGlobalRegistry().RegisterClient(id, "codex", served)
				t.Cleanup(func() { registry.GetGlobalRegistry().UnregisterClient(id) })
				if _, errRegister := manager.Register(context.Background(), &Auth{ID: id, Provider: "codex", Status: StatusActive}); errRegister != nil {
					t.Fatal(errRegister)
				}
				ex.prepare(manager, id)
			}
			rec, ok := manager.poolEarliestRecovery(context.Background(), []string{"codex"}, model, cliproxyexecutor.Options{})
			if tc.wantWait == 0 {
				if ok {
					t.Fatalf("want no pool instant, got %s", rec.Format(time.RFC3339))
				}
				return
			}
			if !ok {
				t.Fatal("want a pool instant, got none")
			}
			assertNearInstant(t, "pool instant", rec, start.Add(tc.wantWait))
		})
	}
}
