package api

import (
	"encoding/json"
	"math"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/router-for-me/CLIProxyAPI/v8/internal/tps"
)

func newTPSTestServer(t *testing.T) *Server {
	t.Helper()
	// The tracker is process-wide, so each case starts from a clean slate.
	tps.Default().Reset()
	t.Cleanup(tps.Default().Reset)
	return newTestServer(t)
}

// recordTPSRequest publishes one generation into the process-wide tracker.
func recordTPSRequest(t *testing.T, model, alias string, at time.Time, latency, ttft time.Duration, input, output int64) {
	t.Helper()
	tps.Default().Record(tps.Sample{
		ObservedAt:   at,
		Model:        model,
		Alias:        alias,
		Provider:     "codex",
		Latency:      latency,
		TTFT:         ttft,
		InputTokens:  input,
		OutputTokens: output,
		Stream:       true,
	})
}

func getTPS(t *testing.T, server *Server, target, apiKey string) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(http.MethodGet, target, nil)
	if apiKey != "" {
		req.Header.Set("Authorization", "Bearer "+apiKey)
	}
	rec := httptest.NewRecorder()
	server.engine.ServeHTTP(rec, req)
	return rec
}

// decodeTPS reads a 200 body into the flat response struct.
func decodeTPS(t *testing.T, rec *httptest.ResponseRecorder) lastRequestTPSResponse {
	t.Helper()
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200, body = %s", rec.Code, rec.Body.String())
	}
	var payload lastRequestTPSResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &payload); err != nil {
		t.Fatalf("failed to decode %s: %v", rec.Body.String(), err)
	}
	return payload
}

// requireTPSError locks the uniform {"error": string} envelope and status.
func requireTPSError(t *testing.T, rec *httptest.ResponseRecorder, wantStatus int) {
	t.Helper()
	if rec.Code != wantStatus {
		t.Fatalf("status = %d, want %d, body = %s", rec.Code, wantStatus, rec.Body.String())
	}
	var payload map[string]string
	if err := json.Unmarshal(rec.Body.Bytes(), &payload); err != nil {
		t.Fatalf("failed to decode error body %s: %v", rec.Body.String(), err)
	}
	if payload["error"] == "" {
		t.Fatalf("body = %s, want a non-empty error message", rec.Body.String())
	}
	if len(payload) != 1 {
		t.Fatalf("body = %s, want only the error key", rec.Body.String())
	}
}

// TestHandleLastRequestTPS_Contract locks the flat wire shape a GUI polls: one
// object at the top level, no nesting, and the exact field types it renders.
func TestHandleLastRequestTPS_Contract(t *testing.T) {
	server := newTPSTestServer(t)
	recordTPSRequest(t, "gpt-5.6", "", time.Now().Add(-time.Minute), 4*time.Second, time.Second, 120, 300)

	got := decodeTPS(t, getTPS(t, server, "/v1/last-request-tps?model=gpt-5.6", "test-key"))
	if got.Model != "gpt-5.6" || got.Provider != "codex" || !got.Stream {
		t.Fatalf("body = %+v, want the recorded request", got)
	}
	if got.DurationMs != 4000 || got.TTFTMs != 1000 || got.GenerationMs != 3000 {
		t.Fatalf("timings = %d/%d/%d ms, want 4000/1000/3000", got.DurationMs, got.TTFTMs, got.GenerationMs)
	}
	if got.InputTokens != 120 || got.OutputTokens != 300 {
		t.Fatalf("tokens = %d/%d, want 120/300", got.InputTokens, got.OutputTokens)
	}
	// 300 output tokens over 3s of generation.
	if math.Abs(got.TPS-100) > 1e-9 {
		t.Fatalf("tps = %v, want 100", got.TPS)
	}
	if got.At.IsZero() {
		t.Fatal("at is zero, want the request completion time")
	}
}

// TestHandleLastRequestTPS_BodyIsFlat locks the "lean, no nesting" contract at
// the wire level: the documented keys are the top-level keys, and no
// wrapper object survives.
func TestHandleLastRequestTPS_BodyIsFlat(t *testing.T) {
	server := newTPSTestServer(t)
	recordTPSRequest(t, "flat-model", "", time.Now(), 2*time.Second, 0, 5, 200)

	rec := getTPS(t, server, "/v1/last-request-tps?model=flat-model", "test-key")
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", rec.Code, rec.Body.String())
	}
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(rec.Body.Bytes(), &fields); err != nil {
		t.Fatalf("failed to decode: %v", err)
	}
	for _, removed := range []string{"last_request", "summary", "window", "generated_at"} {
		if _, present := fields[removed]; present {
			t.Fatalf("body still has %q, want a flat object: %s", removed, rec.Body.String())
		}
	}
	if len(fields) == 0 {
		t.Fatal("body has no fields")
	}
	// tps must be a top-level number, not an object.
	var tpsValue float64
	if err := json.Unmarshal(fields["tps"], &tpsValue); err != nil {
		t.Fatalf("tps is not a top-level number: %s", rec.Body.String())
	}
	if math.Abs(tpsValue-100) > 1e-9 {
		t.Fatalf("tps = %v, want 100", tpsValue)
	}
}

// TestHandleLastRequestTPS_NotFoundWhenNoRequest covers the idle state: no
// request served for the model answers 404, with no 200 body to misread.
func TestHandleLastRequestTPS_NotFoundWhenNoRequest(t *testing.T) {
	server := newTPSTestServer(t)
	requireTPSError(t, getTPS(t, server, "/v1/last-request-tps?model=never-called", "test-key"), http.StatusNotFound)
}

// TestHandleLastRequestTPS_RequiresInferenceKey locks the auth envelope shared
// with the rest of the v1 group.
func TestHandleLastRequestTPS_RequiresInferenceKey(t *testing.T) {
	server := newTPSTestServer(t)
	requireTPSError(t, getTPS(t, server, "/v1/last-request-tps", ""), http.StatusUnauthorized)
}

// TestHandleLastRequestTPS_ReportsNewestRequest locks the core behavior the
// name promises: the reading tracks the most recent request, not the first one
// served.
func TestHandleLastRequestTPS_ReportsNewestRequest(t *testing.T) {
	server := newTPSTestServer(t)
	now := time.Now()
	recordTPSRequest(t, "moving-model", "", now.Add(-2*time.Minute), 10*time.Second, 0, 0, 100)
	recordTPSRequest(t, "moving-model", "", now.Add(-time.Minute), 2*time.Second, 0, 0, 200)

	got := decodeTPS(t, getTPS(t, server, "/v1/last-request-tps?model=moving-model", "test-key"))
	if got.OutputTokens != 200 || got.DurationMs != 2000 {
		t.Fatalf("body = %+v, want the newest (200-token) request", got)
	}
	if math.Abs(got.TPS-100) > 1e-9 {
		t.Fatalf("tps = %v, want 100", got.TPS)
	}
}

// TestHandleLastRequestTPS_MatchesAlias covers how a client names its selected
// model: the upstream model id, or the alias it actually requested.
func TestHandleLastRequestTPS_MatchesAlias(t *testing.T) {
	server := newTPSTestServer(t)
	recordTPSRequest(t, "claude-sonnet-4-5", "fast", time.Now(), 2*time.Second, 0, 0, 200)

	for _, model := range []string{"fast", "claude-sonnet-4-5"} {
		got := decodeTPS(t, getTPS(t, server, "/v1/last-request-tps?model="+model, "test-key"))
		if got.Model != "claude-sonnet-4-5" || got.Alias != "fast" {
			t.Fatalf("model %q: body = %+v, want the aliased request", model, got)
		}
	}
}

// TestHandleLastRequestTPS_WithoutModelSpansEveryModel keeps the unfiltered
// reading useful for a dashboard showing all models at once.
func TestHandleLastRequestTPS_WithoutModelSpansEveryModel(t *testing.T) {
	server := newTPSTestServer(t)
	now := time.Now()
	recordTPSRequest(t, "model-a", "", now.Add(-time.Minute), 2*time.Second, 0, 0, 100)
	recordTPSRequest(t, "model-b", "", now, 2*time.Second, 0, 0, 300)

	got := decodeTPS(t, getTPS(t, server, "/v1/last-request-tps", "test-key"))
	if got.Model != "model-b" {
		t.Fatalf("body = %+v, want the newest request across models", got)
	}
}

// TestHandleLastRequestTPS_StripsThinkingSuffix keeps parity with the quota
// endpoint: a selected model carrying a thinking suffix still resolves.
func TestHandleLastRequestTPS_StripsThinkingSuffix(t *testing.T) {
	server := newTPSTestServer(t)
	recordTPSRequest(t, "gemini-3-pro", "", time.Now(), 2*time.Second, 0, 0, 100)

	got := decodeTPS(t, getTPS(t, server, "/v1/last-request-tps?model=gemini-3-pro(high)", "test-key"))
	if got.Model != "gemini-3-pro" {
		t.Fatalf("body = %+v, want the suffix stripped", got.Model)
	}
}

// TestHandleLastRequestTPS_UnknownModelIs404NotOtherModel guards a subtle
// failure: an unmatched model must not fall back to some other model's
// reading, which would silently show a wrong TPS to the user.
func TestHandleLastRequestTPS_UnknownModelIs404NotOtherModel(t *testing.T) {
	server := newTPSTestServer(t)
	recordTPSRequest(t, "known-model", "", time.Now(), 2*time.Second, 0, 0, 300)

	requireTPSError(t, getTPS(t, server, "/v1/last-request-tps?model=other-model", "test-key"), http.StatusNotFound)
}

// TestLastRequestTPSSchemaParity guards the published contract
// (api/v1-last-request-tps.schema.json) against struct drift in either
// direction: every JSON field must be documented, and every documented
// required field must exist.
func TestLastRequestTPSSchemaParity(t *testing.T) {
	schemaPath := filepath.Join("..", "..", "api", "v1-last-request-tps.schema.json")
	raw, err := os.ReadFile(schemaPath)
	if err != nil {
		t.Fatalf("failed to read schema: %v", err)
	}
	var schema struct {
		Type       string         `json:"type"`
		Required   []string       `json:"required"`
		Properties map[string]any `json:"properties"`
	}
	if err := json.Unmarshal(raw, &schema); err != nil {
		t.Fatalf("schema is not valid JSON: %v", err)
	}
	if schema.Type != "object" {
		t.Fatalf("schema root type = %q, want object", schema.Type)
	}

	wantRequired, wantFields := jsonTagSets(reflect.TypeOf(lastRequestTPSResponse{}))
	if !equalStringSets(schema.Required, wantRequired) {
		t.Fatalf("required = %v, want %v", schema.Required, wantRequired)
	}
	var gotFields []string
	for field := range schema.Properties {
		gotFields = append(gotFields, field)
	}
	if !equalStringSets(gotFields, wantFields) {
		t.Fatalf("properties = %v, want %v", gotFields, wantFields)
	}
}

// TestHandleLastRequestTPS_NotFoundWithoutModel covers the unfiltered 404: the
// message must not read "for model " with a trailing blank model name.
func TestHandleLastRequestTPS_NotFoundWithoutModel(t *testing.T) {
	server := newTPSTestServer(t)
	rec := getTPS(t, server, "/v1/last-request-tps", "test-key")
	requireTPSError(t, rec, http.StatusNotFound)
	var payload map[string]string
	if err := json.Unmarshal(rec.Body.Bytes(), &payload); err != nil {
		t.Fatalf("failed to decode: %v", err)
	}
	if strings.Contains(payload["error"], "for model ") {
		t.Fatalf("error = %q, want no dangling model name", payload["error"])
	}
}
