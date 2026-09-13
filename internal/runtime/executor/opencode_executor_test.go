package executor

import (
	"context"
	"io"
	"net/http"
	"strings"
	"testing"

	"github.com/router-for-me/CLIProxyAPI/v7/internal/config"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/registry"
	cliproxyauth "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/auth"
	cliproxyexecutor "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/executor"
	sdktranslator "github.com/router-for-me/CLIProxyAPI/v7/sdk/translator"
	"github.com/tidwall/gjson"
)

type opencodeRoundTripperFunc func(*http.Request) (*http.Response, error)

func (f opencodeRoundTripperFunc) RoundTrip(req *http.Request) (*http.Response, error) {
	return f(req)
}

const opencodeChatFixture = `{"id":"chatcmpl-1","object":"chat.completion","created":1,"model":"glm-5.2","choices":[{"index":0,"message":{"role":"assistant","content":"hello"},"finish_reason":"stop"}],"usage":{"prompt_tokens":5,"completion_tokens":3,"total_tokens":8}}`

func opencodeTestAuth() *cliproxyauth.Auth {
	return &cliproxyauth.Auth{
		Provider:   "opencode",
		Attributes: map[string]string{},
		Metadata:   map[string]any{"api_key": "sk-test", "access_token": "sk-test"},
	}
}

func TestOpencodeUpstreamRoute(t *testing.T) {
	if got := opencodeUpstreamRoute("minimax-m2.5"); got != "anthropic" {
		t.Fatalf("minimax route = %q", got)
	}
	if got := opencodeUpstreamRoute("qwen3.8-flash"); got != "anthropic" {
		t.Fatalf("qwen flash route = %q", got)
	}
	if got := opencodeUpstreamRoute("gpt-5.6-luna"); got != "responses" {
		t.Fatalf("luna route = %q", got)
	}
	if got := opencodeUpstreamRoute("glm-5.2"); got != "chat" {
		t.Fatalf("glm route = %q", got)
	}
	if got := registry.OpencodeUpstreamRoute("glm-5.2(high)"); got != "chat" {
		t.Fatalf("suffixed route = %q", got)
	}
}

func TestOpencodeChatRoundTrip(t *testing.T) {
	var upstreamURL, authHeader, uaHeader, upstreamModel string
	ctx := context.WithValue(context.Background(), "cliproxy.roundtripper", opencodeRoundTripperFunc(func(req *http.Request) (*http.Response, error) {
		upstreamURL = req.URL.String()
		authHeader = req.Header.Get("Authorization")
		uaHeader = req.Header.Get("User-Agent")
		body, _ := io.ReadAll(req.Body)
		upstreamModel = gjson.GetBytes(body, "model").String()
		return &http.Response{
			StatusCode: http.StatusOK,
			Header:     http.Header{"Content-Type": []string{"application/json"}},
			Body:       io.NopCloser(strings.NewReader(opencodeChatFixture)),
		}, nil
	}))

	executor := NewOpenCodeExecutor(&config.Config{})
	resp, err := executor.Execute(ctx, opencodeTestAuth(), cliproxyexecutor.Request{
		Model:   "glm-5.2",
		Payload: []byte(`{"model":"glm-5.2","messages":[{"role":"user","content":"hi"}]}`),
	}, cliproxyexecutor.Options{SourceFormat: sdktranslator.FormatOpenAI})
	if err != nil {
		t.Fatalf("Execute() error = %v", err)
	}
	if upstreamURL != "https://opencode.ai/zen/go/v1/chat/completions" {
		t.Fatalf("upstreamURL = %q", upstreamURL)
	}
	if authHeader != "Bearer sk-test" {
		t.Fatalf("Authorization = %q", authHeader)
	}
	if !strings.HasPrefix(uaHeader, "CLIProxyAPI/") {
		t.Fatalf("User-Agent = %q, want CLIProxyAPI/", uaHeader)
	}
	if upstreamModel != "glm-5.2" {
		t.Fatalf("upstream model = %q", upstreamModel)
	}
	if got := gjson.GetBytes(resp.Payload, "choices.0.message.content").String(); got != "hello" {
		t.Fatalf("response text = %q", got)
	}
}

func TestOpencodeClaudeSourceTranslates(t *testing.T) {
	var upstreamBody []byte
	ctx := context.WithValue(context.Background(), "cliproxy.roundtripper", opencodeRoundTripperFunc(func(req *http.Request) (*http.Response, error) {
		var err error
		upstreamBody, err = io.ReadAll(req.Body)
		if err != nil {
			return nil, err
		}
		return &http.Response{
			StatusCode: http.StatusOK,
			Header:     http.Header{"Content-Type": []string{"application/json"}},
			Body:       io.NopCloser(strings.NewReader(opencodeChatFixture)),
		}, nil
	}))

	executor := NewOpenCodeExecutor(&config.Config{})
	resp, err := executor.Execute(ctx, opencodeTestAuth(), cliproxyexecutor.Request{
		Model:   "glm-5.2",
		Payload: []byte(`{"model":"glm-5.2","max_tokens":64,"messages":[{"role":"user","content":"hi"}]}`),
	}, cliproxyexecutor.Options{SourceFormat: sdktranslator.FormatClaude})
	if err != nil {
		t.Fatalf("Execute() error = %v", err)
	}
	// Claude source on a chat-route model goes out as OpenAI upstream.
	if got := gjson.GetBytes(upstreamBody, "messages.0.role").String(); got != "user" {
		t.Fatalf("upstream role = %q", got)
	}
	if got := gjson.GetBytes(resp.Payload, "content.0.text").String(); got != "hello" {
		t.Fatalf("claude response text = %q (payload %s)", got, resp.Payload)
	}
}

func TestOpencodeNoToolChoiceOnDeepseek(t *testing.T) {
	var upstreamBody []byte
	ctx := context.WithValue(context.Background(), "cliproxy.roundtripper", opencodeRoundTripperFunc(func(req *http.Request) (*http.Response, error) {
		var err error
		upstreamBody, err = io.ReadAll(req.Body)
		if err != nil {
			return nil, err
		}
		return &http.Response{
			StatusCode: http.StatusOK,
			Header:     http.Header{"Content-Type": []string{"application/json"}},
			Body:       io.NopCloser(strings.NewReader(opencodeChatFixture)),
		}, nil
	}))

	executor := NewOpenCodeExecutor(&config.Config{})
	_, err := executor.Execute(ctx, opencodeTestAuth(), cliproxyexecutor.Request{
		Model:   "deepseek-v4-flash",
		Payload: []byte(`{"model":"deepseek-v4-flash","messages":[{"role":"user","content":"hi"}],"tool_choice":"auto","tools":[{"type":"function","function":{"name":"f","parameters":{"type":"object"}}}]}`),
	}, cliproxyexecutor.Options{SourceFormat: sdktranslator.FormatOpenAI})
	if err != nil {
		t.Fatalf("Execute() error = %v", err)
	}
	if gjson.GetBytes(upstreamBody, "tool_choice").Exists() {
		t.Fatalf("tool_choice should be stripped for deepseek lanes")
	}
	if got := gjson.GetBytes(upstreamBody, "tools.0.type").String(); got != "function" {
		t.Fatalf("tools type = %q", got)
	}
}

func TestOpencodeMissingKeyUnauthorized(t *testing.T) {
	executor := NewOpenCodeExecutor(&config.Config{})
	auth := &cliproxyauth.Auth{Provider: "opencode"}
	_, err := executor.Execute(context.Background(), auth, cliproxyexecutor.Request{
		Model:   "glm-5.2",
		Payload: []byte(`{}`),
	}, cliproxyexecutor.Options{SourceFormat: sdktranslator.FormatOpenAI})
	if err == nil {
		t.Fatalf("expected unauthorized without key")
	}
}
