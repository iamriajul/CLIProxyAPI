package executor

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"github.com/MoeForge/moego/ojson"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/config"
	cliproxyauth "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/auth"
	cliproxyexecutor "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/executor"
	sdktranslator "github.com/router-for-me/CLIProxyAPI/v8/sdk/translator"
)

const structuredVerdictSchema = `{"type":"object","properties":{"complete":{"type":"boolean"},"reason":{"type":"string"}},"required":["complete","reason"],"additionalProperties":false}`

// A Chat Completions request held to a strict JSON schema reaches Claude with
// that schema in output_config.format, on a Claude sign-in (whose requests
// are cloaked as Claude Code's) and on an API key alike. A prompt instruction
// only asks for the shape, and a model can answer it in prose.
func TestClaudeExecutor_ChatCompletionsJSONSchemaReachesClaudeAsOutputFormat(t *testing.T) {
	for _, tc := range []struct {
		name string
		auth func(baseURL string) *cliproxyauth.Auth
	}{
		{"claude sign-in", func(baseURL string) *cliproxyauth.Auth {
			return &cliproxyauth.Auth{
				ID:         "claude-structured-oauth",
				Attributes: map[string]string{"base_url": baseURL},
				Metadata: map[string]any{
					"access_token": "sk-ant-oat-structured-fixture",
					"account_uuid": "aaaaaaaa-aaaa-4aaa-8aaa-aaaaaaaaaaaa",
				},
			}
		}},
		{"api key", func(baseURL string) *cliproxyauth.Auth {
			return &cliproxyauth.Auth{Attributes: map[string]string{"api_key": "key-structured", "base_url": baseURL}}
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var mu sync.Mutex
			var sent []byte
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				body, _ := io.ReadAll(r.Body)
				if strings.HasSuffix(r.URL.Path, "/v1/messages") {
					mu.Lock()
					sent = body
					mu.Unlock()
				}
				const verdict = `{\"complete\":false,\"reason\":\"waiting on the customer\"}`
				if ojson.GetBytes(body, "stream").Bool() {
					w.Header().Set("Content-Type", "text/event-stream")
					_, _ = io.WriteString(w, strings.Join([]string{
						`event: message_start`,
						`data: {"type":"message_start","message":{"id":"msg_1","type":"message","role":"assistant","model":"claude-sonnet-4-6","usage":{"input_tokens":10,"output_tokens":0}}}`,
						`event: content_block_start`,
						`data: {"type":"content_block_start","index":0,"content_block":{"type":"text","text":""}}`,
						`event: content_block_delta`,
						`data: {"type":"content_block_delta","index":0,"delta":{"type":"text_delta","text":"` + verdict + `"}}`,
						`event: content_block_stop`,
						`data: {"type":"content_block_stop","index":0}`,
						`event: message_delta`,
						`data: {"type":"message_delta","delta":{"stop_reason":"end_turn"},"usage":{"output_tokens":12}}`,
						`event: message_stop`,
						`data: {"type":"message_stop"}`,
						``,
					}, "\n"))
					return
				}
				w.Header().Set("Content-Type", "application/json")
				_, _ = io.WriteString(w, `{"id":"msg_1","type":"message","role":"assistant","model":"claude-sonnet-4-6",`+
					`"content":[{"type":"text","text":"`+verdict+`"}],`+
					`"stop_reason":"end_turn","usage":{"input_tokens":10,"output_tokens":12}}`)
			}))
			defer server.Close()

			payload := []byte(`{"model":"claude-sonnet-4-6","stream":false,` +
				`"messages":[{"role":"system","content":"You check whether a support conversation is finished."},` +
				`{"role":"user","content":"Customer: Hello! (no reply for 2 hours)"}],` +
				`"response_format":{"type":"json_schema","json_schema":{"name":"verdict","strict":true,"schema":` + structuredVerdictSchema + `}}}`)
			resp, err := NewClaudeExecutor(&config.Config{}).Execute(context.Background(), tc.auth(server.URL), cliproxyexecutor.Request{
				Model: "claude-sonnet-4-6", Payload: payload,
			}, cliproxyexecutor.Options{
				SourceFormat: sdktranslator.FormatOpenAI, ResponseFormat: sdktranslator.FormatOpenAI, OriginalRequest: payload,
			})
			if err != nil {
				t.Fatalf("Execute() error = %v", err)
			}

			mu.Lock()
			body := sent
			mu.Unlock()
			if body == nil {
				t.Fatal("nothing reached /v1/messages")
			}
			format := ojson.GetBytes(body, "output_config.format")
			if format.Get("type").String() != "json_schema" {
				t.Fatalf("output_config.format = %s, want a json_schema format (body %s)", format.Raw, body)
			}
			if got := format.Get("schema"); got.Raw != structuredVerdictSchema {
				t.Fatalf("output_config.format.schema = %s, want the request's schema", got.Raw)
			}
			if strings.Contains(string(body), "conforms strictly to the following JSON schema") {
				t.Fatalf("the schema went to Claude as a prompt instruction too: %s", body)
			}
			if got := ojson.GetBytes(resp.Payload, "choices.0.message.content").String(); got != `{"complete":false,"reason":"waiting on the customer"}` {
				t.Fatalf("reply content = %q", got)
			}
		})
	}
}
