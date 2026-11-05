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

// ClaudeOutputFormatSchema returns the JSON schema of a json_schema response
// format (Chat Completions response_format, or Responses text.format) when it
// can be held to Claude's native structured outputs, the way Anthropic's own
// SDKs send it: output_config.format {type: json_schema, schema}. A prompt
// instruction only asks for the shape; this makes the model produce it.
// Anything else (json_object, no schema, a model without structured outputs)
// returns false and keeps BuildClaudeStructuredOutputInstruction.
func ClaudeOutputFormatSchema(format ojson.Result, modelName string) (string, bool) {
	if !format.Exists() || strings.ToLower(strings.TrimSpace(format.Get("type").String())) != "json_schema" {
		return "", false
	}
	schema := format.Get("json_schema.schema")
	if !schema.Exists() {
		schema = format.Get("schema")
	}
	if !schema.IsObject() || !ClaudeModelTakesStructuredOutputs(modelName) {
		return "", false
	}
	// The format's description tells the model what the answer is for; a
	// native format has no field for it, so it travels as the schema's own.
	desc := strings.TrimSpace(format.Get("json_schema.description").String())
	if desc == "" {
		desc = strings.TrimSpace(format.Get("description").String())
	}
	if desc == "" || schema.Get("description").Exists() {
		return schema.Raw, true
	}
	doc := ojson.NewDocString(schema.Raw)
	_ = doc.Set("description", desc)
	return doc.String(), true
}
