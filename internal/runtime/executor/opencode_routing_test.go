package executor

import (
	"context"
	"io"
	"net/http"
	"strings"
	"testing"

	"github.com/router-for-me/CLIProxyAPI/v8/internal/config"
	cliproxyauth "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/auth"
	cliproxyexecutor "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/executor"
	sdktranslator "github.com/router-for-me/CLIProxyAPI/v8/sdk/translator"
	"github.com/tidwall/gjson"
)

const museResponsesFixture = `{"id":"resp_1","object":"response","created_at":1,"status":"completed","model":"muse-spark-1.3-contributor","output":[{"type":"message","role":"assistant","content":[{"type":"output_text","text":"hello"}]}],"usage":{"input_tokens":5,"output_tokens":3,"total_tokens":8}}`

const museResponsesStreamFixture = "data: {\"type\":\"response.created\",\"response\":{\"id\":\"resp_1\",\"status\":\"in_progress\",\"model\":\"muse-spark-1.3-contributor\"}}\n\n" +
	"data: {\"type\":\"response.output_text.delta\",\"delta\":\"hello\"}\n\n" +
	"data: {\"type\":\"response.completed\",\"response\":{\"id\":\"resp_1\",\"status\":\"completed\",\"model\":\"muse-spark-1.3-contributor\",\"output\":[{\"type\":\"message\",\"role\":\"assistant\",\"content\":[{\"type\":\"output_text\",\"text\":\"hello\"}]}],\"usage\":{\"input_tokens\":5,\"output_tokens\":3,\"total_tokens\":8}}}\n\n"

const opencodeMessagesFixture = `{"id":"msg_1","type":"message","role":"assistant","model":"minimax-m2.7","content":[{"type":"text","text":"hello"}],"stop_reason":"end_turn","usage":{"input_tokens":5,"output_tokens":3}}`

// The ClaudeExecutor streams upstream whenever the caller needs translation, so
// translated callers on /messages lanes are answered with Messages SSE.
const opencodeMessagesStreamFixture = "event: message_start\ndata: {\"type\":\"message_start\",\"message\":{\"id\":\"msg_1\",\"type\":\"message\",\"role\":\"assistant\",\"model\":\"minimax-m2.7\",\"content\":[],\"usage\":{\"input_tokens\":5,\"output_tokens\":0}}}\n\n" +
	"event: content_block_start\ndata: {\"type\":\"content_block_start\",\"index\":0,\"content_block\":{\"type\":\"text\",\"text\":\"\"}}\n\n" +
	"event: content_block_delta\ndata: {\"type\":\"content_block_delta\",\"index\":0,\"delta\":{\"type\":\"text_delta\",\"text\":\"hello\"}}\n\n" +
	"event: content_block_stop\ndata: {\"type\":\"content_block_stop\",\"index\":0}\n\n" +
	"event: message_delta\ndata: {\"type\":\"message_delta\",\"delta\":{\"stop_reason\":\"end_turn\"},\"usage\":{\"output_tokens\":3}}\n\n" +
	"event: message_stop\ndata: {\"type\":\"message_stop\"}\n\n"

// opencodeCapture records the single upstream request an executor call makes
// and answers it with the given body.
type opencodeCapture struct {
	url  string
	body []byte
}

func (c *opencodeCapture) ctx(contentType, reply string) context.Context {
	return context.WithValue(context.Background(), "cliproxy.roundtripper", opencodeRoundTripperFunc(func(req *http.Request) (*http.Response, error) {
		c.url = req.URL.String()
		var err error
		c.body, err = io.ReadAll(req.Body)
		if err != nil {
			return nil, err
		}
		return &http.Response{
			StatusCode: http.StatusOK,
			Header:     http.Header{"Content-Type": {contentType}},
			Body:       io.NopCloser(strings.NewReader(reply)),
		}, nil
	}))
}

func drainOpencodeStream(t *testing.T, result *cliproxyexecutor.StreamResult) string {
	t.Helper()
	var out strings.Builder
	for chunk := range result.Chunks {
		if chunk.Err != nil {
			t.Fatalf("stream chunk error: %v", chunk.Err)
		}
		out.Write(chunk.Payload)
		out.WriteString("\n")
	}
	return out.String()
}

// Regression for the reported ModelProtocolUnsupported: a chat-completions (or
// Claude) caller asking for a lane the gateway serves only at /responses. The
// request must be translated to /responses, and the reply translated back to
// the caller's own shape: a raw Responses object is unreadable to a chat client.
func TestOpencodeResponsesOnlyLaneTranslatesEveryCaller(t *testing.T) {
	for _, tc := range []struct {
		name   string
		model  string
		source sdktranslator.Format
		check  func(payload []byte) bool
	}{
		{"chat caller", "muse-spark-1.3-contributor", sdktranslator.FormatOpenAI, func(p []byte) bool {
			return gjson.GetBytes(p, "choices.0.message.content").String() == "hello"
		}},
		{"claude caller", "muse-spark-1.3-contributor", sdktranslator.FormatClaude, func(p []byte) bool {
			return gjson.GetBytes(p, "type").String() == "message" && gjson.GetBytes(p, "content.0.text").String() == "hello"
		}},
		{"unreleased revision", "muse-spark-1.4-contributor", sdktranslator.FormatOpenAI, func(p []byte) bool {
			return gjson.GetBytes(p, "choices.0.message.content").String() == "hello"
		}},
		{"openai upstream lane", "gpt-6-luna", sdktranslator.FormatOpenAI, func(p []byte) bool {
			return gjson.GetBytes(p, "choices.0.message.content").String() == "hello"
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var capture opencodeCapture
			payload := []byte(`{"model":"` + tc.model + `","max_tokens":64,"messages":[{"role":"user","content":"hi"}]}`)
			resp, err := NewOpenCodeExecutor(&config.Config{}).Execute(capture.ctx("application/json", museResponsesFixture), opencodeTestAuth(),
				cliproxyexecutor.Request{Model: tc.model, Payload: payload},
				cliproxyexecutor.Options{SourceFormat: tc.source, OriginalRequest: payload})
			if err != nil {
				t.Fatalf("Execute() error = %v", err)
			}
			if capture.url != "https://opencode.ai/zen/go/v1/responses" {
				t.Fatalf("responses-only lane must ride /responses, got %q", capture.url)
			}
			if !gjson.GetBytes(capture.body, "input").IsArray() || gjson.GetBytes(capture.body, "model").String() != tc.model {
				t.Fatalf("upstream body is not a Responses request for %s: %s", tc.model, capture.body)
			}
			if !tc.check(resp.Payload) {
				t.Fatalf("reply not translated back to the caller's shape: %s", resp.Payload)
			}
		})
	}
}

// Streaming takes the same route and translates each event for the caller.
func TestOpencodeResponsesOnlyLaneStreamsInCallerShape(t *testing.T) {
	for _, tc := range []struct {
		source sdktranslator.Format
		want   string
	}{
		{sdktranslator.FormatOpenAI, `"object":"chat.completion.chunk"`},
		{sdktranslator.FormatClaude, "content_block_delta"},
	} {
		var capture opencodeCapture
		payload := []byte(`{"model":"muse-spark-1.3-contributor","max_tokens":64,"stream":true,"messages":[{"role":"user","content":"hi"}]}`)
		result, err := NewOpenCodeExecutor(&config.Config{}).ExecuteStream(capture.ctx("text/event-stream", museResponsesStreamFixture), opencodeTestAuth(),
			cliproxyexecutor.Request{Model: "muse-spark-1.3-contributor", Payload: payload},
			cliproxyexecutor.Options{SourceFormat: tc.source, OriginalRequest: payload, Stream: true})
		if err != nil {
			t.Fatalf("%s: ExecuteStream() error = %v", tc.source, err)
		}
		out := drainOpencodeStream(t, result)
		if capture.url != "https://opencode.ai/zen/go/v1/responses" {
			t.Fatalf("%s: responses-only lane must stream from /responses, got %q", tc.source, capture.url)
		}
		if !strings.Contains(out, tc.want) || !strings.Contains(out, "hello") {
			t.Fatalf("%s: stream not translated to the caller's shape:\n%s", tc.source, out)
		}
		if strings.Contains(out, "response.output_text.delta") {
			t.Fatalf("%s: raw Responses events leaked to the caller:\n%s", tc.source, out)
		}
	}
}

// Claude callers on /messages lanes ride /messages natively, and the streaming
// and non-streaming paths must agree.
func TestOpencodeAnthropicLaneKeepsClaudeWire(t *testing.T) {
	for _, model := range []string{"qwen3.8-flash", "minimax-m2.5", "minimax-m2.7"} {
		streamURL, streamErr := anthropicWireURL(model, true)
		if streamErr != nil {
			t.Fatalf("%s: ExecuteStream() error = %v", model, streamErr)
		}
		nonStreamURL, nonStreamErr := anthropicWireURL(model, false)
		if nonStreamErr != nil {
			t.Fatalf("%s: Execute() error = %v", model, nonStreamErr)
		}
		if streamURL != "https://opencode.ai/zen/go/v1/messages?beta=true" || nonStreamURL != streamURL {
			t.Fatalf("%s: streaming used %q and non-streaming %q, want both on /messages", model, streamURL, nonStreamURL)
		}
	}
}

// anthropicWireURL runs one Claude-caller request and reports the upstream URL.
func anthropicWireURL(model string, stream bool) (string, error) {
	var capture opencodeCapture
	reply, contentType := opencodeMessagesFixture, "application/json"
	if stream {
		reply, contentType = "event: message_start\ndata: {}\n\n", "text/event-stream"
	}
	executor := NewOpenCodeExecutor(&config.Config{})
	req := cliproxyexecutor.Request{
		Model:   model,
		Payload: []byte(`{"model":"` + model + `","max_tokens":64,"messages":[{"role":"user","content":"hi"}]}`),
	}
	opts := cliproxyexecutor.Options{SourceFormat: sdktranslator.FormatClaude}
	ctx := capture.ctx(contentType, reply)
	if stream {
		result, err := executor.ExecuteStream(ctx, opencodeTestAuth(), req, opts)
		if err != nil {
			return "", err
		}
		for range result.Chunks {
		}
		return capture.url, nil
	}
	_, err := executor.Execute(ctx, opencodeTestAuth(), req, opts)
	return capture.url, err
}

// minimax-m2.7 is served only at /messages (the gateway refuses chat for it), so
// a chat caller is translated to the Messages wire and the reply back to chat.
func TestOpencodeMessagesOnlyLaneTranslatesChatCaller(t *testing.T) {
	var capture opencodeCapture
	payload := []byte(`{"model":"minimax-m2.7","max_tokens":64,"messages":[{"role":"user","content":"hi"}]}`)
	resp, err := NewOpenCodeExecutor(&config.Config{}).Execute(capture.ctx("text/event-stream", opencodeMessagesStreamFixture), opencodeTestAuth(),
		cliproxyexecutor.Request{Model: "minimax-m2.7", Payload: payload},
		cliproxyexecutor.Options{SourceFormat: sdktranslator.FormatOpenAI, OriginalRequest: payload})
	if err != nil {
		t.Fatalf("Execute() error = %v", err)
	}
	if !strings.HasPrefix(capture.url, "https://opencode.ai/zen/go/v1/messages") {
		t.Fatalf("messages-only lane must ride /messages, got %q", capture.url)
	}
	if got := gjson.GetBytes(capture.body, "messages.0.role").String(); got != "user" || !gjson.GetBytes(capture.body, "max_tokens").Exists() {
		t.Fatalf("upstream body is not a Messages request: %s", capture.body)
	}
	if got := gjson.GetBytes(resp.Payload, "choices.0.message.content").String(); got != "hello" {
		t.Fatalf("reply not translated back to chat completions: %s", resp.Payload)
	}
}

// Lanes that serve chat keep every non-native caller on /chat/completions,
// including deepseek-v4-flash, whose Responses pin only spares Responses callers
// a translation.
func TestOpencodeChatServingLanesKeepChat(t *testing.T) {
	for _, tc := range []struct {
		model  string
		source sdktranslator.Format
	}{
		{"mimo-v2.6-flash", sdktranslator.FormatOpenAI},
		{"mimo-v2.6-flash", sdktranslator.FormatOpenAIResponse},
		{"deepseek-v4-flash", sdktranslator.FormatOpenAI},
		{"deepseek-v4-flash", sdktranslator.FormatClaude},
		{"minimax-m2.5", sdktranslator.FormatOpenAI},
	} {
		var capture opencodeCapture
		payload := []byte(`{"model":"` + tc.model + `","max_tokens":64,"messages":[{"role":"user","content":"hi"}],"input":[{"type":"message","role":"user","content":[{"type":"input_text","text":"hi"}]}]}`)
		if _, err := NewOpenCodeExecutor(&config.Config{}).Execute(capture.ctx("application/json", opencodeChatFixture), opencodeTestAuth(),
			cliproxyexecutor.Request{Model: tc.model, Payload: payload},
			cliproxyexecutor.Options{SourceFormat: tc.source}); err != nil {
			t.Fatalf("%s/%s: Execute() error = %v", tc.model, tc.source, err)
		}
		if capture.url != "https://opencode.ai/zen/go/v1/chat/completions" {
			t.Fatalf("%s/%s must ride /chat/completions, got %q", tc.model, tc.source, capture.url)
		}
	}
}

// RequestToFormat must report the wire Execute actually uses, so the
// conductor's interceptors see the real upstream format.
func TestOpencodeRequestToFormatMatchesWire(t *testing.T) {
	executor := NewOpenCodeExecutor(&config.Config{})
	for _, tc := range []struct {
		model  string
		source sdktranslator.Format
		want   sdktranslator.Format
	}{
		{"muse-spark-1.3-contributor", sdktranslator.FormatOpenAI, sdktranslator.FormatCodex},
		{"muse-spark-1.3-contributor", sdktranslator.FormatOpenAIResponse, sdktranslator.FormatOpenAIResponse},
		{"deepseek-v4-flash", sdktranslator.FormatOpenAI, sdktranslator.FormatOpenAI},
		{"qwen3.8-flash", sdktranslator.FormatClaude, sdktranslator.FormatClaude},
		{"qwen3.8-flash", sdktranslator.FormatOpenAI, sdktranslator.FormatOpenAI},
		{"minimax-m2.7", sdktranslator.FormatOpenAI, sdktranslator.FormatClaude},
		{"glm-5.2", sdktranslator.FormatClaude, sdktranslator.FormatOpenAI},
	} {
		got := executor.RequestToFormat(cliproxyexecutor.Request{Model: tc.model}, cliproxyexecutor.Options{SourceFormat: tc.source})
		if got != tc.want {
			t.Errorf("RequestToFormat(%q, source=%s) = %s, want %s", tc.model, tc.source, got, tc.want)
		}
	}
}

// Forced tool selection fails on many gateway lanes while "auto" is accepted on
// all of them, so forced choices are relaxed to "auto" on every wire.
func TestOpencodeForcedToolChoiceBecomesAuto(t *testing.T) {
	const chatTools = `"tools":[{"type":"function","function":{"name":"get_time","parameters":{"type":"object"}}}]`
	for _, tc := range []struct {
		name, model, choice, want string
	}{
		{"required", "kimi-k2.7-code", `"required"`, "auto"},
		{"named", "qwen3.8-flash", `{"type":"function","function":{"name":"get_time"}}`, "auto"},
		{"none is honored", "kimi-k2.7-code", `"none"`, "none"},
		{"auto untouched", "glm-5.3", `"auto"`, "auto"},
	} {
		var capture opencodeCapture
		payload := []byte(`{"model":"` + tc.model + `","messages":[{"role":"user","content":"hi"}],` + chatTools + `,"tool_choice":` + tc.choice + `}`)
		if _, err := NewOpenCodeExecutor(&config.Config{}).Execute(capture.ctx("application/json", opencodeChatFixture), opencodeTestAuth(),
			cliproxyexecutor.Request{Model: tc.model, Payload: payload},
			cliproxyexecutor.Options{SourceFormat: sdktranslator.FormatOpenAI}); err != nil {
			t.Fatalf("%s: Execute() error = %v", tc.name, err)
		}
		if got := gjson.GetBytes(capture.body, "tool_choice").String(); got != tc.want {
			t.Errorf("%s: upstream tool_choice = %q, want %q (body %s)", tc.name, got, tc.want, capture.body)
		}
		if !gjson.GetBytes(capture.body, "tools.0").Exists() {
			t.Errorf("%s: tools must stay available", tc.name)
		}
	}
}

// The muse-spark lanes accept no tool_choice but "auto": a forced choice is
// relaxed, and "none" is honored by sending no tools at all.
func TestOpencodeAutoOnlyLaneToolChoice(t *testing.T) {
	const respTools = `"tools":[{"type":"function","name":"get_time","parameters":{"type":"object"}}]`
	for _, tc := range []struct {
		choice    string
		wantTools bool
		want      string
	}{
		{`{"type":"function","name":"get_time"}`, true, "auto"},
		{`"required"`, true, "auto"},
		{`"none"`, false, ""},
	} {
		var capture opencodeCapture
		payload := []byte(`{"model":"muse-spark-1.3-contributor","input":"hi",` + respTools + `,"tool_choice":` + tc.choice + `}`)
		if _, err := NewOpenCodeExecutor(&config.Config{}).Execute(capture.ctx("application/json", museResponsesFixture), opencodeTestAuth(),
			cliproxyexecutor.Request{Model: "muse-spark-1.3-contributor", Payload: payload},
			cliproxyexecutor.Options{SourceFormat: sdktranslator.FormatOpenAIResponse}); err != nil {
			t.Fatalf("%s: Execute() error = %v", tc.choice, err)
		}
		if got := gjson.GetBytes(capture.body, "tool_choice").String(); got != tc.want {
			t.Errorf("%s: upstream tool_choice = %q, want %q", tc.choice, got, tc.want)
		}
		if got := gjson.GetBytes(capture.body, "tools.0").Exists(); got != tc.wantTools {
			t.Errorf("%s: tools present = %v, want %v (body %s)", tc.choice, got, tc.wantTools, capture.body)
		}
	}
}

// On /messages lanes the embedded ClaudeExecutor builds the body, so the rule
// is applied through its upstream body hook: "any" and named-tool choices
// become "auto", for Claude callers and translated callers alike.
func TestOpencodeMessagesLaneForcedToolChoiceBecomesAuto(t *testing.T) {
	for _, tc := range []struct {
		model   string
		source  sdktranslator.Format
		payload string
	}{
		{"minimax-m2.5", sdktranslator.FormatClaude, `{"model":"minimax-m2.5","max_tokens":64,"messages":[{"role":"user","content":"hi"}],"tools":[{"name":"get_time","input_schema":{"type":"object"}}],"tool_choice":{"type":"tool","name":"get_time","disable_parallel_tool_use":true}}`},
		{"minimax-m2.7", sdktranslator.FormatOpenAI, `{"model":"minimax-m2.7","max_tokens":64,"messages":[{"role":"user","content":"hi"}],"tools":[{"type":"function","function":{"name":"get_time","parameters":{"type":"object"}}}],"tool_choice":"required"}`},
	} {
		var capture opencodeCapture
		reply, contentType := opencodeMessagesFixture, "application/json"
		if tc.source != sdktranslator.FormatClaude {
			reply, contentType = opencodeMessagesStreamFixture, "text/event-stream"
		}
		if _, err := NewOpenCodeExecutor(&config.Config{}).Execute(capture.ctx(contentType, reply), opencodeTestAuth(),
			cliproxyexecutor.Request{Model: tc.model, Payload: []byte(tc.payload)},
			cliproxyexecutor.Options{SourceFormat: tc.source, OriginalRequest: []byte(tc.payload)}); err != nil {
			t.Fatalf("%s: Execute() error = %v", tc.model, err)
		}
		if got := gjson.GetBytes(capture.body, "tool_choice.type").String(); got != "auto" {
			t.Errorf("%s: upstream tool_choice = %s, want type auto", tc.model, gjson.GetBytes(capture.body, "tool_choice").Raw)
		}
		if gjson.GetBytes(capture.body, "tool_choice.name").Exists() {
			t.Errorf("%s: tool name must be dropped with the forced choice", tc.model)
		}
		if !gjson.GetBytes(capture.body, "tools.0").Exists() {
			t.Errorf("%s: tools must stay available", tc.model)
		}
	}
	if got := gjson.GetBytes(normalizeOpencodeClaudeToolChoice([]byte(`{"tool_choice":{"type":"any","disable_parallel_tool_use":true}}`)), "tool_choice.disable_parallel_tool_use").Bool(); !got {
		t.Error("disable_parallel_tool_use must survive the rewrite")
	}
}

// Delegated /messages requests must carry what the gateway requires even
// though the ClaudeExecutor's generic header handling would not send it: the
// key as x-api-key (Bearer alone answers 401; login-flow auth files store it
// as metadata["api_key"], which the ClaudeExecutor does not read), the
// x-opencode-session header (forwarded natively only for confirmed Claude Code
// callers), and CPA's own User-Agent rather than the caller's.
func TestOpencodeMessagesLaneSendsGatewayHeaders(t *testing.T) {
	for _, tc := range []struct {
		name, model        string
		source             sdktranslator.Format
		reply, contentType string
	}{
		{"generic claude client", "minimax-m2.5", sdktranslator.FormatClaude, opencodeMessagesFixture, "application/json"},
		{"translated chat caller", "minimax-m2.7", sdktranslator.FormatOpenAI, opencodeMessagesStreamFixture, "text/event-stream"},
	} {
		for _, callerHeaders := range []http.Header{nil, {"User-Agent": {"Python-urllib/3.12"}}} {
			var got http.Header
			ctx := context.WithValue(context.Background(), "cliproxy.roundtripper", opencodeRoundTripperFunc(func(req *http.Request) (*http.Response, error) {
				got = req.Header.Clone()
				return &http.Response{
					StatusCode: http.StatusOK,
					Header:     http.Header{"Content-Type": {tc.contentType}},
					Body:       io.NopCloser(strings.NewReader(tc.reply)),
				}, nil
			}))
			auth := &cliproxyauth.Auth{
				Provider:   "opencode",
				Attributes: map[string]string{},
				Metadata:   map[string]any{"type": "opencode", "api_key": "sk-login-flow"},
			}
			payload := []byte(`{"model":"` + tc.model + `","max_tokens":64,"messages":[{"role":"user","content":"hi"}]}`)
			if _, err := NewOpenCodeExecutor(&config.Config{}).Execute(ctx, auth, cliproxyexecutor.Request{Model: tc.model, Payload: payload},
				cliproxyexecutor.Options{SourceFormat: tc.source, OriginalRequest: payload, Headers: callerHeaders}); err != nil {
				t.Fatalf("%s: Execute() error = %v", tc.name, err)
			}
			if v := got.Get("x-api-key"); v != "sk-login-flow" {
				t.Errorf("%s: x-api-key = %q, want the OpenCode key", tc.name, v)
			}
			if v := got.Get("x-opencode-session"); v == "" {
				t.Errorf("%s: x-opencode-session missing; the gateway answers MissingSessionID", tc.name)
			}
			if v := got.Get("User-Agent"); !strings.HasPrefix(v, "CLIProxyAPI/") {
				t.Errorf("%s: User-Agent = %q, want CPA's own", tc.name, v)
			}
		}
	}
}

// Replayed reasoning from a native Responses caller: the muse-spark lanes drop
// every reasoning item and stop requesting encrypted reasoning (omp #11928);
// other lanes drop empty reasoning shells, which the Grok lanes reject, and
// keep reasoning that still carries encrypted content.
func TestOpencodeReasoningReplayFilter(t *testing.T) {
	const shell = `{"type":"reasoning","summary":[],"content":null}`
	const encrypted = `{"type":"reasoning","summary":[],"encrypted_content":"enc-1"}`
	const turn = `{"type":"function_call","call_id":"c1","name":"get_time","arguments":"{}"},{"type":"function_call_output","call_id":"c1","output":"12:00"}`
	for _, tc := range []struct {
		model         string
		wantReasoning int
		wantInclude   bool
	}{
		{"grok-4.7", 1, true},
		{"muse-spark-1.3-contributor", 0, false},
	} {
		var capture opencodeCapture
		payload := []byte(`{"model":"` + tc.model + `","include":["reasoning.encrypted_content"],"input":[{"type":"message","role":"user","content":[{"type":"input_text","text":"hi"}]},` + shell + `,` + encrypted + `,` + turn + `]}`)
		if _, err := NewOpenCodeExecutor(&config.Config{}).Execute(capture.ctx("application/json", museResponsesFixture), opencodeTestAuth(),
			cliproxyexecutor.Request{Model: tc.model, Payload: payload},
			cliproxyexecutor.Options{SourceFormat: sdktranslator.FormatOpenAIResponse}); err != nil {
			t.Fatalf("%s: Execute() error = %v", tc.model, err)
		}
		reasoning := 0
		for _, item := range gjson.GetBytes(capture.body, "input").Array() {
			if item.Get("type").String() == "reasoning" {
				reasoning++
				if item.Get("encrypted_content").String() != "enc-1" {
					t.Errorf("%s: an empty reasoning shell survived: %s", tc.model, item.Raw)
				}
			}
		}
		if reasoning != tc.wantReasoning {
			t.Errorf("%s: %d reasoning items replayed, want %d (body %s)", tc.model, reasoning, tc.wantReasoning, capture.body)
		}
		if got := strings.Contains(gjson.GetBytes(capture.body, "include").Raw, "reasoning.encrypted_content"); got != tc.wantInclude {
			t.Errorf("%s: encrypted reasoning requested = %v, want %v", tc.model, got, tc.wantInclude)
		}
		if n := len(gjson.GetBytes(capture.body, "input").Array()); n != 3+tc.wantReasoning {
			t.Errorf("%s: %d input items, want the user turn and tool exchange kept", tc.model, n)
		}
	}
}

// A translated caller's reasoning does not survive the round trip (a Claude
// thinking signature restores to a blob the Grok lanes cannot decode), so it is
// not replayed at all, even when it carries encrypted content; a native
// Responses caller's identical item is kept.
func TestOpencodeTranslatedCallerReasoningNotReplayed(t *testing.T) {
	body := []byte(`{"input":[{"type":"message","role":"user","content":"hi"},{"type":"reasoning","summary":[],"encrypted_content":"enc-1"},{"type":"function_call","call_id":"c1","name":"get_time","arguments":"{}"},{"type":"function_call_output","call_id":"c1","output":"12:00"}]}`)
	count := func(b []byte) (reasoning, total int) {
		for _, item := range gjson.GetBytes(b, "input").Array() {
			total++
			if item.Get("type").String() == "reasoning" {
				reasoning++
			}
		}
		return
	}
	if r, n := count(filterOpencodeReasoningReplay(body, "grok-4.7", true)); r != 0 || n != 3 {
		t.Errorf("translated caller: %d reasoning of %d items, want 0 of 3", r, n)
	}
	if r, n := count(filterOpencodeReasoningReplay(body, "grok-4.7", false)); r != 1 || n != 4 {
		t.Errorf("native Responses caller: %d reasoning of %d items, want 1 of 4", r, n)
	}
}
