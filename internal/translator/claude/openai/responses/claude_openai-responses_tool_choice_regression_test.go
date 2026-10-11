package responses

import (
	"testing"

	"github.com/tidwall/gjson"
)

// A Responses client that declares a `namespace` tool and forces one of its
// children via tool_choice must keep the hard constraint. Dropping the
// toolNameMap assignment silently degraded these to `auto`, which is a
// permissions bypass on a forced-tool request.
func TestNamespaceForcedToolChoiceSurvivesConversion(t *testing.T) {
	raw := []byte(`{
		"model":"claude-test",
		"input":[{"role":"user","content":"read the file"}],
		"tools":[{"type":"namespace","name":"fs","tools":[
			{"type":"function","name":"read_file","parameters":{"type":"object","properties":{}}}
		]}],
		"tool_choice":{"type":"function","namespace":"fs","name":"read_file"}
	}`)
	out, _ := ConvertOpenAIResponsesRequestToClaude("claude-test", raw, false)

	choice := gjson.GetBytes(out, "tool_choice")
	if !choice.Exists() {
		t.Fatalf("forced namespace tool_choice was dropped: %s", out)
	}
	if got := choice.Get("type").String(); got != "tool" {
		t.Fatalf("tool_choice.type = %q, want tool: %s", got, out)
	}
	// The emitted name must be the qualified name Claude actually sees.
	declared := map[string]bool{}
	gjson.GetBytes(out, "tools").ForEach(func(_, tool gjson.Result) bool {
		declared[tool.Get("name").String()] = true
		return true
	})
	if !declared[choice.Get("name").String()] {
		t.Fatalf("tool_choice.name %q is not a declared tool %v: %s", choice.Get("name").String(), declared, out)
	}
}

// A client function literally named `web_search` must not shadow a declared
// typed search tool when the client forces the search: the server tool wins
// the name, and the forced choice must resolve to it.
func TestClientFunctionNamedWebSearchDoesNotShadowForcedSearch(t *testing.T) {
	raw := []byte(`{
		"model":"claude-test",
		"input":[{"role":"user","content":"search"}],
		"tools":[{"type":"web_search"},{"type":"function","name":"web_search","parameters":{"type":"object","properties":{}}}],
		"tool_choice":{"type":"web_search"}
	}`)
	out, _ := ConvertOpenAIResponsesRequestToClaude("claude-test", raw, false)
	tools := gjson.GetBytes(out, "tools").Array()
	if len(tools) != 1 {
		t.Fatalf("expected the server tool to win the name outright, got %d tools: %s", len(tools), out)
	}
	if got := tools[0].Get("type").String(); got != "web_search_20250305" {
		t.Fatalf("surviving tool type = %q, want the server tool: %s", got, out)
	}
	choice := gjson.GetBytes(out, "tool_choice")
	if got := choice.Get("type").String(); got != "tool" {
		t.Fatalf("tool_choice.type = %q, want tool: %s", got, out)
	}
	if got, want := choice.Get("name").String(), tools[0].Get("name").String(); got != want {
		t.Fatalf("tool_choice.name = %q, want the surviving server tool %q: %s", got, want, out)
	}
}
