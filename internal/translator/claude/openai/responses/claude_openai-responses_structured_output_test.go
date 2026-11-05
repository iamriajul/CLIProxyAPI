package responses

import (
	"strings"
	"testing"

	"github.com/MoeForge/moego/ojson"
)

// Fork (claude-structured-outputs): a text.format json_schema reaches Claude as
// native output_config.format as well as upstream's system-prompt instruction,
// and text.format still wins over response_format, carrying its description.
func TestConvertOpenAIResponsesRequestToClaude_JSONSchemaAlsoRidesNativeOutputFormat(t *testing.T) {
	input := []byte(`{
		"model": "claude-sonnet-4-6",
		"instructions": "Be concise.",
		"input": "Extract facts.",
		"text": {"format": {"type": "json_schema", "name": "winning_schema", "description": "Primary facts",
			"schema": {"type": "object", "properties": {"item": {"type": "string"}}, "required": ["item"]}}},
		"response_format": {"type": "json_object"}
	}`)
	out, _ := ConvertOpenAIResponsesRequestToClaude("claude-sonnet-4-6", input, false)
	if got := ojson.GetBytesNoCopy(out, "output_config.format.type").String(); got != "json_schema" {
		t.Fatalf("output_config.format.type = %q, want json_schema. Output: %s", got, out)
	}
	if got := ojson.GetBytesNoCopy(out, "output_config.format.schema.properties.item.type").String(); got != "string" {
		t.Fatalf("output_config.format is not the winning schema. Output: %s", out)
	}
	if got := ojson.GetBytesNoCopy(out, "output_config.format.schema.description").String(); got != "Primary facts" {
		t.Fatalf("the format's description = %q, want it on the schema. Output: %s", got, out)
	}
	if !strings.Contains(ojson.GetBytesNoCopy(out, "system").Raw, "winning_schema") {
		t.Fatalf("upstream's schema instruction must stay in the system prompt. Output: %s", out)
	}
}

// A model without native structured outputs keeps only the prompt instruction.
func TestConvertOpenAIResponsesRequestToClaude_JSONSchemaOnAnOlderModelStaysPromptOnly(t *testing.T) {
	input := []byte(`{"model":"claude-3-7-sonnet-20250219","input":"Extract facts.",
		"text":{"format":{"type":"json_schema","name":"facts","schema":{"type":"object","properties":{"facts":{"type":"string"}}}}}}`)
	out, _ := ConvertOpenAIResponsesRequestToClaude("claude-3-7-sonnet-20250219", input, false)
	if ojson.GetBytesNoCopy(out, "output_config.format").Exists() {
		t.Fatalf("output_config.format sent to a model without structured outputs. Output: %s", out)
	}
	if !strings.Contains(ojson.GetBytesNoCopy(out, "system").Raw, "facts") {
		t.Fatalf("expected the schema instruction in the system prompt. Output: %s", out)
	}
}
