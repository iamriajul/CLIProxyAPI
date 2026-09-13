package executor

import (
	"context"
	"io"
	"net/http"
	"strings"
	"testing"

	"github.com/router-for-me/CLIProxyAPI/v7/internal/config"
	cliproxyauth "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/auth"
	cliproxyexecutor "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/executor"
	sdktranslator "github.com/router-for-me/CLIProxyAPI/v7/sdk/translator"
	"github.com/tidwall/gjson"
)

type zaiRoundTripperFunc func(*http.Request) (*http.Response, error)

func (f zaiRoundTripperFunc) RoundTrip(req *http.Request) (*http.Response, error) {
	return f(req)
}

const zaiChatFixture = `{"id":"chatcmpl-1","object":"chat.completion","created":1,"model":"glm-5.3-flash","choices":[{"index":0,"message":{"role":"assistant","content":"hello"},"finish_reason":"stop"}],"usage":{"prompt_tokens":5,"completion_tokens":3,"total_tokens":8}}`

func zaiTestAuth() *cliproxyauth.Auth {
	return &cliproxyauth.Auth{
		Provider:   "zai",
		Attributes: map[string]string{},
		Metadata:   map[string]any{"access_token": "ak-123.sk-456"},
	}
}

func TestZaiPrepareRequestSendsRawKey(t *testing.T) {
	executor := NewZaiExecutor(&config.Config{})
	req, _ := http.NewRequest(http.MethodPost, "https://api.z.ai/api/coding/paas/v4/chat/completions", nil)
	if err := executor.PrepareRequest(req, zaiTestAuth()); err != nil {
		t.Fatalf("PrepareRequest err = %v", err)
	}
	// Z.AI rejects the Bearer prefix: the key goes out verbatim.
	if got := req.Header.Get("Authorization"); got != "ak-123.sk-456" {
		t.Fatalf("Authorization = %q, want raw key", got)
	}
}

func TestZaiRequestToFormatByLane(t *testing.T) {
	executor := NewZaiExecutor(&config.Config{})
	openAIReq := cliproxyexecutor.Request{Model: "glm-5.3-flash"}
	if got := executor.RequestToFormat(openAIReq, cliproxyexecutor.Options{SourceFormat: sdktranslator.FormatOpenAI}); got != sdktranslator.FormatOpenAI {
		t.Fatalf("coding lane = %v, want OpenAI", got)
	}
	claudeReq := cliproxyexecutor.Request{Model: "glm-5.3"}
	if got := executor.RequestToFormat(claudeReq, cliproxyexecutor.Options{SourceFormat: sdktranslator.FormatClaude}); got != sdktranslator.FormatClaude {
		t.Fatalf("glm lane = %v, want Claude", got)
	}
	// No Responses endpoint exists: even Responses input reports the lane format.
	respReq := cliproxyexecutor.Request{Model: "glm-5.3-flash"}
	if got := executor.RequestToFormat(respReq, cliproxyexecutor.Options{SourceFormat: sdktranslator.FormatOpenAIResponse}); got != sdktranslator.FormatOpenAI {
		t.Fatalf("responses input on coding lane = %v, want OpenAI", got)
	}
}

func TestZaiOpenAIRoundTrip(t *testing.T) {
	var upstreamURL, authHeader, upstreamModel string
	ctx := context.WithValue(context.Background(), "cliproxy.roundtripper", zaiRoundTripperFunc(func(req *http.Request) (*http.Response, error) {
		upstreamURL = req.URL.String()
		authHeader = req.Header.Get("Authorization")
		body, _ := io.ReadAll(req.Body)
		upstreamModel = gjson.GetBytes(body, "model").String()
		return &http.Response{
			StatusCode: http.StatusOK,
			Header:     http.Header{"Content-Type": []string{"application/json"}},
			Body:       io.NopCloser(strings.NewReader(zaiChatFixture)),
		}, nil
	}))

	executor := NewZaiExecutor(&config.Config{})
	resp, err := executor.Execute(ctx, zaiTestAuth(), cliproxyexecutor.Request{
		Model:   "glm-5.3-flash",
		Payload: []byte(`{"model":"glm-5.3-flash","messages":[{"role":"user","content":"hi"}]}`),
	}, cliproxyexecutor.Options{SourceFormat: sdktranslator.FormatOpenAI})
	if err != nil {
		t.Fatalf("Execute() error = %v", err)
	}
	if upstreamURL != "https://api.z.ai/api/coding/paas/v4/chat/completions" {
		t.Fatalf("upstreamURL = %q", upstreamURL)
	}
	if authHeader != "ak-123.sk-456" {
		t.Fatalf("Authorization = %q, want raw key without Bearer", authHeader)
	}
	if upstreamModel != "glm-5.3-flash" {
		t.Fatalf("upstream model = %q", upstreamModel)
	}
	if got := gjson.GetBytes(resp.Payload, "choices.0.message.content").String(); got != "hello" {
		t.Fatalf("response text = %q", got)
	}
}

func TestZaiResponsesInputTranslatesToChat(t *testing.T) {
	var upstreamURL string
	var upstreamBody []byte
	ctx := context.WithValue(context.Background(), "cliproxy.roundtripper", zaiRoundTripperFunc(func(req *http.Request) (*http.Response, error) {
		upstreamURL = req.URL.String()
		var err error
		upstreamBody, err = io.ReadAll(req.Body)
		if err != nil {
			return nil, err
		}
		return &http.Response{
			StatusCode: http.StatusOK,
			Header:     http.Header{"Content-Type": []string{"application/json"}},
			Body:       io.NopCloser(strings.NewReader(zaiChatFixture)),
		}, nil
	}))

	executor := NewZaiExecutor(&config.Config{})
	resp, err := executor.Execute(ctx, zaiTestAuth(), cliproxyexecutor.Request{
		Model:   "glm-5.3-flash",
		Payload: []byte(`{"model":"glm-5.3-flash","input":[{"type":"message","role":"user","content":[{"type":"input_text","text":"hi"}]}]}`),
	}, cliproxyexecutor.Options{SourceFormat: sdktranslator.FormatOpenAIResponse})
	if err != nil {
		t.Fatalf("Execute() error = %v", err)
	}
	if upstreamURL != "https://api.z.ai/api/coding/paas/v4/chat/completions" {
		t.Fatalf("responses input must ride chat completions (no zai responses endpoint), got %q", upstreamURL)
	}
	if got := gjson.GetBytes(upstreamBody, "messages.0.role").String(); got != "user" {
		t.Fatalf("translated role = %q (body %s)", got, upstreamBody)
	}
	if len(resp.Payload) == 0 {
		t.Fatalf("empty translated response")
	}
}
