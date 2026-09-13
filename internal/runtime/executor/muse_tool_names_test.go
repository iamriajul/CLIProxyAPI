package executor

import (
	"strings"
	"testing"

	sdktranslator "github.com/router-for-me/CLIProxyAPI/v7/sdk/translator"
)

func longMCPName(prefix string, total int) string {
	name := prefix
	for len([]rune(name)) < total {
		name += "x"
	}
	return name
}

func TestRemapMuseToolNamesPassthrough(t *testing.T) {
	body := []byte(`{"model":"muse-spark-1.3","messages":[{"role":"user","content":"hi"}],"tools":[{"type":"function","function":{"name":"short_tool","description":"d","parameters":{"type":"object"}}}]}`)
	out, reverseMap := remapMuseToolNames(body)
	if reverseMap != nil {
		t.Fatalf("reverseMap = %v, want nil for compliant names", reverseMap)
	}
	if string(out) != string(body) {
		t.Fatalf("compliant body was rewritten")
	}
}

func TestRemapMuseToolNamesShortensOverlong(t *testing.T) {
	original := "mcp__some_rather_long_server_name__" + longMCPName("tool", 68-len("mcp__some_rather_long_server_name__"))
	if len([]rune(original)) != 68 {
		t.Fatalf("fixture length = %d, want 68", len([]rune(original)))
	}
	body := []byte(`{"model":"m","messages":[{"role":"user","content":"hi"}],"tools":[{"type":"function","function":{"name":"` + original + `","parameters":{"type":"object"}}}]}`)
	out, reverseMap := remapMuseToolNames(body)
	if len(reverseMap) != 1 {
		t.Fatalf("reverseMap size = %d, want 1", len(reverseMap))
	}
	var short string
	for shortName, orig := range reverseMap {
		short = shortName
		if orig != original {
			t.Fatalf("reverse maps to %q, want original", orig)
		}
	}
	if len([]rune(short)) > 64 {
		t.Fatalf("short name length = %d, want <= 64", len([]rune(short)))
	}
	if !museToolNameCharset(short) {
		t.Fatalf("short name %q has invalid chars", short)
	}
	if !strings.Contains(string(out), short) {
		t.Fatalf("rewritten body missing alias")
	}
	if strings.Contains(string(out), original) {
		t.Fatalf("rewritten body still contains original")
	}
	// Deterministic: same input maps identically.
	_, reverseMap2 := remapMuseToolNames(body)
	for shortName := range reverseMap2 {
		if shortName != short {
			t.Fatalf("non-deterministic alias: %q vs %q", shortName, short)
		}
	}
}

func TestRemapMuseToolNamesCollision(t *testing.T) {
	head := strings.Repeat("a", 60)
	first := head + strings.Repeat("b", 20)
	second := head + strings.Repeat("c", 20)
	body := []byte(`{"model":"m","tools":[{"type":"function","function":{"name":"` + first + `","parameters":{}}},{"type":"function","function":{"name":"` + second + `","parameters":{}}}]}`)
	out, reverseMap := remapMuseToolNames(body)
	if len(reverseMap) != 2 {
		t.Fatalf("reverseMap size = %d, want 2 distinct aliases", len(reverseMap))
	}
	if strings.Contains(string(out), first) || strings.Contains(string(out), second) {
		t.Fatalf("originals leaked into rewritten body")
	}
}

func TestRemapMuseToolNamesHistoryAndChoice(t *testing.T) {
	original := longMCPName("mcp__srv__tool_", 68)
	body := []byte(`{"model":"m","tool_choice":{"type":"function","function":{"name":"` + original + `"}},"messages":[{"role":"assistant","content":null,"tool_calls":[{"id":"call_1","type":"function","function":{"name":"` + original + `","arguments":"{}"}}]}],"tools":[{"type":"function","function":{"name":"` + original + `","parameters":{}}}]}`)
	out, reverseMap := remapMuseToolNames(body)
	if len(reverseMap) != 1 {
		t.Fatalf("reverseMap size = %d, want 1", len(reverseMap))
	}
	if strings.Contains(string(out), original) {
		t.Fatalf("original name leaked: %s", out)
	}
}

func TestRemapMuseToolNamesSanitizesCharset(t *testing.T) {
	original := "tool with spaces.and/slashes" + strings.Repeat("z", 40)
	body := []byte(`{"model":"m","tools":[{"type":"function","function":{"name":"` + original + `","parameters":{}}}]}`)
	_, reverseMap := remapMuseToolNames(body)
	if len(reverseMap) != 1 {
		t.Fatalf("invalid-charset name was not renamed")
	}
	for short := range reverseMap {
		if !museToolNameCharset(short) || len([]rune(short)) > 64 {
			t.Fatalf("alias %q not Meta-compliant", short)
		}
	}
}

func TestRestoreOpenAIChatNames(t *testing.T) {
	original := longMCPName("mcp__srv__tool_", 68)
	shortBody, reverseMap := remapMuseToolNames([]byte(`{"tools":[{"type":"function","function":{"name":"` + original + `"}}]}`))
	_ = shortBody
	var short string
	for short = range reverseMap {
	}
	resp := []byte(`{"id":"1","choices":[{"message":{"role":"assistant","tool_calls":[{"id":"call_1","type":"function","function":{"name":"` + short + `","arguments":"{}"}}]}}]}`)
	restored := restoreMuseToolNamesInJSON(resp, sdktranslator.FormatOpenAI, reverseMap)
	if !strings.Contains(string(restored), original) {
		t.Fatalf("original not restored: %s", restored)
	}
	if strings.Contains(string(restored), short) {
		t.Fatalf("alias leaked into restored response")
	}
	// Unknown names pass through.
	untouched := restoreMuseToolNamesInJSON([]byte(`{"choices":[{"message":{"tool_calls":[{"function":{"name":"other"}}]}}]}`), sdktranslator.FormatOpenAI, reverseMap)
	if !strings.Contains(string(untouched), "other") {
		t.Fatalf("unrelated name was mangled")
	}
}

func TestRestoreStreamLine(t *testing.T) {
	original := longMCPName("mcp__srv__tool_", 68)
	_, reverseMap := remapMuseToolNames([]byte(`{"tools":[{"type":"function","function":{"name":"` + original + `"}}]}`))
	var short string
	for short = range reverseMap {
	}
	line := []byte(`data: {"choices":[{"delta":{"tool_calls":[{"function":{"name":"` + short + `"}}]}}]}`)
	restored := restoreMuseToolNamesInStreamLine(line, sdktranslator.FormatOpenAI, reverseMap)
	if !strings.Contains(string(restored), original) || !strings.HasPrefix(string(restored), "data: ") {
		t.Fatalf("stream delta not restored: %s", restored)
	}
	claudeLine := []byte(`data: {"type":"content_block_start","content_block":{"type":"tool_use","name":"` + short + `"}}`)
	restoredClaude := restoreMuseToolNamesInStreamLine(claudeLine, sdktranslator.FormatClaude, reverseMap)
	if !strings.Contains(string(restoredClaude), original) {
		t.Fatalf("claude event not restored: %s", restoredClaude)
	}
	// Framing preserved.
	for _, framing := range [][]byte{[]byte("[DONE]"), []byte("data: [DONE]"), []byte(""), []byte(": ping"), []byte("data: not-json{{{")} {
		if got := restoreMuseToolNamesInStreamLine(framing, sdktranslator.FormatOpenAI, reverseMap); string(got) != string(framing) {
			t.Fatalf("framing altered: %q -> %q", framing, got)
		}
	}
	// Empty map is a passthrough.
	if got := restoreMuseToolNamesInStreamLine(line, sdktranslator.FormatOpenAI, nil); string(got) != string(line) {
		t.Fatalf("nil map should passthrough")
	}
}

func TestRestoreResponsesAndClaudeFull(t *testing.T) {
	original := longMCPName("mcp__srv__tool_", 68)
	_, reverseMap := remapMuseToolNames([]byte(`{"tools":[{"type":"function","name":"` + original + `"}]}`))
	var short string
	for short = range reverseMap {
	}
	responses := []byte(`{"output":[{"type":"function_call","name":"` + short + `","call_id":"1"}]}`)
	if got := restoreMuseToolNamesInJSON(responses, sdktranslator.FormatOpenAIResponse, reverseMap); !strings.Contains(string(got), original) {
		t.Fatalf("responses output not restored: %s", got)
	}
	event := []byte(`{"type":"response.output_item.added","item":{"type":"function_call","name":"` + short + `"}}`)
	if got := restoreMuseToolNamesInStreamLine(event, sdktranslator.FormatOpenAIResponse, reverseMap); !strings.Contains(string(got), original) {
		t.Fatalf("responses event not restored: %s", got)
	}
	claude := []byte(`{"content":[{"type":"tool_use","name":"` + short + `","input":{}}]}`)
	if got := restoreMuseToolNamesInJSON(claude, sdktranslator.FormatClaude, reverseMap); !strings.Contains(string(got), original) {
		t.Fatalf("claude content not restored: %s", got)
	}
	gemini := []byte(`{"candidates":[{"content":{"parts":[{"functionCall":{"name":"` + short + `"}}]}}]}`)
	if got := restoreMuseToolNamesInJSON(gemini, sdktranslator.FormatGemini, reverseMap); !strings.Contains(string(got), original) {
		t.Fatalf("gemini part not restored: %s", got)
	}
}
