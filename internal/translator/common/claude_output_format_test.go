package common

import (
	"testing"

	"github.com/MoeForge/moego/ojson"
)

func TestClaudeModelTakesStructuredOutputs(t *testing.T) {
	for model, want := range map[string]bool{
		"claude-sonnet-4-6":          true,
		"claude-haiku-4-5-20251001":  true,
		"claude-opus-4-1-20250805":   true,
		"claude-opus-4-5-20251101":   true,
		"claude-sonnet-5-5":          true,
		"claude-fable-5-1":           true,
		"Claude-Opus-5-5":            true,
		"claude-opus-4-20250514":     false,
		"claude-sonnet-4-20250514":   false,
		"claude-sonnet-4-0":          false,
		"claude-opus-4":              false,
		"claude-3-7-sonnet-20250219": false,
		"claude-3-5-haiku-20241022":  false,
		"glm-4.6":                    false,
		"kimi-k2":                    false,
		"":                           false,
	} {
		if got := ClaudeModelTakesStructuredOutputs(model); got != want {
			t.Errorf("ClaudeModelTakesStructuredOutputs(%q) = %v, want %v", model, got, want)
		}
	}
}

func TestClaudeOutputFormatSchema(t *testing.T) {
	const schema = `{"type":"object","properties":{"a":{"type":"string"}},"required":["a"],"additionalProperties":false}`
	for _, tc := range []struct {
		name, format, model string
		ok                  bool
	}{
		{"chat completions json_schema", `{"type":"json_schema","json_schema":{"name":"x","strict":true,"schema":` + schema + `}}`, "claude-sonnet-4-6", true},
		{"responses text.format", `{"type":"json_schema","name":"x","strict":true,"schema":` + schema + `}`, "claude-sonnet-4-6", true},
		{"json_object keeps the instruction", `{"type":"json_object"}`, "claude-sonnet-4-6", false},
		{"no schema keeps the instruction", `{"type":"json_schema","json_schema":{"name":"x"}}`, "claude-sonnet-4-6", false},
		{"a model without structured outputs keeps the instruction", `{"type":"json_schema","json_schema":{"name":"x","schema":` + schema + `}}`, "claude-3-7-sonnet-20250219", false},
		{"a Claude-compatible endpoint's own model keeps the instruction", `{"type":"json_schema","json_schema":{"name":"x","schema":` + schema + `}}`, "glm-4.6", false},
	} {
		got, ok := ClaudeOutputFormatSchema(ojson.Parse(tc.format), tc.model)
		if ok != tc.ok {
			t.Errorf("%s: ok = %v, want %v", tc.name, ok, tc.ok)
			continue
		}
		if ok && got != schema {
			t.Errorf("%s: schema = %s, want %s", tc.name, got, schema)
		}
	}
}

func TestClaudeOutputFormatSchemaCarriesTheFormatsDescription(t *testing.T) {
	got, ok := ClaudeOutputFormatSchema(ojson.Parse(`{"type":"json_schema","json_schema":{"name":"x","description":"Primary facts","schema":{"type":"object"}}}`), "claude-sonnet-4-6")
	if !ok || ojson.Parse(got).Get("description").String() != "Primary facts" || ojson.Parse(got).Get("type").String() != "object" {
		t.Fatalf("schema = %s (%v), want the format's description on the schema", got, ok)
	}
	got, _ = ClaudeOutputFormatSchema(ojson.Parse(`{"type":"json_schema","description":"Primary facts","schema":{"type":"object","description":"Its own"}}`), "claude-sonnet-4-6")
	if ojson.Parse(got).Get("description").String() != "Its own" {
		t.Fatalf("schema = %s, want the schema's own description kept", got)
	}
}
