package common

import (
	"reflect"
	"testing"

	"github.com/MoeForge/moego/ojson"
)

func TestClaudeStrictSchema(t *testing.T) {
	for _, tc := range []struct {
		name, in, want string
	}{
		{
			"a schema already inside Claude's subset is unchanged",
			`{"type":"object","properties":{"a":{"type":"string","format":"date"},"b":{"type":"array","items":{"type":"integer"},"minItems":1}},"required":["a","b"],"additionalProperties":false}`,
			`{"type":"object","properties":{"a":{"type":"string","format":"date"},"b":{"type":"array","items":{"type":"integer"},"minItems":1}},"required":["a","b"],"additionalProperties":false}`,
		},
		{
			"bounds move into the description, after the existing one",
			`{"type":"integer","minimum":1,"maximum":10,"description":"A number"}`,
			`{"type":"integer","description":"A number\n\n{minimum: 1, maximum: 10}"}`,
		},
		{
			"string limits, patterns and unknown formats move into the description",
			`{"type":"string","minLength":2,"maxLength":5,"pattern":"^[a-z]+$","format":"hex"}`,
			`{"type":"string","description":"{minLength: 2, maxLength: 5, pattern: \"^[a-z]+$\", format: \"hex\"}"}`,
		},
		{
			"every object closes, whatever additionalProperties said",
			`{"type":"object","properties":{"inner":{"type":"object","properties":{"x":{"type":"number"}}}},"additionalProperties":true}`,
			`{"type":"object","properties":{"inner":{"type":"object","properties":{"x":{"type":"number"}},"additionalProperties":false}},"additionalProperties":false}`,
		},
		{
			"an object with no properties gets an empty set",
			`{"type":"object"}`,
			`{"type":"object","properties":{},"additionalProperties":false}`,
		},
		{
			"oneOf becomes anyOf in place",
			`{"oneOf":[{"type":"string"},{"type":"null"}],"description":"maybe"}`,
			`{"anyOf":[{"type":"string"},{"type":"null"}],"description":"maybe"}`,
		},
		{
			"a type list becomes an anyOf of one schema per type",
			`{"type":["string","null"],"description":"maybe","maxLength":3}`,
			`{"anyOf":[{"type":"string","description":"{maxLength: 3}"},{"type":"null","description":"{maxLength: 3}"}],"description":"maybe"}`,
		},
		{
			"literal enums drop the type; array bounds above one move",
			`{"type":"array","items":{"type":"string","enum":["a","b"]},"minItems":2,"maxItems":4}`,
			`{"type":"array","items":{"enum":["a","b"]},"description":"{minItems: 2, maxItems: 4}"}`,
		},
		{
			"non-recursive $defs and $ref keep their shape",
			`{"type":"object","properties":{"p":{"$ref":"#/$defs/P"}},"required":["p"],"additionalProperties":false,"$defs":{"P":{"type":"object","properties":{"n":{"type":"integer","minimum":0}},"required":["n"]}}}`,
			`{"type":"object","properties":{"p":{"$ref":"#/$defs/P"}},"required":["p"],"additionalProperties":false,"$defs":{"P":{"type":"object","properties":{"n":{"type":"integer","description":"{minimum: 0}"}},"required":["n"],"additionalProperties":false}}}`,
		},
		{
			"property names with path characters survive",
			`{"type":"object","properties":{"a.b":{"type":"string"},"c*d":{"type":"string"}},"required":["a.b"],"additionalProperties":false}`,
			`{"type":"object","properties":{"a.b":{"type":"string"},"c*d":{"type":"string"}},"required":["a.b"],"additionalProperties":false}`,
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got, ok := ClaudeStrictSchema(ojson.Parse(tc.in))
			if !ok {
				t.Fatalf("ClaudeStrictSchema(%s) failed", tc.in)
			}
			if got != tc.want {
				t.Fatalf("ClaudeStrictSchema(%s)\n got %s\nwant %s", tc.in, got, tc.want)
			}
		})
	}
}

func TestClaudeStrictSchemaRefusesWhatClaudeCannotHold(t *testing.T) {
	for name, in := range map[string]string{
		"a $defs cycle":           `{"type":"object","properties":{"n":{"$ref":"#/$defs/Node"}},"$defs":{"Node":{"type":"object","properties":{"kids":{"type":"array","items":{"$ref":"#/$defs/Node"}}}}}}`,
		"an indirect $defs cycle": `{"$ref":"#/definitions/A","definitions":{"A":{"type":"object","properties":{"b":{"$ref":"#/definitions/B"}}},"B":{"type":"object","properties":{"a":{"$ref":"#/definitions/A"}}}}}`,
		"a reference to the root": `{"type":"object","properties":{"child":{"$ref":"#"}}}`,
		"a node with no type":     `{"type":"object","properties":{"x":{"description":"anything"}}}`,
		"an unknown type":         `{"type":"object","properties":{"x":{"type":"date"}}}`,
		"not an object":           `["type"]`,
	} {
		if got, ok := ClaudeStrictSchema(ojson.Parse(in)); ok {
			t.Errorf("%s: ClaudeStrictSchema = %s, want it refused", name, got)
		}
	}
}

// What Claude receives is the same schema semantically, whatever order the
// rewrite writes keys in.
func TestClaudeStrictSchemaIsValidJSON(t *testing.T) {
	in := `{"type":"object","properties":{"tags":{"type":"array","items":{"type":"string","maxLength":20},"uniqueItems":true}},"required":["tags"]}`
	got, ok := ClaudeStrictSchema(ojson.Parse(in))
	if !ok || !ojson.Valid(got) {
		t.Fatalf("ClaudeStrictSchema = %s (%v), want valid JSON", got, ok)
	}
	want := map[string]any{
		"type": "object",
		"properties": map[string]any{"tags": map[string]any{
			"type":        "array",
			"items":       map[string]any{"type": "string", "description": "{maxLength: 20}"},
			"description": "{uniqueItems: true}",
		}},
		"required":             []any{"tags"},
		"additionalProperties": false,
	}
	if v := ojson.Parse(got).Value(); !reflect.DeepEqual(v, want) {
		t.Fatalf("ClaudeStrictSchema = %s", got)
	}
}
