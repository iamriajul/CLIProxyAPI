package executor

import (
	"context"
	"io"
	"net/http"
	"strings"
	"testing"

	"github.com/router-for-me/CLIProxyAPI/v7/internal/config"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/runtime/executor/helps"
	cliproxyauth "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/auth"
	cliproxyexecutor "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/executor"
	sdktranslator "github.com/router-for-me/CLIProxyAPI/v7/sdk/translator"
	"github.com/tidwall/gjson"
)

// Claude Code's auto mode asks the server to review an action as part of the
// session's own model requests. That travels as the `safeguards` request field
// plus the dangerous-tool-use beta, and the verdict comes back as
// `safeguard_results`. Dropping either half makes the client fall back to its
// own billable classifier requests, or deny every action it reviewed.
//
// https://code.claude.com/docs/en/auto-mode-classifier-billing
const (
	autoModeClassifierUserID = `{\"device_id\":\"0000000000000000000000000000000000000000000000000000000000000000\",\"account_uuid\":\"aaaaaaaa-aaaa-4aaa-8aaa-aaaaaaaaaaaa\",\"session_id\":\"11111111-2222-4333-8444-555555555555\"}`

	autoModeClassifierRequest = `{"model":"claude-opus-5","max_tokens":1024,"metadata":{"user_id":"` + autoModeClassifierUserID + `"},"system":[{"type":"text","text":"x-anthropic-billing-header: cc_version=2.1.283.abc; cc_entrypoint=cli; cch=00000;"}],"messages":[{"role":"user","content":[{"type":"text","text":"<transcript>user: run ls</transcript>"}]}],"safeguards":[{"type":"bash","command":"ls -la"}],"tools":[],"stream":true}`
)

// autoModeClassifierSSE is the upstream stream shape: the verdict rides on the
// message_delta event, ahead of message_stop.
const autoModeClassifierSSE = "event: message_start\n" +
	"data: {\"type\":\"message_start\",\"message\":{\"id\":\"msg_classifier\",\"type\":\"message\",\"model\":\"claude-opus-5\",\"role\":\"assistant\",\"content\":[],\"usage\":{\"input_tokens\":1,\"output_tokens\":0}}}\n\n" +
	"event: message_delta\n" +
	"data: {\"type\":\"message_delta\",\"delta\":{\"stop_reason\":\"end_turn\"},\"usage\":{\"output_tokens\":1},\"safeguard_results\":[{\"tool\":\"bash\",\"verdict\":\"allow\"}]}\n\n" +
	"event: message_stop\n" +
	"data: {\"type\":\"message_stop\"}\n\n"

// autoModeClassifierHeaders is a confirmed native Claude Code caller, which is
// the case the server-side review path depends on: the client trusts the
// gateway and sends the full Anthropic request shape.
func autoModeClassifierHeaders() http.Header {
	return http.Header{
		"User-Agent":                  {"claude-cli/2.1.280 (external, cli)"},
		"X-App":                       {"cli"},
		"Anthropic-Beta":              {"claude-code-20250219,oauth-2025-04-20,dangerous-tool-use-2026-09-03"},
		"X-Claude-Code-Request-Class": {"auxiliary"},
		"X-Claude-Code-Session-Id":    {"11111111-2222-4333-8444-555555555555"},
	}
}

func autoModeClassifierAuth(baseURL string) *cliproxyauth.Auth {
	auth := directClaudeOAuthAuth()
	auth.Attributes["base_url"] = baseURL
	return auth
}

func autoModeClassifierSSETransport(t *testing.T, seenBody *[]byte, seenHeaders *http.Header) http.RoundTripper {
	t.Helper()
	return roundTripperFunc(func(req *http.Request) (*http.Response, error) {
		body, errRead := io.ReadAll(req.Body)
		if errRead != nil {
			t.Fatal(errRead)
		}
		*seenBody = body
		*seenHeaders = req.Header.Clone()
		return &http.Response{
			StatusCode: http.StatusOK,
			Header:     http.Header{"Content-Type": {"text/event-stream"}},
			Body:       io.NopCloser(strings.NewReader(autoModeClassifierSSE)),
			Request:    req,
		}, nil
	})
}

// An Anthropic upstream must receive the review request intact and the verdict
// must reach Claude Code. Dropping the field makes the server skip its checks,
// which is what bills the fallback classifier requests in the notice.
func TestClaudeExecutor_AutoModeClassifierPassesThroughAnthropicUpstream(t *testing.T) {
	var seenBody []byte
	var seenHeaders http.Header
	ctx := context.WithValue(context.Background(), "cliproxy.roundtripper",
		autoModeClassifierSSETransport(t, &seenBody, &seenHeaders))

	headers := autoModeClassifierHeaders()
	payload := []byte(autoModeClassifierRequest)
	if !helps.DetectClaudeCodeRequest(headers, payload, false).Confirmed {
		t.Fatal("fixture must be a confirmed native Claude Code request")
	}

	result, err := NewClaudeExecutor(&config.Config{}).ExecuteStream(ctx,
		autoModeClassifierAuth("https://api.anthropic.com"),
		cliproxyexecutor.Request{Model: "claude-opus-5", Payload: payload},
		cliproxyexecutor.Options{SourceFormat: sdktranslator.FormatClaude, Headers: headers, OriginalRequest: payload})
	if err != nil {
		t.Fatalf("ExecuteStream() error = %v", err)
	}
	var downstream strings.Builder
	for chunk := range result.Chunks {
		if chunk.Err != nil {
			t.Fatalf("chunk error = %v", chunk.Err)
		}
		downstream.Write(chunk.Payload)
	}

	if got := gjson.GetBytes(seenBody, "safeguards").Raw; got != `[{"type":"bash","command":"ls -la"}]` {
		t.Errorf("upstream safeguards = %q, want the caller's review request unchanged; body: %s", got, seenBody)
	}
	if betas := helps.HeaderValueCaseInsensitive(seenHeaders, "Anthropic-Beta"); !strings.Contains(betas, "dangerous-tool-use-2026-09-03") {
		t.Errorf("Anthropic-Beta = %q, want dangerous-tool-use-2026-09-03", betas)
	}
	if !strings.Contains(downstream.String(), "safeguard_results") {
		t.Errorf("downstream stream dropped safeguard_results: %s", downstream.String())
	}
}

// x-claude-code-request-class is how Claude Code marks an auxiliary request, and
// prompt-id groups every request serving one prompt. Anthropic keys review
// rollout off these, so a confirmed caller keeps them. The gateway hint set
// grows with Claude Code releases, so this pins the whole documented list
// rather than the one field the classifier happens to use today.
func TestClaudeExecutor_AutoModeClassifierForwardsGatewayHintHeaders(t *testing.T) {
	var seenBody []byte
	var seenHeaders http.Header
	ctx := context.WithValue(context.Background(), "cliproxy.roundtripper",
		autoModeClassifierSSETransport(t, &seenBody, &seenHeaders))

	headers := autoModeClassifierHeaders()
	headers.Set("X-Claude-Code-Prompt-Id", "99999999-2222-4333-8444-555555555555")
	payload := []byte(autoModeClassifierRequest)

	result, err := NewClaudeExecutor(&config.Config{}).ExecuteStream(ctx,
		autoModeClassifierAuth("https://api.anthropic.com"),
		cliproxyexecutor.Request{Model: "claude-opus-5", Payload: payload},
		cliproxyexecutor.Options{SourceFormat: sdktranslator.FormatClaude, Headers: headers, OriginalRequest: payload})
	if err != nil {
		t.Fatalf("ExecuteStream() error = %v", err)
	}
	for chunk := range result.Chunks {
		if chunk.Err != nil {
			t.Fatalf("chunk error = %v", chunk.Err)
		}
	}

	for _, header := range []string{
		"X-Claude-Code-Request-Class",
		"X-Claude-Code-Prompt-Id",
	} {
		if got := helps.HeaderValueCaseInsensitive(seenHeaders, header); got != headers.Get(header) {
			t.Errorf("%s = %q, want %q", header, got, headers.Get(header))
		}
	}
}

// An unconfirmed caller must not be able to spoof its request class, since
// Anthropic reads these as Claude Code's own software state.
func TestClaudeExecutor_AutoModeClassifierUnconfirmedCallerKeepsNoGatewayHints(t *testing.T) {
	var seenBody []byte
	var seenHeaders http.Header
	ctx := context.WithValue(context.Background(), "cliproxy.roundtripper",
		autoModeClassifierSSETransport(t, &seenBody, &seenHeaders))

	payload := []byte(autoModeClassifierRequest)
	if _, err := NewClaudeExecutor(&config.Config{}).ExecuteStream(ctx,
		autoModeClassifierAuth("https://api.anthropic.com"),
		cliproxyexecutor.Request{Model: "claude-opus-5", Payload: payload},
		cliproxyexecutor.Options{SourceFormat: sdktranslator.FormatClaude, Headers: http.Header{
			"Anthropic-Beta":              {"dangerous-tool-use-2026-09-03"},
			"X-Claude-Code-Request-Class": {"auxiliary"},
		}, OriginalRequest: payload}); err != nil {
		t.Fatalf("ExecuteStream() error = %v", err)
	}

	if got := helps.HeaderValueCaseInsensitive(seenHeaders, "X-Claude-Code-Request-Class"); got != "" {
		t.Errorf("unconfirmed caller forwarded X-Claude-Code-Request-Class = %q", got)
	}
}

// A Claude-compatible upstream that is not Anthropic has no server-side
// classifier and rejects unknown top-level fields, so `safeguards` would turn
// every auxiliary request into a hard 400.
func TestClaudeExecutor_AutoModeClassifierStripsSafeguardsForNonAnthropicUpstream(t *testing.T) {
	var seenBody []byte
	var seenHeaders http.Header
	ctx := context.WithValue(context.Background(), "cliproxy.roundtripper",
		autoModeClassifierSSETransport(t, &seenBody, &seenHeaders))

	headers := autoModeClassifierHeaders()
	payload := []byte(autoModeClassifierRequest)
	auth := &cliproxyauth.Auth{
		Provider:   "kimi",
		Attributes: map[string]string{"base_url": "https://api.kimi.com/coding"},
	}

	result, err := NewClaudeExecutor(&config.Config{}).ExecuteStream(ctx, auth,
		cliproxyexecutor.Request{Model: "kimi-k2.5", Payload: payload},
		cliproxyexecutor.Options{SourceFormat: sdktranslator.FormatClaude, Headers: headers, OriginalRequest: payload})
	if err != nil {
		t.Fatalf("ExecuteStream() error = %v", err)
	}
	for chunk := range result.Chunks {
		if chunk.Err != nil {
			t.Fatalf("chunk error = %v", chunk.Err)
		}
	}

	if gjson.GetBytes(seenBody, "safeguards").Exists() {
		t.Errorf("non-Anthropic upstream received Anthropic-only safeguards: %s", seenBody)
	}
	if got := gjson.GetBytes(seenBody, "messages.0.content.0.text").String(); got != "<transcript>user: run ls</transcript>" {
		t.Errorf("non-Anthropic upstream lost the caller's action payload: %q", got)
	}
}
