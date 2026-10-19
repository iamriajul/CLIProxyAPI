package common

import (
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/tidwall/gjson"
	"github.com/tidwall/sjson"
)

// GroundingSupport is one cited span from Gemini groundingMetadata.
type groundingSupport struct {
	StartIndex int64
	EndIndex   int64
	Text       string
	ChunkURLs  []string
	ChunkTitle string
}

// CitedTextBlock is a Claude text block, with citations when the span is grounded.
type citedTextBlock struct {
	Text      string
	Citations []map[string]any
}

// HasWebGrounding reports whether groundingMetadata carries a real web search.
// An empty object, or chunks that are not web pages, does not count.
func HasWebGrounding(groundingMetadata gjson.Result) bool {
	if !groundingMetadata.Exists() {
		return false
	}
	if queries := groundingMetadata.Get("webSearchQueries"); queries.IsArray() && len(queries.Array()) > 0 {
		return true
	}
	chunks := groundingMetadata.Get("groundingChunks")
	if !chunks.IsArray() {
		return false
	}
	for _, chunk := range chunks.Array() {
		if uri := chunk.Get("web.uri").String(); uri != "" {
			return true
		}
	}
	return false
}

// NewClaudeWebSearchToolUseID returns an id Claude accepts for a server tool use.
func NewClaudeWebSearchToolUseID() string {
	return fmt.Sprintf("srvtoolu_%d", time.Now().UnixNano())
}

func webSearchQueryFromGrounding(groundingMetadata gjson.Result) string {
	if queries := groundingMetadata.Get("webSearchQueries"); queries.IsArray() && len(queries.Array()) > 0 {
		return queries.Array()[0].String()
	}
	return ""
}

func webSearchResultsFromGrounding(groundingMetadata gjson.Result) []byte {
	results := []byte(`[]`)
	chunks := groundingMetadata.Get("groundingChunks")
	if !chunks.IsArray() {
		return results
	}
	seen := make(map[string]struct{})
	for _, chunk := range chunks.Array() {
		web := chunk.Get("web")
		if !web.Exists() {
			continue
		}
		uri := web.Get("uri").String()
		if uri == "" {
			continue
		}
		if _, ok := seen[uri]; ok {
			continue
		}
		seen[uri] = struct{}{}
		result := []byte(`{"type":"web_search_result","page_age":null}`)
		if title := web.Get("title"); title.Exists() {
			result, _ = sjson.SetBytes(result, "title", title.String())
		}
		result, _ = sjson.SetBytes(result, "url", uri)
		results, _ = sjson.SetRawBytes(results, "-1", result)
	}
	return results
}

func parseGroundingSupports(groundingMetadata gjson.Result) []groundingSupport {
	chunksResult := groundingMetadata.Get("groundingChunks")
	if !chunksResult.IsArray() {
		return nil
	}
	chunks := chunksResult.Array()
	chunkData := make([]struct {
		URL   string
		Title string
	}, len(chunks))
	for i, chunk := range chunks {
		web := chunk.Get("web")
		if web.Exists() {
			chunkData[i].URL = web.Get("uri").String()
			chunkData[i].Title = web.Get("title").String()
		}
	}
	supportsResult := groundingMetadata.Get("groundingSupports")
	if !supportsResult.IsArray() {
		return nil
	}
	supports := make([]groundingSupport, 0, len(supportsResult.Array()))
	for _, support := range supportsResult.Array() {
		segment := support.Get("segment")
		if !segment.Exists() {
			continue
		}
		parsed := groundingSupport{
			StartIndex: segment.Get("startIndex").Int(),
			EndIndex:   segment.Get("endIndex").Int(),
			Text:       segment.Get("text").String(),
		}
		if indices := support.Get("groundingChunkIndices"); indices.IsArray() {
			for _, idx := range indices.Array() {
				chunkIndex := int(idx.Int())
				if chunkIndex < 0 || chunkIndex >= len(chunkData) {
					continue
				}
				url := strings.TrimSpace(chunkData[chunkIndex].URL)
				if url == "" {
					continue
				}
				parsed.ChunkURLs = append(parsed.ChunkURLs, url)
				if parsed.ChunkTitle == "" {
					parsed.ChunkTitle = chunkData[chunkIndex].Title
				}
			}
		}
		supports = append(supports, parsed)
	}
	return supports
}

func citedTextBlocks(textContent string, supports []groundingSupport) []citedTextBlock {
	if len(supports) == 0 {
		if textContent == "" {
			return nil
		}
		return []citedTextBlock{{Text: textContent}}
	}
	textBytes := []byte(textContent)
	blocks := make([]citedTextBlock, 0, len(supports)+1)
	lastEnd := int64(0)
	for _, support := range supports {
		if support.EndIndex <= lastEnd {
			continue
		}
		if support.StartIndex > lastEnd {
			start := int(lastEnd)
			end := min(int(support.StartIndex), len(textBytes))
			if start < end {
				blocks = append(blocks, citedTextBlock{Text: string(textBytes[start:end])})
			}
		}
		citedStart := support.StartIndex
		if citedStart < lastEnd {
			citedStart = lastEnd
		}
		citedText := ""
		if citedStart < support.EndIndex {
			start := min(int(citedStart), len(textBytes))
			end := min(int(support.EndIndex), len(textBytes))
			if start < end {
				citedText = string(textBytes[start:end])
			}
		}
		if citedText != "" {
			block := citedTextBlock{Text: citedText}
			if len(support.ChunkURLs) > 0 {
				block.Citations = []map[string]any{{
					"type":       "web_search_result_location",
					"cited_text": citedText,
					"url":        support.ChunkURLs[0],
					"title":      support.ChunkTitle,
				}}
			}
			blocks = append(blocks, block)
		}
		if support.EndIndex > lastEnd {
			lastEnd = support.EndIndex
		}
	}
	if int(lastEnd) < len(textBytes) {
		blocks = append(blocks, citedTextBlock{Text: string(textBytes[lastEnd:])})
	}
	return blocks
}

// BuildClaudeWebSearchContent turns Gemini grounding into the Claude blocks
// a client renders for a server web search: the search call, the results, and
// the answer text with citations.
func BuildClaudeWebSearchContent(toolUseID, textContent string, groundingMetadata gjson.Result) []byte {
	content := []byte(`[]`)
	serverToolUse := []byte(`{"type":"server_tool_use","id":"","name":"web_search","input":{}}`)
	serverToolUse, _ = sjson.SetBytes(serverToolUse, "id", toolUseID)
	if query := webSearchQueryFromGrounding(groundingMetadata); query != "" {
		serverToolUse, _ = sjson.SetBytes(serverToolUse, "input.query", query)
	}
	content, _ = sjson.SetRawBytes(content, "-1", serverToolUse)

	toolResult := []byte(`{"type":"web_search_tool_result","tool_use_id":"","content":[]}`)
	toolResult, _ = sjson.SetBytes(toolResult, "tool_use_id", toolUseID)
	toolResult, _ = sjson.SetRawBytes(toolResult, "content", webSearchResultsFromGrounding(groundingMetadata))
	content, _ = sjson.SetRawBytes(content, "-1", toolResult)

	for _, block := range citedTextBlocks(textContent, parseGroundingSupports(groundingMetadata)) {
		if block.Text == "" {
			continue
		}
		textBlock := []byte(`{"type":"text","text":""}`)
		textBlock, _ = sjson.SetBytes(textBlock, "text", block.Text)
		if len(block.Citations) > 0 {
			citationsJSON, _ := json.Marshal(block.Citations)
			textBlock, _ = sjson.SetRawBytes(textBlock, "citations", citationsJSON)
		}
		content, _ = sjson.SetRawBytes(content, "-1", textBlock)
	}
	return content
}

// AppendClaudeWebSearchStreamBlocks emits the same blocks as
// BuildClaudeWebSearchContent, as Claude SSE events. It returns the next
// content block index.
func AppendClaudeWebSearchStreamBlocks(appendEvent func(event, payload string), startIndex int, toolUseID, textContent string, groundingMetadata gjson.Result) int {
	contentIndex := startIndex
	serverStart := fmt.Sprintf(`{"type":"content_block_start","index":%d,"content_block":{"type":"server_tool_use","id":"%s","name":"web_search","input":{}}}`, contentIndex, toolUseID)
	appendEvent("content_block_start", serverStart)
	if query := webSearchQueryFromGrounding(groundingMetadata); query != "" {
		queryJSON, _ := sjson.Set(`{}`, "query", query)
		inputDelta := fmt.Sprintf(`{"type":"content_block_delta","index":%d,"delta":{"type":"input_json_delta","partial_json":""}}`, contentIndex)
		inputDelta, _ = sjson.Set(inputDelta, "delta.partial_json", queryJSON)
		appendEvent("content_block_delta", inputDelta)
	}
	appendEvent("content_block_stop", fmt.Sprintf(`{"type":"content_block_stop","index":%d}`, contentIndex))
	contentIndex++

	resultStart := fmt.Sprintf(`{"type":"content_block_start","index":%d,"content_block":{"type":"web_search_tool_result","tool_use_id":"%s","content":[]}}`, contentIndex, toolUseID)
	resultStart, _ = sjson.SetRaw(resultStart, "content_block.content", string(webSearchResultsFromGrounding(groundingMetadata)))
	appendEvent("content_block_start", resultStart)
	appendEvent("content_block_stop", fmt.Sprintf(`{"type":"content_block_stop","index":%d}`, contentIndex))
	contentIndex++

	for _, block := range citedTextBlocks(textContent, parseGroundingSupports(groundingMetadata)) {
		if block.Text == "" {
			continue
		}
		textStart := fmt.Sprintf(`{"type":"content_block_start","index":%d,"content_block":{"type":"text","text":""}}`, contentIndex)
		if len(block.Citations) > 0 {
			textStart = fmt.Sprintf(`{"type":"content_block_start","index":%d,"content_block":{"citations":[],"type":"text","text":""}}`, contentIndex)
		}
		appendEvent("content_block_start", textStart)
		for _, citation := range block.Citations {
			citationJSON, _ := json.Marshal(citation)
			citationDelta := fmt.Sprintf(`{"type":"content_block_delta","index":%d,"delta":{"type":"citations_delta","citation":%s}}`, contentIndex, string(citationJSON))
			appendEvent("content_block_delta", citationDelta)
		}
		for _, chunk := range splitRunes(block.Text, 50) {
			textDelta := fmt.Sprintf(`{"type":"content_block_delta","index":%d,"delta":{"type":"text_delta","text":""}}`, contentIndex)
			textDelta, _ = sjson.Set(textDelta, "delta.text", chunk)
			appendEvent("content_block_delta", textDelta)
		}
		appendEvent("content_block_stop", fmt.Sprintf(`{"type":"content_block_stop","index":%d}`, contentIndex))
		contentIndex++
	}
	return contentIndex
}

func splitRunes(text string, chunkSize int) []string {
	if chunkSize <= 0 || text == "" {
		return nil
	}
	runes := []rune(text)
	chunks := make([]string, 0, (len(runes)+chunkSize-1)/chunkSize)
	for start := 0; start < len(runes); start += chunkSize {
		end := start + chunkSize
		if end > len(runes) {
			end = len(runes)
		}
		chunks = append(chunks, string(runes[start:end]))
	}
	return chunks
}
