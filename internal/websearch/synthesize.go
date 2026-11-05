package websearch

import (
	"fmt"
	"strings"
	"time"

	"github.com/MoeForge/moego/ojson"
)

// synthesisSupported reports whether a final non-streaming response can be
// re-emitted as downstream wire chunks for streaming fallback requests.
func synthesisSupported(format string) bool {
	switch normalizeFallbackFormat(format) {
	case FallbackFormatClaude, FallbackFormatOpenAI, FallbackFormatOpenAIResponse, FallbackFormatCodex, FallbackFormatGemini:
		return true
	default:
		return false
	}
}

// SynthesizeStream converts a final non-streaming response body into
// downstream wire chunks matching what executors emit for live streams:
// framed SSE for Claude and Responses, bare JSON for OpenAI chat and
// Gemini (their handlers add framing).
func SynthesizeStream(format, model string, body []byte) ([][]byte, error) {
	if !ojson.ValidBytes(body) {
		return nil, fmt.Errorf("invalid response body")
	}
	switch normalizeFallbackFormat(format) {
	case FallbackFormatClaude:
		return synthesizeClaudeStream(body), nil
	case FallbackFormatOpenAI:
		return synthesizeChatStream(model, body), nil
	case FallbackFormatOpenAIResponse, FallbackFormatCodex:
		return synthesizeResponsesStream(model, body), nil
	case FallbackFormatGemini:
		return [][]byte{body}, nil
	default:
		return nil, fmt.Errorf("unsupported format %q", format)
	}
}

func sseFramed(event string, payload []byte, trailingNewlines int) []byte {
	out := make([]byte, 0, len(event)+len(payload)+16)
	out = append(out, "event: "...)
	out = append(out, event...)
	out = append(out, '\n')
	out = append(out, "data: "...)
	out = append(out, payload...)
	for range trailingNewlines {
		out = append(out, '\n')
	}
	return out
}

func synthesizeClaudeStream(body []byte) [][]byte {
	root := ojson.ParseBytes(body)
	messageID := root.Get("id").String()
	if messageID == "" {
		messageID = fmt.Sprintf("msg_proxy_%d", time.Now().UnixNano())
	}
	model := root.Get("model").String()
	usage := root.Get("usage")
	startUsage := []byte(`{"input_tokens":0,"output_tokens":0}`)
	if usage.Exists() {
		startUsage, _ = ojson.SetBytes(startUsage, "input_tokens", usage.Get("input_tokens").Int())
		for _, field := range []string{"cache_creation_input_tokens", "cache_read_input_tokens"} {
			if value := usage.Get(field); value.Exists() {
				startUsage, _ = ojson.SetBytes(startUsage, field, value.Int())
			}
		}
	}
	start := []byte(`{"type":"message_start","message":{"id":"","type":"message","role":"assistant","content":[],"model":"","stop_reason":null,"usage":{}}}`)
	start, _ = ojson.SetBytes(start, "message.id", messageID)
	start, _ = ojson.SetBytes(start, "message.model", model)
	start, _ = ojson.SetRawBytes(start, "message.usage", startUsage)
	chunks := [][]byte{sseFramed("message_start", start, 2)}

	blocks := root.Get("content")
	index := 0
	if blocks.IsArray() {
		for _, block := range blocks.Array() {
			chunks = append(chunks, synthesizeClaudeBlock(block, index)...)
			index++
		}
	}
	stopReason := strings.TrimSpace(root.Get("stop_reason").String())
	if stopReason == "" {
		stopReason = "end_turn"
	}
	finalUsage := []byte(`{"input_tokens":0,"output_tokens":0}`)
	if usage.Exists() {
		finalUsage = []byte(usage.Raw)
	}
	delta := []byte(`{"type":"message_delta","delta":{"stop_reason":"","stop_sequence":null},"usage":{}}`)
	delta, _ = ojson.SetBytes(delta, "delta.stop_reason", stopReason)
	delta, _ = ojson.SetRawBytes(delta, "usage", finalUsage)
	chunks = append(chunks, sseFramed("message_delta", delta, 2))
	chunks = append(chunks, sseFramed("message_stop", []byte(`{"type":"message_stop"}`), 2))
	return chunks
}

func synthesizeClaudeBlock(block ojson.Result, index int) [][]byte {
	blockType := block.Get("type").String()
	start := []byte(`{"type":"content_block_start","index":0,"content_block":{}}`)
	start, _ = ojson.SetBytes(start, "index", index)
	stop := []byte(`{"type":"content_block_stop","index":0}`)
	stop, _ = ojson.SetBytes(stop, "index", index)
	deltaFor := func(delta []byte) []byte {
		event, _ := ojson.SetBytes([]byte(`{"type":"content_block_delta","index":0}`), "index", index)
		event, _ = ojson.SetRawBytes(event, "delta", delta)
		return sseFramed("content_block_delta", event, 2)
	}
	switch blockType {
	case "text":
		start, _ = ojson.SetRawBytes(start, "content_block", []byte(`{"type":"text","text":""}`))
		delta, _ := ojson.SetBytes([]byte(`{"type":"text_delta","text":""}`), "text", block.Get("text").String())
		return [][]byte{sseFramed("content_block_start", start, 2), deltaFor(delta), sseFramed("content_block_stop", stop, 2)}
	case "tool_use":
		skeleton := []byte(`{"type":"tool_use","id":"","name":"","input":{}}`)
		skeleton, _ = ojson.SetBytes(skeleton, "id", block.Get("id").String())
		skeleton, _ = ojson.SetBytes(skeleton, "name", block.Get("name").String())
		start, _ = ojson.SetRawBytes(start, "content_block", skeleton)
		partial := "{}"
		if input := block.Get("input"); input.Exists() {
			partial = input.Raw
			if input.Type == ojson.String {
				partial = input.String()
			}
		}
		delta, _ := ojson.SetBytes([]byte(`{"type":"input_json_delta","partial_json":""}`), "partial_json", partial)
		return [][]byte{sseFramed("content_block_start", start, 2), deltaFor(delta), sseFramed("content_block_stop", stop, 2)}
	case "thinking":
		start, _ = ojson.SetRawBytes(start, "content_block", []byte(`{"type":"thinking","thinking":""}`))
		chunks := [][]byte{sseFramed("content_block_start", start, 2)}
		thinking, _ := ojson.SetBytes([]byte(`{"type":"thinking_delta","thinking":""}`), "thinking", block.Get("thinking").String())
		chunks = append(chunks, deltaFor(thinking))
		if signature := block.Get("signature").String(); signature != "" {
			signatureDelta, _ := ojson.SetBytes([]byte(`{"type":"signature_delta","signature":""}`), "signature", signature)
			chunks = append(chunks, deltaFor(signatureDelta))
		}
		return append(chunks, sseFramed("content_block_stop", stop, 2))
	case "redacted_thinking":
		skeleton := []byte(`{"type":"redacted_thinking","data":""}`)
		skeleton, _ = ojson.SetBytes(skeleton, "data", block.Get("data").String())
		start, _ = ojson.SetRawBytes(start, "content_block", skeleton)
		return [][]byte{sseFramed("content_block_start", start, 2), sseFramed("content_block_stop", stop, 2)}
	default:
		return nil
	}
}

func synthesizeChatStream(model string, body []byte) [][]byte {
	root := ojson.ParseBytes(body)
	id := root.Get("id").String()
	if id == "" {
		id = fmt.Sprintf("chatcmpl-proxy-%d", time.Now().UnixNano())
	}
	created := root.Get("created").Int()
	if created == 0 {
		created = time.Now().Unix()
	}
	if modelName := strings.TrimSpace(root.Get("model").String()); modelName != "" {
		model = modelName
	}
	base := []byte(`{"id":"","object":"chat.completion.chunk","created":0,"model":"","choices":[{"index":0,"delta":{},"finish_reason":null}]}`)
	base, _ = ojson.SetBytes(base, "id", id)
	base, _ = ojson.SetBytes(base, "created", created)
	base, _ = ojson.SetBytes(base, "model", model)
	role, _ := ojson.SetBytes([]byte(`{}`), "role", "assistant")
	first, _ := ojson.SetRawBytes(append([]byte(nil), base...), "choices.0.delta", role)
	chunks := [][]byte{first}

	message := root.Get("choices.0.message")
	if text := message.Get("content").String(); strings.TrimSpace(text) != "" {
		content, _ := ojson.SetBytes([]byte(`{}`), "content", message.Get("content").String())
		chunk, _ := ojson.SetRawBytes(append([]byte(nil), base...), "choices.0.delta", content)
		chunks = append(chunks, chunk)
	}
	toolCalls := message.Get("tool_calls")
	if toolCalls.IsArray() && len(toolCalls.Array()) > 0 {
		calls := make([][]byte, 0, len(toolCalls.Array()))
		for callIndex, call := range toolCalls.Array() {
			entry := []byte(`{"index":0,"id":"","type":"function","function":{"name":"","arguments":""}}`)
			entry, _ = ojson.SetBytes(entry, "index", callIndex)
			entry, _ = ojson.SetBytes(entry, "id", call.Get("id").String())
			entry, _ = ojson.SetBytes(entry, "function.name", call.Get("function.name").String())
			entry, _ = ojson.SetBytes(entry, "function.arguments", call.Get("function.arguments").String())
			calls = append(calls, entry)
		}
		delta, _ := ojson.SetRawBytes([]byte(`{}`), "tool_calls", joinRawJSONArray(calls))
		chunk, _ := ojson.SetRawBytes(append([]byte(nil), base...), "choices.0.delta", delta)
		chunks = append(chunks, chunk)
	}
	finishReason := strings.TrimSpace(root.Get("choices.0.finish_reason").String())
	if finishReason == "" || finishReason == "null" {
		if toolCalls.IsArray() && len(toolCalls.Array()) > 0 {
			finishReason = "tool_calls"
		} else {
			finishReason = "stop"
		}
	}
	terminal, _ := ojson.SetRawBytes(append([]byte(nil), base...), "choices.0.delta", []byte(`{}`))
	terminal, _ = ojson.SetBytes(terminal, "choices.0.finish_reason", finishReason)
	return append(chunks, terminal)
}

func synthesizeResponsesStream(model string, body []byte) [][]byte {
	root := ojson.ParseBytes(body)
	responseID := root.Get("id").String()
	if responseID == "" {
		responseID = fmt.Sprintf("resp_proxy_%d", time.Now().UnixNano())
	}
	createdAt := root.Get("created_at").Int()
	if createdAt == 0 {
		createdAt = root.Get("created").Int()
	}
	if createdAt == 0 {
		createdAt = time.Now().Unix()
	}
	if modelName := strings.TrimSpace(root.Get("model").String()); modelName != "" {
		model = modelName
	}
	sequence := 0
	nextSeq := func() int {
		value := sequence
		sequence++
		return value
	}
	created := []byte(`{"type":"response.created","sequence_number":0,"response":{"id":"","object":"response","created_at":0,"status":"in_progress","background":false,"error":null,"output":[]}}`)
	created, _ = ojson.SetBytes(created, "sequence_number", nextSeq())
	created, _ = ojson.SetBytes(created, "response.id", responseID)
	created, _ = ojson.SetBytes(created, "response.created_at", createdAt)
	created, _ = ojson.SetBytes(created, "response.model", model)
	inProgress := []byte(`{"type":"response.in_progress","sequence_number":0,"response":{"id":"","object":"response","created_at":0,"status":"in_progress","output":[]}}`)
	inProgress, _ = ojson.SetBytes(inProgress, "sequence_number", nextSeq())
	inProgress, _ = ojson.SetBytes(inProgress, "response.id", responseID)
	inProgress, _ = ojson.SetBytes(inProgress, "response.created_at", createdAt)
	inProgress, _ = ojson.SetBytes(inProgress, "response.model", model)
	chunks := [][]byte{
		sseFramed("response.created", created, 0),
		sseFramed("response.in_progress", inProgress, 0),
	}

	output := root.Get("output")
	outputIndex := 0
	if output.IsArray() {
		for _, item := range output.Array() {
			chunks = append(chunks, synthesizeResponsesItem(item, outputIndex, &sequence)...)
			outputIndex++
		}
	}
	completedResponse := []byte(`{"id":"","object":"response","created_at":0,"status":"completed","background":false,"error":null,"model":"","output":[]}`)
	completedResponse, _ = ojson.SetBytes(completedResponse, "id", responseID)
	completedResponse, _ = ojson.SetBytes(completedResponse, "created_at", createdAt)
	completedResponse, _ = ojson.SetBytes(completedResponse, "model", model)
	if output.IsArray() {
		completedResponse, _ = ojson.SetRawBytes(completedResponse, "output", []byte(output.Raw))
	}
	if usage := root.Get("usage"); usage.Exists() {
		completedResponse, _ = ojson.SetRawBytes(completedResponse, "usage", []byte(usage.Raw))
	}
	completed := []byte(`{"type":"response.completed","sequence_number":0,"response":{}}`)
	completed, _ = ojson.SetBytes(completed, "sequence_number", nextSeq())
	completed, _ = ojson.SetRawBytes(completed, "response", completedResponse)
	return append(chunks, sseFramed("response.completed", completed, 0))
}

func synthesizeResponsesItem(item ojson.Result, outputIndex int, sequence *int) [][]byte {
	nextSeq := func() int {
		value := *sequence
		*sequence++
		return value
	}
	itemType := item.Get("type").String()
	itemID := item.Get("id").String()
	added := []byte(`{"type":"response.output_item.added","sequence_number":0,"output_index":0,"item":{}}`)
	added, _ = ojson.SetBytes(added, "sequence_number", nextSeq())
	added, _ = ojson.SetBytes(added, "output_index", outputIndex)
	skeleton := []byte(`{"id":"","type":"","status":"in_progress"}`)
	skeleton, _ = ojson.SetBytes(skeleton, "id", itemID)
	skeleton, _ = ojson.SetBytes(skeleton, "type", itemType)
	switch itemType {
	case "message":
		skeleton, _ = ojson.SetBytes(skeleton, "role", "assistant")
		skeleton, _ = ojson.SetRawBytes(skeleton, "content", []byte(`[]`))
	case "function_call", "custom_tool_call":
		skeleton, _ = ojson.SetBytes(skeleton, "call_id", item.Get("call_id").String())
		skeleton, _ = ojson.SetBytes(skeleton, "name", item.Get("name").String())
		skeleton, _ = ojson.SetBytes(skeleton, "arguments", "")
	}
	added, _ = ojson.SetRawBytes(added, "item", skeleton)
	chunks := [][]byte{sseFramed("response.output_item.added", added, 0)}

	switch itemType {
	case "message":
		content := item.Get("content")
		if content.IsArray() {
			for contentIndex, part := range content.Array() {
				if part.Get("type").String() != "output_text" {
					continue
				}
				partAdded := []byte(`{"type":"response.content_part.added","sequence_number":0,"item_id":"","output_index":0,"content_index":0,"part":{"type":"output_text","annotations":[],"logprobs":[],"text":""}}`)
				partAdded, _ = ojson.SetBytes(partAdded, "sequence_number", nextSeq())
				partAdded, _ = ojson.SetBytes(partAdded, "item_id", itemID)
				partAdded, _ = ojson.SetBytes(partAdded, "output_index", outputIndex)
				partAdded, _ = ojson.SetBytes(partAdded, "content_index", contentIndex)
				chunks = append(chunks, sseFramed("response.content_part.added", partAdded, 0))
				text := part.Get("text").String()
				delta := []byte(`{"type":"response.output_text.delta","sequence_number":0,"item_id":"","output_index":0,"content_index":0,"delta":"","logprobs":[]}`)
				delta, _ = ojson.SetBytes(delta, "sequence_number", nextSeq())
				delta, _ = ojson.SetBytes(delta, "item_id", itemID)
				delta, _ = ojson.SetBytes(delta, "output_index", outputIndex)
				delta, _ = ojson.SetBytes(delta, "content_index", contentIndex)
				delta, _ = ojson.SetBytes(delta, "delta", text)
				chunks = append(chunks, sseFramed("response.output_text.delta", delta, 0))
				textDone := []byte(`{"type":"response.output_text.done","sequence_number":0,"item_id":"","output_index":0,"content_index":0,"text":"","logprobs":[]}`)
				textDone, _ = ojson.SetBytes(textDone, "sequence_number", nextSeq())
				textDone, _ = ojson.SetBytes(textDone, "item_id", itemID)
				textDone, _ = ojson.SetBytes(textDone, "output_index", outputIndex)
				textDone, _ = ojson.SetBytes(textDone, "content_index", contentIndex)
				textDone, _ = ojson.SetBytes(textDone, "text", text)
				chunks = append(chunks, sseFramed("response.output_text.done", textDone, 0))
				partDone := []byte(`{"type":"response.content_part.done","sequence_number":0,"item_id":"","output_index":0,"content_index":0,"part":{"type":"output_text","annotations":[],"logprobs":[],"text":""}}`)
				partDone, _ = ojson.SetBytes(partDone, "sequence_number", nextSeq())
				partDone, _ = ojson.SetBytes(partDone, "item_id", itemID)
				partDone, _ = ojson.SetBytes(partDone, "output_index", outputIndex)
				partDone, _ = ojson.SetBytes(partDone, "content_index", contentIndex)
				partDone, _ = ojson.SetBytes(partDone, "part.text", text)
				if annotations := part.Get("annotations"); annotations.IsArray() {
					partDone, _ = ojson.SetRawBytes(partDone, "part.annotations", []byte(annotations.Raw))
				}
				chunks = append(chunks, sseFramed("response.content_part.done", partDone, 0))
			}
		}
	case "function_call", "custom_tool_call":
		arguments := item.Get("arguments").String()
		delta := []byte(`{"type":"response.function_call_arguments.delta","sequence_number":0,"item_id":"","output_index":0,"delta":""}`)
		delta, _ = ojson.SetBytes(delta, "sequence_number", nextSeq())
		delta, _ = ojson.SetBytes(delta, "item_id", itemID)
		delta, _ = ojson.SetBytes(delta, "output_index", outputIndex)
		delta, _ = ojson.SetBytes(delta, "delta", arguments)
		chunks = append(chunks, sseFramed("response.function_call_arguments.delta", delta, 0))
	case "web_search_call":
		for _, event := range []string{"response.web_search_call.in_progress", "response.web_search_call.searching", "response.web_search_call.completed"} {
			progress := []byte(`{"type":"","sequence_number":0,"output_index":0,"item_id":""}`)
			progress, _ = ojson.SetBytes(progress, "type", event)
			progress, _ = ojson.SetBytes(progress, "sequence_number", nextSeq())
			progress, _ = ojson.SetBytes(progress, "output_index", outputIndex)
			progress, _ = ojson.SetBytes(progress, "item_id", itemID)
			chunks = append(chunks, sseFramed(event, progress, 0))
		}
	}

	done := []byte(`{"type":"response.output_item.done","sequence_number":0,"output_index":0,"item":{}}`)
	done, _ = ojson.SetBytes(done, "sequence_number", nextSeq())
	done, _ = ojson.SetBytes(done, "output_index", outputIndex)
	completed, _ := ojson.SetBytes([]byte(item.Raw), "status", "completed")
	done, _ = ojson.SetRawBytes(done, "item", completed)
	return append(chunks, sseFramed("response.output_item.done", done, 0))
}
