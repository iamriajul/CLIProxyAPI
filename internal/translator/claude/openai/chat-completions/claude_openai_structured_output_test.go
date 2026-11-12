package chat_completions

import (
	"strings"
	"testing"

	"github.com/MoeForge/moego/ojson"
)

// Fork (claude-structured-outputs): a strict json_schema reaches Claude as
// native output_config.format, rewritten into the subset Claude accepts, as
// well as upstream's system-prompt instruction.
func TestConvertOpenAIRequestToClaude_JSONSchemaAlsoRidesNativeOutputFormat(t *testing.T) {
	inputJSON := []byte(`{"model":"claude-sonnet-4-6","messages":[{"role":"user","content":"Extract facts."}],` +
		`"response_format":{"type":"json_schema","json_schema":{"name":"extracted_facts","strict":true,"schema":{"type":"object","properties":{"facts":{"type":"array","items":{"type":"string"},"maxItems":5}},"required":["facts"]}}}}`)
	out, _ := ConvertOpenAIRequestToClaude("claude-sonnet-4-6", inputJSON, false)
	if got := ojson.GetBytesNoCopy(out, "output_config.format.type").String(); got != "json_schema" {
		t.Fatalf("output_config.format.type = %q, want json_schema. Output: %s", got, out)
	}
	if got := ojson.GetBytesNoCopy(out, "output_config.format.schema.required.0").String(); got != "facts" {
		t.Fatalf("output_config.format.schema lost the request schema. Output: %s", out)
	}
	if ojson.GetBytesNoCopy(out, "output_config.format.schema.properties.facts.maxItems").Exists() ||
		ojson.GetBytesNoCopy(out, "output_config.format.schema.additionalProperties").Type != ojson.False {
		t.Fatalf("output_config.format.schema was not rewritten for Claude. Output: %s", out)
	}
	if !strings.Contains(ojson.GetBytesNoCopy(out, "system").Raw, "facts") {
		t.Fatalf("upstream's schema instruction must stay in the system prompt. Output: %s", out)
	}
}

// A model without native structured outputs keeps only the prompt instruction.
func TestConvertOpenAIRequestToClaude_JSONSchemaOnAnOlderModelStaysPromptOnly(t *testing.T) {
	inputJSON := []byte(`{"model":"claude-3-7-sonnet-20250219","messages":[{"role":"user","content":"Extract facts."}],` +
		`"response_format":{"type":"json_schema","json_schema":{"name":"extracted_facts","schema":{"type":"object","properties":{"facts":{"type":"array","items":{"type":"string"}}},"required":["facts"]}}}}`)
	out, _ := ConvertOpenAIRequestToClaude("claude-3-7-sonnet-20250219", inputJSON, false)
	if ojson.GetBytesNoCopy(out, "output_config.format").Exists() {
		t.Fatalf("output_config.format sent to a model without structured outputs. Output: %s", out)
	}
	if !strings.Contains(ojson.GetBytesNoCopy(out, "system").Raw, "facts") {
		t.Fatalf("expected the schema instruction in the system prompt. Output: %s", out)
	}
}

// A schema the client did not mark strict asks for best effort, so it keeps
// only the prompt instruction rather than risking a 400 on a loose schema.
func TestConvertOpenAIRequestToClaude_NonStrictJSONSchemaStaysPromptOnly(t *testing.T) {
	inputJSON := []byte(`{"model":"claude-sonnet-4-6","messages":[{"role":"user","content":"Extract facts."}],` +
		`"response_format":{"type":"json_schema","json_schema":{"name":"extracted_facts","schema":{"type":"object","properties":{"facts":{"type":"string"}}}}}}`)
	out, _ := ConvertOpenAIRequestToClaude("claude-sonnet-4-6", inputJSON, false)
	if ojson.GetBytesNoCopy(out, "output_config.format").Exists() {
		t.Fatalf("output_config.format sent for a non-strict schema. Output: %s", out)
	}
	if !strings.Contains(ojson.GetBytesNoCopy(out, "system").Raw, "extracted_facts") {
		t.Fatalf("expected the schema instruction in the system prompt. Output: %s", out)
	}
}
