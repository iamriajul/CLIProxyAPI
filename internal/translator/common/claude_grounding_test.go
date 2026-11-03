package common

import (
	"strings"
	"testing"

	"github.com/MoeForge/moego/ojson"
)

func TestBuildClaudeWebSearchContent_KeepsTextWhenCitationHasNoURL(t *testing.T) {
	grounding := ojson.Parse(`{
		"webSearchQueries": ["q"],
		"groundingChunks": [{"web": {"uri": "https://example.com", "title": "Example"}}],
		"groundingSupports": [
			{"segment": {"startIndex": 6, "endIndex": 11}, "groundingChunkIndices": [5]},
			{"segment": {"startIndex": 12, "endIndex": 16}, "groundingChunkIndices": [0]}
		]
	}`)
	out := BuildClaudeWebSearchContent("srvtoolu_test", "Alpha gamma delta.", grounding)
	var text strings.Builder
	ojson.ParseBytes(out).ForEach(func(_, block ojson.Result) bool {
		if block.Get("type").String() == "text" {
			text.WriteString(block.Get("text").String())
		}
		return true
	})
	if got := text.String(); got != "Alpha gamma delta." {
		t.Fatalf("visible text = %q, body %s", got, out)
	}
	cited := ojson.ParseBytes(out).Get(`#(type=="text")#.citations.0.url`)
	for _, url := range cited.Array() {
		if url.String() == "" {
			t.Fatalf("empty citation url: %s", out)
		}
	}
}

func TestBuildClaudeWebSearchContent_NonWebChunkIsUncited(t *testing.T) {
	grounding := ojson.Parse(`{
		"webSearchQueries": ["q"],
		"groundingChunks": [{"retrievedContext": {"uri": "rag-doc-1", "title": "Internal"}}],
		"groundingSupports": [
			{"segment": {"startIndex": 0, "endIndex": 4}, "groundingChunkIndices": [0]}
		]
	}`)
	out := BuildClaudeWebSearchContent("srvtoolu_test", "beta", grounding)
	block := ojson.ParseBytes(out).Get(`#(type=="text")`)
	if block.Get("text").String() != "beta" {
		t.Fatalf("text = %s", out)
	}
	if block.Get("citations").Exists() {
		t.Fatalf("non-web chunk must not cite: %s", block.Raw)
	}
}
