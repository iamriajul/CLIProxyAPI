package websearch

import (
	"github.com/MoeForge/moego/ojson"
)

// usageTotals accumulates token usage across fallback loop iterations so
// the client-visible response accounts for every upstream call.
type usageTotals struct {
	input  int64
	output int64
}

func (t *usageTotals) add(input, output int64) {
	t.input += input
	t.output += output
}

func (t usageTotals) empty() bool {
	return t.input == 0 && t.output == 0
}

// parseUsage extracts input/output token counts from a response body.
func parseUsage(format string, body []byte) (input, output int64) {
	switch normalizeFallbackFormat(format) {
	case FallbackFormatClaude:
		return ojson.GetBytes(body, "usage.input_tokens").Int(),
			ojson.GetBytes(body, "usage.output_tokens").Int()
	case FallbackFormatOpenAI:
		return ojson.GetBytes(body, "usage.prompt_tokens").Int(),
			ojson.GetBytes(body, "usage.completion_tokens").Int()
	case FallbackFormatOpenAIResponse, FallbackFormatCodex:
		return ojson.GetBytes(body, "usage.input_tokens").Int(),
			ojson.GetBytes(body, "usage.output_tokens").Int()
	case FallbackFormatGemini:
		return ojson.GetBytes(body, "usageMetadata.promptTokenCount").Int(),
			ojson.GetBytes(body, "usageMetadata.candidatesTokenCount").Int()
	default:
		return 0, 0
	}
}

// withUsage rewrites the response usage block with loop totals. Per-call
// detail objects are dropped because they cannot be meaningfully summed.
func withUsage(format string, body []byte, totals usageTotals) []byte {
	if totals.empty() {
		return body
	}
	out := body
	switch normalizeFallbackFormat(format) {
	case FallbackFormatClaude:
		out, _ = ojson.SetBytes(out, "usage.input_tokens", totals.input)
		out, _ = ojson.SetBytes(out, "usage.output_tokens", totals.output)
	case FallbackFormatOpenAI:
		out, _ = ojson.SetBytes(out, "usage.prompt_tokens", totals.input)
		out, _ = ojson.SetBytes(out, "usage.completion_tokens", totals.output)
		if ojson.GetBytes(out, "usage.total_tokens").Exists() {
			out, _ = ojson.SetBytes(out, "usage.total_tokens", totals.input+totals.output)
		}
		out, _ = ojson.DeleteBytes(out, "usage.prompt_tokens_details")
		out, _ = ojson.DeleteBytes(out, "usage.completion_tokens_details")
	case FallbackFormatOpenAIResponse, FallbackFormatCodex:
		out, _ = ojson.SetBytes(out, "usage.input_tokens", totals.input)
		out, _ = ojson.SetBytes(out, "usage.output_tokens", totals.output)
		if ojson.GetBytes(out, "usage.total_tokens").Exists() {
			out, _ = ojson.SetBytes(out, "usage.total_tokens", totals.input+totals.output)
		}
		out, _ = ojson.DeleteBytes(out, "usage.input_tokens_details")
		out, _ = ojson.DeleteBytes(out, "usage.output_tokens_details")
	case FallbackFormatGemini:
		out, _ = ojson.SetBytes(out, "usageMetadata.promptTokenCount", totals.input)
		out, _ = ojson.SetBytes(out, "usageMetadata.candidatesTokenCount", totals.output)
		if ojson.GetBytes(out, "usageMetadata.totalTokenCount").Exists() {
			out, _ = ojson.SetBytes(out, "usageMetadata.totalTokenCount", totals.input+totals.output)
		}
		out, _ = ojson.DeleteBytes(out, "usageMetadata.promptTokensDetails")
		out, _ = ojson.DeleteBytes(out, "usageMetadata.candidatesTokensDetails")
	}
	return out
}
