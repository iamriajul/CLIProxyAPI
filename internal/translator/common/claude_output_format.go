package common

import (
	"regexp"
	"strings"

	"github.com/MoeForge/moego/ojson"
)

// claudeModelsWithoutStructuredOutputs are the Claude models that predate
// native structured outputs (output_config.format): every Claude 3 model, and
// Claude Opus 4 and Sonnet 4 as first released. Opus 4.1 and every later model
// take it.
var claudeModelsWithoutStructuredOutputs = regexp.MustCompile(`^claude-(3|(opus|sonnet)-4(-0|-\d{8})?$)`)

// ClaudeModelTakesStructuredOutputs reports whether modelName is an Anthropic
// Claude model that honours output_config.format. A model a Claude-compatible
// endpoint serves under its own name (GLM, Kimi, ...) is not assumed to.
func ClaudeModelTakesStructuredOutputs(modelName string) bool {
	model := strings.ToLower(strings.TrimSpace(modelName))
	return strings.HasPrefix(model, "claude-") && !claudeModelsWithoutStructuredOutputs.MatchString(model)
}

// ClaudeOutputFormatSchema returns the JSON schema of a strict json_schema
// response format (Chat Completions response_format, or Responses
// text.format) when it can be held to Claude's native structured outputs, the
// way Anthropic's own SDKs send it: output_config.format {type: json_schema,
// schema}. A prompt instruction only asks for the shape; this makes the model
// produce it.
//
// Only a format the client marked strict goes native: a non-strict schema
// asks for best effort, and enforcing it would turn a loose schema Claude
// cannot hold into a 400. The schema is rewritten into the subset Claude
// accepts (ClaudeStrictSchema). Anything else (json_object, no schema, not
// strict, a recursive or unreadable schema, a model without structured
// outputs) returns false and keeps BuildClaudeStructuredOutputInstruction.
func ClaudeOutputFormatSchema(format ojson.Result, modelName string) (string, bool) {
	if !format.Exists() || strings.ToLower(strings.TrimSpace(format.Get("type").String())) != "json_schema" {
		return "", false
	}
	schema := format.Get("json_schema.schema")
	strict := format.Get("json_schema.strict")
	if !schema.Exists() {
		schema = format.Get("schema")
		strict = format.Get("strict")
	}
	if !schema.IsObject() || strict.Type != ojson.True || !ClaudeModelTakesStructuredOutputs(modelName) {
		return "", false
	}
	// The format's description tells the model what the answer is for; a
	// native format has no field for it, so it travels as the schema's own.
	desc := strings.TrimSpace(format.Get("json_schema.description").String())
	if desc == "" {
		desc = strings.TrimSpace(format.Get("description").String())
	}
	if desc != "" && !schema.Get("description").Exists() {
		doc := ojson.NewDocString(schema.Raw)
		_ = doc.Set("description", desc)
		schema = ojson.Parse(doc.String())
	}
	return ClaudeStrictSchema(schema)
}
