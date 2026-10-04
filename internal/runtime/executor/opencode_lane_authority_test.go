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

const museResponsesFixture = `{"id":"resp_1","object":"response","created_at":1,"status":"completed","model":"muse-spark-1.3-contributor","output":[{"type":"message","role":"assistant","content":[{"type":"output_text","text":"hello"}]}],"usage":{"input_tokens":5,"output_tokens":3,"total_tokens":8}}`

// Regression for the reported ModelProtocolUnsupported: a chat-completions
// caller asking for a Responses-only Muse lane. The lane decides the wire, so
// the request must be translated up to /responses instead of being sent to
// /chat/completions, which the gateway rejects with ModelProtocolUnsupported.
func TestOpencodeChatSourceOnResponsesLaneRidesResponses(t *testing.T) {
	for _, tc := range []struct {
		name   string
		model  string
		source sdktranslator.Format
	}{
		{"chat completions source", "muse-spark-1.3-contributor", sdktranslator.FormatOpenAI},
		{"claude source", "muse-spark-1.3-contributor", sdktranslator.FormatClaude},
		{"non-contributor lane", "muse-spark-1.3", sdktranslator.FormatOpenAI},
		{"unreleased revision", "muse-spark-1.4-contributor", sdktranslator.FormatOpenAI},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var gotURL string
			var gotBody []byte
			ctx := context.WithValue(context.Background(), "cliproxy.roundtripper", opencodeRoundTripperFunc(func(req *http.Request) (*http.Response, error) {
				gotURL = req.URL.String()
				var err error
				gotBody, err = io.ReadAll(req.Body)
				if err != nil {
					return nil, err
				}
				return &http.Response{
					StatusCode: http.StatusOK,
					Header:     http.Header{"Content-Type": {"application/json"}},
					Body:       io.NopCloser(strings.NewReader(museResponsesFixture)),
				}, nil
			}))

			payload := []byte(`{"model":"` + tc.model + `","messages":[{"role":"user","content":"hi"}]}`)
			executor := NewOpenCodeExecutor(&config.Config{})
			if _, err := executor.Execute(ctx, opencodeTestAuth(), cliproxyexecutor.Request{
				Model:   tc.model,
				Payload: payload,
			}, cliproxyexecutor.Options{SourceFormat: tc.source}); err != nil {
				t.Fatalf("Execute() error = %v", err)
			}
			if gotURL != "https://opencode.ai/zen/go/v1/responses" {
				t.Fatalf("responses-native lane must ride /responses, got %q", gotURL)
			}
			// The translated body must be Responses-shaped and carry the model.
			if !gjson.GetBytes(gotBody, "input").IsArray() {
				t.Fatalf("translated body is not Responses-shaped: %s", gotBody)
			}
			if got := gjson.GetBytes(gotBody, "model").String(); got != tc.model {
				t.Fatalf("upstream model = %q, want %q (body %s)", got, tc.model, gotBody)
			}
		})
	}
}

// Streaming takes the same lane-authoritative path.
func TestOpencodeChatSourceOnResponsesLaneStreamsFromResponses(t *testing.T) {
	var gotURL string
	ctx := context.WithValue(context.Background(), "cliproxy.roundtripper", opencodeRoundTripperFunc(func(req *http.Request) (*http.Response, error) {
		gotURL = req.URL.String()
		return &http.Response{
			StatusCode: http.StatusOK,
			Header:     http.Header{"Content-Type": {"text/event-stream"}},
			Body:       io.NopCloser(strings.NewReader("data: {\"type\":\"response.completed\",\"response\":{\"id\":\"resp_1\",\"status\":\"completed\"}}\n\ndata: [DONE]\n\n")),
		}, nil
	}))
	executor := NewOpenCodeExecutor(&config.Config{})
	result, err := executor.ExecuteStream(ctx, opencodeTestAuth(), cliproxyexecutor.Request{
		Model:   "muse-spark-1.3-contributor",
		Payload: []byte(`{"model":"muse-spark-1.3-contributor","messages":[{"role":"user","content":"hi"}]}`),
	}, cliproxyexecutor.Options{SourceFormat: sdktranslator.FormatOpenAI})
	if err != nil {
		t.Fatalf("ExecuteStream() error = %v", err)
	}
	for range result.Chunks {
	}
	if gotURL != "https://opencode.ai/zen/go/v1/responses" {
		t.Fatalf("responses-native lane must stream from /responses, got %q", gotURL)
	}
}

// Regression: ExecuteStream lost its anthropic delegation when the Responses
// gate was made lane-authoritative, so an anthropic lane driven by a Claude
// caller would silently stream over /chat/completions instead of /messages.
// The anthropic branch must stay ahead of the Responses check, in streaming as
// in the non-streaming path, or the two drift apart.
func TestOpencodeAnthropicLaneKeepsClaudeWire(t *testing.T) {
	for _, model := range []string{"qwen3.8-flash", "minimax-m2.5", "union-alpha"} {
		streamURL, streamErr := anthropicWireURL(model, true)
		if streamErr != nil {
			t.Fatalf("%s: ExecuteStream() error = %v", model, streamErr)
		}
		if !strings.Contains(streamURL, "/messages") {
			t.Fatalf("%s: streaming anthropic lane used %q, want /messages", model, streamURL)
		}
		nonStreamURL, nonStreamErr := anthropicWireURL(model, false)
		if nonStreamErr != nil {
			t.Fatalf("%s: Execute() error = %v", model, nonStreamErr)
		}
		if !strings.Contains(nonStreamURL, "/messages") {
			t.Fatalf("%s: non-streaming anthropic lane used %q, want /messages", model, nonStreamURL)
		}
		if streamURL != nonStreamURL {
			t.Fatalf("%s: streaming used %q but non-streaming used %q; the paths must agree", model, streamURL, nonStreamURL)
		}
	}
}

// anthropicWireURL runs one anthropic-lane request and reports the upstream
// wire it selected.
func anthropicWireURL(model string, stream bool) (string, error) {
	var gotURL string
	ctx := context.WithValue(context.Background(), "cliproxy.roundtripper", opencodeRoundTripperFunc(func(req *http.Request) (*http.Response, error) {
		gotURL = req.URL.String()
		if stream {
			return &http.Response{
				StatusCode: http.StatusOK,
				Header:     http.Header{"Content-Type": {"text/event-stream"}},
				Body:       io.NopCloser(strings.NewReader("event: message_start\ndata: {}\n\n")),
			}, nil
		}
		return &http.Response{
			StatusCode: http.StatusOK,
			Header:     http.Header{"Content-Type": {"application/json"}},
			Body:       io.NopCloser(strings.NewReader(`{"id":"msg_1","type":"message","role":"assistant","content":[{"type":"text","text":"hi"}]}`)),
		}, nil
	}))
	executor := NewOpenCodeExecutor(&config.Config{})
	req := cliproxyexecutor.Request{
		Model:   model,
		Payload: []byte(`{"model":"` + model + `","messages":[{"role":"user","content":"hi"}]}`),
	}
	opts := cliproxyexecutor.Options{SourceFormat: sdktranslator.FormatClaude}
	var err error
	if stream {
		var result *cliproxyexecutor.StreamResult
		result, err = executor.ExecuteStream(ctx, opencodeTestAuth(), req, opts)
		if err == nil {
			for range result.Chunks {
			}
		}
	} else {
		_, err = executor.Execute(ctx, opencodeTestAuth(), req, opts)
	}
	return gotURL, err
}

// No regression: chat-route lanes keep /chat/completions for every source.
func TestOpencodeChatRouteLaneStillRidesChat(t *testing.T) {
	for _, source := range []sdktranslator.Format{sdktranslator.FormatOpenAI, sdktranslator.FormatOpenAIResponse} {
		var gotURL string
		ctx := context.WithValue(context.Background(), "cliproxy.roundtripper", opencodeRoundTripperFunc(func(req *http.Request) (*http.Response, error) {
			gotURL = req.URL.String()
			return &http.Response{
				StatusCode: http.StatusOK,
				Header:     http.Header{"Content-Type": {"application/json"}},
				Body:       io.NopCloser(strings.NewReader(opencodeChatFixture)),
			}, nil
		}))
		executor := NewOpenCodeExecutor(&config.Config{})
		if _, err := executor.Execute(ctx, opencodeTestAuth(), cliproxyexecutor.Request{
			Model:   "mimo-v2.6-flash",
			Payload: []byte(`{"model":"mimo-v2.6-flash","input":[{"type":"message","role":"user","content":[{"type":"input_text","text":"hi"}]}]}`),
		}, cliproxyexecutor.Options{SourceFormat: source}); err != nil {
			t.Fatalf("Execute() error = %v", err)
		}
		if gotURL != "https://opencode.ai/zen/go/v1/chat/completions" {
			t.Fatalf("chat-route lane must ride /chat/completions, got %q", gotURL)
		}
	}
}

// The lane's protocol is authoritative in RequestToFormat too, so the
// conductor's format negotiation agrees with the executor's URL choice.
func TestOpencodeRequestToFormatIsLaneAuthoritative(t *testing.T) {
	executor := NewOpenCodeExecutor(&config.Config{})
	for _, tc := range []struct {
		model string
		want  sdktranslator.Format
	}{
		{"muse-spark-1.3-contributor", sdktranslator.FormatCodex},
		{"muse-spark-1.4", sdktranslator.FormatCodex},
		{"qwen3.8-flash", sdktranslator.FormatClaude},
		{"minimax-m3", sdktranslator.FormatOpenAI},
		{"glm-5.2", sdktranslator.FormatOpenAI},
	} {
		for _, source := range []sdktranslator.Format{sdktranslator.FormatOpenAI, sdktranslator.FormatClaude, sdktranslator.FormatOpenAIResponse} {
			got := executor.RequestToFormat(cliproxyexecutor.Request{Model: tc.model}, cliproxyexecutor.Options{SourceFormat: source})
			if got != tc.want {
				t.Errorf("RequestToFormat(%q, source=%s) = %s, want %s", tc.model, source, got, tc.want)
			}
		}
	}
}
