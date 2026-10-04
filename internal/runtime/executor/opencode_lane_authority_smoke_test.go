package executor

import (
	"context"
	"io"
	"net/http"
	"strings"
	"testing"

	"github.com/router-for-me/CLIProxyAPI/v8/internal/config"
	cliproxyexecutor "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/executor"
	sdktranslator "github.com/router-for-me/CLIProxyAPI/v8/sdk/translator"
	"github.com/tidwall/gjson"
)

// End-to-end smoke test for the reported failure: the exact shape of the
// user's error (chat-completions client, Muse Spark 1.3 Contributor, OpenCode Go
// subscription). The gateway returns ModelProtocolUnsupported when a chat
// request reaches a Responses-only lane; this proves we no longer send one.
func TestSmokeReportedFailureNowRoutesToResponses(t *testing.T) {
	var gotURL string
	var gotBody []byte
	var gotSession string
	ctx := context.WithValue(context.Background(), "cliproxy.roundtripper", opencodeRoundTripperFunc(func(req *http.Request) (*http.Response, error) {
		gotURL = req.URL.String()
		gotSession = req.Header.Get("x-opencode-session")
		var err error
		gotBody, err = io.ReadAll(req.Body)
		if err != nil {
			return nil, err
		}
		// The gateway answers the Responses wire; a chat request would 400 with
		// ModelProtocolUnsupported.
		return &http.Response{
			StatusCode: http.StatusOK,
			Header:     http.Header{"Content-Type": {"application/json"}},
			Body: io.NopCloser(strings.NewReader(
				`{"id":"resp_1","object":"response","created_at":1,"status":"completed","model":"muse-spark-1.3-contributor","output":[{"type":"message","role":"assistant","content":[{"type":"output_text","text":"hi"}]}],"usage":{"input_tokens":5,"output_tokens":3,"total_tokens":8}}`)),
		}, nil
	}))

	executor := NewOpenCodeExecutor(&config.Config{})
	resp, err := executor.Execute(ctx, opencodeTestAuth(), cliproxyexecutor.Request{
		Model:   "muse-spark-1.3-contributor",
		Payload: []byte(`{"model":"muse-spark-1.3-contributor","messages":[{"role":"user","content":"review this PR"}],"stream":false}`),
	}, cliproxyexecutor.Options{SourceFormat: sdktranslator.FormatOpenAI})
	if err != nil {
		t.Fatalf("Execute() error = %v", err)
	}

	// 1. Sent to the wire the gateway accepts for this model.
	if gotURL != "https://opencode.ai/zen/go/v1/responses" {
		t.Fatalf("wire = %q, want /responses (chat triggers ModelProtocolUnsupported)", gotURL)
	}
	// 2. Body is genuinely Responses-shaped, not an untranslated chat payload.
	if !gjson.GetBytes(gotBody, "input").IsArray() {
		t.Fatalf("body was not translated to Responses: %s", gotBody)
	}
	if got := gjson.GetBytes(gotBody, "input.0.role").String(); got != "user" {
		t.Fatalf("translated input lost the user turn: %s", gotBody)
	}
	// 3. The caller's message actually survived the translation.
	if !strings.Contains(string(gotBody), "review this PR") {
		t.Fatalf("caller content lost in translation: %s", gotBody)
	}
	// 4. Model id preserved on the wire.
	if got := gjson.GetBytes(gotBody, "model").String(); got != "muse-spark-1.3-contributor" {
		t.Fatalf("wire model = %q", got)
	}
	// 5. Session header set: prompt-cache alignment depends on it, so this is
	// asserted rather than logged.
	if gotSession == "" {
		t.Fatal("x-opencode-session header missing; prompt-cache alignment would break")
	}
	// 6. Response reaches the caller.
	if !gjson.GetBytes(resp.Payload, "output").Exists() {
		t.Fatalf("response not translated back to the caller: %s", resp.Payload)
	}
}
func TestSmokeEncryptedReasoningIsStripped(t *testing.T) {
	// A prior turn returned an encrypted reasoning item bound to the gateway's
	// own caller; replaying it verbatim is exactly what #11928 rejects.
	replay := `{"model":"muse-spark-1.3-contributor","input":[{"type":"reasoning","id":"rs_1","summary":[],"encrypted_content":"not-issued-to-this-caller"},{"type":"message","role":"user","content":[{"type":"input_text","text":"next step"}]}]}`
	if !gjson.GetBytes([]byte(replay), "input.0.encrypted_content").Exists() {
		t.Fatal("sanity: fixture must carry encrypted_content")
	}

	var gotBody []byte
	ctx := context.WithValue(context.Background(), "cliproxy.roundtripper", opencodeRoundTripperFunc(func(req *http.Request) (*http.Response, error) {
		var err error
		gotBody, err = io.ReadAll(req.Body)
		if err != nil {
			return nil, err
		}
		return &http.Response{
			StatusCode: http.StatusOK,
			Header:     http.Header{"Content-Type": {"application/json"}},
			Body: io.NopCloser(strings.NewReader(
				`{"id":"resp_1","object":"response","status":"completed","model":"muse-spark-1.3-contributor","output":[],"usage":{"input_tokens":1,"output_tokens":1,"total_tokens":2}}`)),
		}, nil
	}))

	executor := NewOpenCodeExecutor(&config.Config{})
	if _, err := executor.Execute(ctx, opencodeTestAuth(), cliproxyexecutor.Request{
		Model:   "muse-spark-1.3-contributor",
		Payload: []byte(replay),
	}, cliproxyexecutor.Options{SourceFormat: sdktranslator.FormatOpenAIResponse}); err != nil {
		t.Fatalf("Execute() error = %v", err)
	}
	if strings.Contains(string(gotBody), "not-issued-to-this-caller") {
		t.Fatalf("replayed encrypted_content survived (#11928 guard missing): %s", gotBody)
	}
	if !strings.Contains(string(gotBody), "next step") {
		t.Fatalf("guard dropped the caller turn: %s", gotBody)
	}
	t.Logf("replay body: %s", gotBody)
}
