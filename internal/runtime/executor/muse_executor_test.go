package executor

import (
	"context"
	"io"
	"net/http"
	"strings"
	"testing"

	museauth "github.com/router-for-me/CLIProxyAPI/v7/internal/auth/muse"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/config"
	cliproxyauth "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/auth"
	cliproxyexecutor "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/executor"
	sdktranslator "github.com/router-for-me/CLIProxyAPI/v7/sdk/translator"
	"github.com/tidwall/gjson"
)

func TestMuseExecutorIdentifier(t *testing.T) {
	if got := NewMuseExecutor(&config.Config{}).Identifier(); got != "muse" {
		t.Fatalf("Identifier = %q, want muse", got)
	}
}

func TestMuseRequestToFormatMatchesWireProtocol(t *testing.T) {
	executor := NewMuseExecutor(&config.Config{})
	for _, source := range []sdktranslator.Format{
		sdktranslator.FormatOpenAI,
		sdktranslator.FormatClaude,
		sdktranslator.FormatGemini,
		sdktranslator.FormatCodex,
	} {
		got := executor.RequestToFormat(cliproxyexecutor.Request{}, cliproxyexecutor.Options{SourceFormat: source})
		if got != sdktranslator.FormatOpenAI {
			t.Fatalf("RequestToFormat(%v) = %v, want OpenAI (executor always posts to /v1/chat/completions)", source, got)
		}
	}
	got := executor.RequestToFormat(cliproxyexecutor.Request{}, cliproxyexecutor.Options{SourceFormat: sdktranslator.FormatOpenAIResponse})
	if got != sdktranslator.FormatOpenAIResponse {
		t.Fatalf("RequestToFormat(responses) = %v, want OpenAIResponse", got)
	}
}

func TestMusePrepareRequestAddsVersionAndKey(t *testing.T) {
	executor := NewMuseExecutor(&config.Config{})
	auth := &cliproxyauth.Auth{
		Provider: "muse",
		Metadata: map[string]any{"muse_api_key": "LLM|test-key"},
	}
	req, _ := http.NewRequest(http.MethodPost, "https://api.meta.ai/v1/chat/completions", nil)
	if err := executor.PrepareRequest(req, auth); err != nil {
		t.Fatalf("PrepareRequest err = %v", err)
	}
	if got := req.Header.Get("Authorization"); got != "Bearer LLM|test-key" {
		t.Fatalf("Authorization = %q", got)
	}
	if got := req.Header.Get("x-api-version"); got != museauth.MuseAPIVersion {
		t.Fatalf("x-api-version = %q, want %q", got, museauth.MuseAPIVersion)
	}
}

func TestMusePrepareRequestCombinedCredential(t *testing.T) {
	executor := NewMuseExecutor(&config.Config{})
	combined := museauth.EncodeCombinedCredential("oauth-token", "LLM|combined-key")
	auth := &cliproxyauth.Auth{
		Provider: "muse",
		Metadata: map[string]any{"access_token": combined},
	}
	req, _ := http.NewRequest(http.MethodPost, "https://api.meta.ai/v1/chat/completions", nil)
	if err := executor.PrepareRequest(req, auth); err != nil {
		t.Fatalf("PrepareRequest err = %v", err)
	}
	if got := req.Header.Get("Authorization"); got != "Bearer LLM|combined-key" {
		t.Fatalf("Authorization = %q, want minted key", got)
	}
}

func TestNormalizeMuseToolsConvertsCustom(t *testing.T) {
	body := []byte(`{"model":"muse-spark-1.3","tools":[{"type":"custom","name":"apply_patch","description":"edit","parameters":{"properties":{"x":{"type":"string"}}}}]}`)
	out := normalizeMuseTools(body)
	if got := getToolType(out); got != "function" {
		t.Fatalf("tool type = %q, want function", got)
	}
}

func getToolType(body []byte) string {
	// Minimal extraction without extra deps in test.
	for i := 0; i+8 < len(body); i++ {
		if string(body[i:i+8]) == `"type":"` {
			rest := string(body[i+8:])
			end := 0
			for end < len(rest) && rest[end] != '"' {
				end++
			}
			return rest[:end]
		}
	}
	return ""
}

func TestMuseRefreshReusesMintedKey(t *testing.T) {
	executor := NewMuseExecutor(&config.Config{})
	auth := &cliproxyauth.Auth{
		Provider: "muse",
		Metadata: map[string]any{"muse_api_key": "LLM|existing", "access_token": "oauth"},
	}
	refreshed, err := executor.Refresh(t.Context(), auth)
	if err != nil {
		t.Fatalf("Refresh err = %v", err)
	}
	if refreshed != auth {
		t.Fatalf("refresh should return same auth when key present")
	}
}

type museRoundTripperFunc func(*http.Request) (*http.Response, error)

func (f museRoundTripperFunc) RoundTrip(req *http.Request) (*http.Response, error) {
	return f(req)
}

const museChatCompletionFixture = `{"id":"chatcmpl-1","object":"chat.completion","created":1,"model":"muse-spark-1.3","choices":[{"index":0,"message":{"role":"assistant","content":"hello"},"finish_reason":"stop"}],"usage":{"prompt_tokens":5,"completion_tokens":3,"total_tokens":8}}`

// museHarnessMatrix covers every client harness protocol against the same
// mock Meta upstream: OpenAI-style (OpenCode and friends), Claude Messages
// (Claude Code, Agent SDK), Gemini generateContent, and Responses (Codex).
func TestMuseHarnessMatrix(t *testing.T) {
	tests := []struct {
		name         string
		source       sdktranslator.Format
		payload      string
		clientUA     string
		wantEndpoint string
		wantTextPath string
	}{
		{
			name:         "openai harness",
			source:       sdktranslator.FormatOpenAI,
			payload:      `{"model":"muse-spark-1.3","messages":[{"role":"user","content":"hi"}]}`,
			clientUA:     "opencode/1.0",
			wantEndpoint: "https://api.meta.ai/v1/chat/completions",
			wantTextPath: "choices.0.message.content",
		},
		{
			name:         "claude harness",
			source:       sdktranslator.FormatClaude,
			payload:      `{"model":"muse-spark-1.3","max_tokens":64,"messages":[{"role":"user","content":"hi"}]}`,
			clientUA:     "claude-cli/2.1.258 (external, cli)",
			wantEndpoint: "https://api.meta.ai/v1/chat/completions",
			wantTextPath: "content.0.text",
		},
		{
			name:         "gemini harness",
			source:       sdktranslator.FormatGemini,
			payload:      `{"contents":[{"role":"user","parts":[{"text":"hi"}]}]}`,
			clientUA:     "gemini-cli/1.0",
			wantEndpoint: "https://api.meta.ai/v1/chat/completions",
			wantTextPath: "candidates.0.content.parts.0.text",
		},
		{
			name:         "codex harness",
			source:       sdktranslator.FormatOpenAIResponse,
			payload:      `{"model":"muse-spark-1.3","input":[{"type":"message","role":"user","content":[{"type":"input_text","text":"hi"}]}]}`,
			clientUA:     "codex_cli_rs/0.114.0",
			wantEndpoint: "https://api.meta.ai/v1/responses",
			wantTextPath: "output.0.content.0.text",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var upstreamURL, authHeader, versionHeader, upstreamUA, upstreamModel string
			var upstreamBody []byte
			ctx := context.WithValue(context.Background(), "cliproxy.roundtripper", museRoundTripperFunc(func(req *http.Request) (*http.Response, error) {
				upstreamURL = req.URL.String()
				authHeader = req.Header.Get("Authorization")
				versionHeader = req.Header.Get("x-api-version")
				upstreamUA = req.Header.Get("User-Agent")
				var errRead error
				upstreamBody, errRead = io.ReadAll(req.Body)
				if errRead != nil {
					return nil, errRead
				}
				body := museChatCompletionFixture
				if tt.source == sdktranslator.FormatOpenAIResponse {
					body = `{"id":"resp_123","object":"response","status":"completed","model":"muse-spark-1.3","output":[{"type":"message","role":"assistant","content":[{"type":"output_text","text":"hello"}]}],"usage":{"total_tokens":10,"input_tokens":6,"output_tokens":4}}`
				}
				return &http.Response{
					StatusCode: http.StatusOK,
					Header:     http.Header{"Content-Type": []string{"application/json"}},
					Body:       io.NopCloser(strings.NewReader(body)),
				}, nil
			}))

			executor := NewMuseExecutor(&config.Config{})
			auth := &cliproxyauth.Auth{
				Provider: "muse",
				Metadata: map[string]any{"muse_api_key": "LLM|test-key"},
			}
			headers := http.Header{}
			if tt.clientUA != "" {
				headers.Set("User-Agent", tt.clientUA)
			}

			resp, err := executor.Execute(ctx, auth, cliproxyexecutor.Request{
				Model:   "muse-spark-1.3",
				Payload: []byte(tt.payload),
			}, cliproxyexecutor.Options{SourceFormat: tt.source, Headers: headers})
			if err != nil {
				t.Fatalf("Execute() error = %v", err)
			}
			if upstreamURL != tt.wantEndpoint {
				t.Fatalf("upstreamURL = %q, want %q", upstreamURL, tt.wantEndpoint)
			}
			if authHeader != "Bearer LLM|test-key" {
				t.Fatalf("Authorization = %q, want minted key", authHeader)
			}
			if versionHeader != museauth.MuseAPIVersion {
				t.Fatalf("x-api-version = %q, want %q", versionHeader, museauth.MuseAPIVersion)
			}
			// Cloaking: the foreign harness UA must never reach Meta.
			if upstreamUA != museUserAgent {
				t.Fatalf("upstream User-Agent = %q, want cloaked %q", upstreamUA, museUserAgent)
			}
			upstreamModel = gjson.GetBytes(upstreamBody, "model").String()
			if upstreamModel != "muse-spark-1.3" {
				t.Fatalf("upstream model = %q, want muse-spark-1.3", upstreamModel)
			}
			if tt.wantTextPath != "" {
				if got := gjson.GetBytes(resp.Payload, tt.wantTextPath).String(); got != "hello" {
					t.Fatalf("response %s = %q, want hello (payload: %s)", tt.wantTextPath, got, resp.Payload)
				}
			}
		})
	}
}

func TestMuseCloakNeverKeepsTransportIdentity(t *testing.T) {
	var upstreamUA string
	ctx := context.WithValue(context.Background(), "cliproxy.roundtripper", museRoundTripperFunc(func(req *http.Request) (*http.Response, error) {
		upstreamUA = req.Header.Get("User-Agent")
		return &http.Response{
			StatusCode: http.StatusOK,
			Header:     http.Header{"Content-Type": []string{"application/json"}},
			Body:       io.NopCloser(strings.NewReader(museChatCompletionFixture)),
		}, nil
	}))

	executor := NewMuseExecutor(&config.Config{})
	auth := &cliproxyauth.Auth{
		Provider: "muse",
		Metadata: map[string]any{"muse_api_key": "LLM|test-key", "cloak_mode": "never"},
	}
	_, err := executor.Execute(ctx, auth, cliproxyexecutor.Request{
		Model:   "muse-spark-1.3",
		Payload: []byte(`{"model":"muse-spark-1.3","messages":[{"role":"user","content":"hi"}]}`),
	}, cliproxyexecutor.Options{
		SourceFormat: sdktranslator.FormatOpenAI,
		Headers:      http.Header{"User-Agent": []string{"claude-cli/9.9"}},
	})
	if err != nil {
		t.Fatalf("Execute() error = %v", err)
	}
	if upstreamUA != "" {
		t.Fatalf("cloak=never upstream UA = %q, want untouched (empty)", upstreamUA)
	}
}

func TestMuseCloakAutoPassesNativeClient(t *testing.T) {
	var upstreamUA string
	ctx := context.WithValue(context.Background(), "cliproxy.roundtripper", museRoundTripperFunc(func(req *http.Request) (*http.Response, error) {
		upstreamUA = req.Header.Get("User-Agent")
		return &http.Response{
			StatusCode: http.StatusOK,
			Header:     http.Header{"Content-Type": []string{"application/json"}},
			Body:       io.NopCloser(strings.NewReader(museChatCompletionFixture)),
		}, nil
	}))

	executor := NewMuseExecutor(&config.Config{})
	auth := &cliproxyauth.Auth{
		Provider: "muse",
		Metadata: map[string]any{"muse_api_key": "LLM|test-key"},
	}
	_, err := executor.Execute(ctx, auth, cliproxyexecutor.Request{
		Model:   "muse-spark-1.3",
		Payload: []byte(`{"model":"muse-spark-1.3","messages":[{"role":"user","content":"hi"}]}`),
	}, cliproxyexecutor.Options{
		SourceFormat: sdktranslator.FormatOpenAI,
		Headers:      http.Header{"User-Agent": []string{"muse-code/0.9"}},
	})
	if err != nil {
		t.Fatalf("Execute() error = %v", err)
	}
	if upstreamUA != "" {
		t.Fatalf("native client upstream UA = %q, want passthrough (empty)", upstreamUA)
	}
}

func TestMuseLongToolNameRoundTrip(t *testing.T) {
	// Reproduces the Claude Agent SDK report: a 68-char mcp__ tool name must
	// go upstream Meta-compliant and come back with its original name.
	original := longMCPName("mcp__plugin_claude-web-search-router__", 68)
	if len([]rune(original)) != 68 {
		t.Fatalf("fixture length = %d, want 68", len([]rune(original)))
	}
	var upstreamName string
	ctx := context.WithValue(context.Background(), "cliproxy.roundtripper", museRoundTripperFunc(func(req *http.Request) (*http.Response, error) {
		body, _ := io.ReadAll(req.Body)
		upstreamName = gjson.GetBytes(body, "tools.0.function.name").String()
		completion := `{"id":"chatcmpl-1","object":"chat.completion","created":1,"model":"muse-spark-1.3","choices":[{"index":0,"message":{"role":"assistant","content":null,"tool_calls":[{"id":"call_1","type":"function","function":{"name":"` + upstreamName + `","arguments":"{}"}}]},"finish_reason":"tool_calls"}],"usage":{"prompt_tokens":5,"completion_tokens":3,"total_tokens":8}}`
		return &http.Response{
			StatusCode: http.StatusOK,
			Header:     http.Header{"Content-Type": []string{"application/json"}},
			Body:       io.NopCloser(strings.NewReader(completion)),
		}, nil
	}))

	executor := NewMuseExecutor(&config.Config{})
	auth := &cliproxyauth.Auth{
		Provider: "muse",
		Metadata: map[string]any{"muse_api_key": "LLM|test-key"},
	}
	payload := []byte(`{"model":"muse-spark-1.3","max_tokens":64,"messages":[{"role":"user","content":"hi"}],"tools":[{"name":"` + original + `","description":"d","input_schema":{"type":"object"}}]}`)
	resp, err := executor.Execute(ctx, auth, cliproxyexecutor.Request{Model: "muse-spark-1.3", Payload: payload}, cliproxyexecutor.Options{SourceFormat: sdktranslator.FormatClaude})
	if err != nil {
		t.Fatalf("Execute() error = %v", err)
	}
	if len([]rune(upstreamName)) > 64 {
		t.Fatalf("upstream tool name length = %d, want <= 64", len([]rune(upstreamName)))
	}
	if got := gjson.GetBytes(resp.Payload, "content.0.name").String(); got != original {
		t.Fatalf("restored tool name = %q, want original %q (payload %s)", got, original, resp.Payload)
	}
}
