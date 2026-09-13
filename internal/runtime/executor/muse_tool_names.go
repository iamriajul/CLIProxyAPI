package executor

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"strconv"
	"strings"

	"github.com/router-for-me/CLIProxyAPI/v7/internal/runtime/executor/helps"
	sdktranslator "github.com/router-for-me/CLIProxyAPI/v7/sdk/translator"
	"github.com/tidwall/gjson"
	"github.com/tidwall/sjson"
)

// museMaxToolNameLen is the Meta Model API limit for tool function names.
// Longer names (e.g. Claude Agent SDK mcp__server__tool names) are rejected
// with 400 "`name` must be at most 64 characters".
const museMaxToolNameLen = 64

// museToolNameCharset reports whether every rune is accepted in a Meta tool name.
func museToolNameCharset(name string) bool {
	if name == "" {
		return false
	}
	for _, r := range name {
		if (r >= 'a' && r <= 'z') || (r >= 'A' && r <= 'Z') ||
			(r >= '0' && r <= '9') || r == '_' || r == '-' {
			continue
		}
		return false
	}
	return true
}

// museToolNameNeedsShortening reports whether a name cannot go upstream as-is.
func museToolNameNeedsShortening(name string) bool {
	return len([]rune(name)) > museMaxToolNameLen || !museToolNameCharset(name)
}

// shortenMuseToolName deterministically compresses a name to the Meta limit:
// sanitized head plus a hex hash suffix, mirroring the Codex input-ID pattern.
func shortenMuseToolName(name string, attempt int) string {
	var head strings.Builder
	for _, r := range name {
		if (r >= 'a' && r <= 'z') || (r >= 'A' && r <= 'Z') ||
			(r >= '0' && r <= '9') || r == '_' || r == '-' {
			head.WriteRune(r)
		} else {
			head.WriteRune('_')
		}
	}
	hashInput := name
	if attempt > 0 {
		hashInput += "\x00" + strconv.Itoa(attempt)
	}
	sum := sha256.Sum256([]byte(hashInput))
	suffix := "_" + hex.EncodeToString(sum[:8])
	headRunes := []rune(head.String())
	maxHead := museMaxToolNameLen - len(suffix)
	if len(headRunes) > maxHead {
		headRunes = headRunes[:maxHead]
	}
	short := string(headRunes) + suffix
	if short == "_" || short == "" {
		short = "tool" + suffix
	}
	return short
}

// museToolNamer assigns deterministic upstream names, tracking the reverse
// map needed to restore originals on responses. Compliant names pass through
// untouched; everything else shortens, with attempt probing on collision.
type museToolNamer struct {
	forward map[string]string
	reverse map[string]string
	used    map[string]bool
}

func newMuseToolNamer() *museToolNamer {
	return &museToolNamer{
		forward: make(map[string]string),
		reverse: make(map[string]string),
		used:    make(map[string]bool),
	}
}

func (n *museToolNamer) alias(original string) string {
	if short, ok := n.forward[original]; ok {
		return short
	}
	short := original
	if museToolNameNeedsShortening(original) {
		short = shortenMuseToolName(original, 0)
		for attempt := 1; n.used[short]; attempt++ {
			short = shortenMuseToolName(original, attempt)
		}
	} else if n.used[short] {
		// A compliant name colliding with an allocated alias: disambiguate
		// the compliant one too so every upstream name is unique.
		short = shortenMuseToolName(original, 0)
		for attempt := 1; n.used[short]; attempt++ {
			short = shortenMuseToolName(original, attempt)
		}
	}
	n.forward[original] = short
	n.used[short] = true
	if short != original {
		if _, exists := n.reverse[short]; !exists {
			n.reverse[short] = original
		}
	}
	return short
}

// remapMuseToolNames rewrites overlong tool names in an OpenAI/Responses
// request body to Meta-compliant aliases. It covers declarations, tool_choice
// references, and history references so the upstream request is consistent.
// It returns the rewritten body and the reverse map (upstream -> original)
// for response restoration; the map is nil when nothing needed renaming.
func remapMuseToolNames(body []byte) ([]byte, map[string]string) {
	if len(body) == 0 || !gjson.ValidBytes(body) {
		return body, nil
	}
	namer := newMuseToolNamer()
	changed := false

	renameAt := func(raw []byte, path string) []byte {
		name := gjson.GetBytes(raw, path)
		if name.Type != gjson.String || strings.TrimSpace(name.String()) == "" {
			return raw
		}
		original := name.String()
		short := namer.alias(original)
		if short == original {
			return raw
		}
		updated, err := sjson.SetBytes(raw, path, short)
		if err != nil {
			return raw
		}
		changed = true
		return updated
	}

	// Declarations first so compliant names reserve their slots.
	arrayPaths := []struct {
		array string
		name  string
	}{
		{"tools", "function.name"},
		{"tools", "custom.name"},
		{"tools", "name"},
		{"functions", "name"},
	}
	for _, ap := range arrayPaths {
		items := gjson.GetBytes(body, ap.array)
		if !items.Exists() || !items.IsArray() {
			continue
		}
		updatedItems := make([]string, 0, len(items.Array()))
		itemsChanged := false
		for _, item := range items.Array() {
			// For flat tools[].name, only touch function/custom declarations,
			// never bare strings like tool_choice literals elsewhere.
			if ap.name == "name" && item.IsObject() {
				toolType := strings.ToLower(strings.TrimSpace(item.Get("type").String()))
				if toolType != "" && toolType != "function" && toolType != "custom" {
					updatedItems = append(updatedItems, item.Raw)
					continue
				}
			}
			next := renameAt([]byte(item.Raw), ap.name)
			if string(next) != item.Raw {
				itemsChanged = true
			}
			updatedItems = append(updatedItems, string(next))
		}
		if itemsChanged {
			if updated, err := sjson.SetRawBytes(body, ap.array, helps.JoinRawJSONStrings(updatedItems)); err == nil {
				body = updated
				changed = true
			}
		}
	}

	// tool_choice references (object forms only; bare "auto"/"required" pass through).
	body = renameToolChoiceName(body, namer, &changed)

	// History references: assistant tool_calls and Responses function_call items.
	messages := gjson.GetBytes(body, "messages")
	if messages.Exists() && messages.IsArray() {
		updatedMessages := make([]string, 0, len(messages.Array()))
		messagesChanged := false
		for _, msg := range messages.Array() {
			raw := []byte(msg.Raw)
			calls := msg.Get("tool_calls")
			if calls.Exists() && calls.IsArray() {
				updatedCalls := make([]string, 0, len(calls.Array()))
				callsChanged := false
				for _, call := range calls.Array() {
					next := renameAt([]byte(call.Raw), "function.name")
					if string(next) != call.Raw {
						callsChanged = true
					}
					updatedCalls = append(updatedCalls, string(next))
				}
				if callsChanged {
					if updated, err := sjson.SetRawBytes(raw, "tool_calls", helps.JoinRawJSONStrings(updatedCalls)); err == nil {
						raw = updated
						messagesChanged = true
					}
				}
			}
			updatedMessages = append(updatedMessages, string(raw))
		}
		if messagesChanged {
			if updated, err := sjson.SetRawBytes(body, "messages", helps.JoinRawJSONStrings(updatedMessages)); err == nil {
				body = updated
				changed = true
			}
		}
	}
	input := gjson.GetBytes(body, "input")
	if input.Exists() && input.IsArray() {
		updatedInput := make([]string, 0, len(input.Array()))
		inputChanged := false
		for _, item := range input.Array() {
			raw := []byte(item.Raw)
			if strings.EqualFold(strings.TrimSpace(item.Get("type").String()), "function_call") {
				next := renameAt(raw, "name")
				if string(next) != item.Raw {
					raw = next
					inputChanged = true
				}
			}
			updatedInput = append(updatedInput, string(raw))
		}
		if inputChanged {
			if updated, err := sjson.SetRawBytes(body, "input", helps.JoinRawJSONStrings(updatedInput)); err == nil {
				body = updated
				changed = true
			}
		}
	}

	if !changed || len(namer.reverse) == 0 {
		return body, nil
	}
	return body, namer.reverse
}

// renameToolChoiceName rewrites tool_choice object references via the namer.
func renameToolChoiceName(body []byte, namer *museToolNamer, changed *bool) []byte {
	choice := gjson.GetBytes(body, "tool_choice")
	if !choice.Exists() || !choice.IsObject() {
		return body
	}
	for _, path := range []string{"function.name", "name"} {
		name := choice.Get(path)
		if name.Type != gjson.String || strings.TrimSpace(name.String()) == "" {
			continue
		}
		original := name.String()
		short := namer.alias(original)
		if short == original {
			continue
		}
		if updated, err := sjson.SetBytes(body, "tool_choice."+path, short); err == nil {
			body = updated
			*changed = true
		}
	}
	return body
}

// restoreMuseToolNamesInJSON restores original tool names in a translated
// response payload for the given downstream format. It is a no-op when the
// reverse map is empty.
func restoreMuseToolNamesInJSON(payload []byte, downstream sdktranslator.Format, reverseMap map[string]string) []byte {
	if len(reverseMap) == 0 || len(payload) == 0 || !gjson.ValidBytes(payload) {
		return payload
	}
	lookup := func(name string) (string, bool) {
		original, ok := reverseMap[name]
		return original, ok
	}
	switch downstream {
	case sdktranslator.FormatOpenAIResponse, sdktranslator.FormatCodex:
		payload = restoreResponsesOutputNames(payload, lookup)
	case sdktranslator.FormatClaude:
		payload = restoreClaudeContentNames(payload, lookup)
	case sdktranslator.FormatGemini:
		payload = restoreGeminiPartNames(payload, lookup)
	default:
		payload = restoreOpenAIChatNames(payload, lookup)
	}
	return payload
}

// restoreMuseToolNamesInStreamLine restores original tool names in one
// translated SSE line (or returns it unchanged when there is nothing to do).
// A trailing newline is preserved so downstream SSE framing is unaffected.
func restoreMuseToolNamesInStreamLine(line []byte, downstream sdktranslator.Format, reverseMap map[string]string) []byte {
	if len(reverseMap) == 0 {
		return line
	}
	newline := bytes.HasSuffix(line, []byte("\n"))
	trimmed := bytes.TrimSpace(line)
	if len(trimmed) == 0 || bytes.Equal(trimmed, []byte("[DONE]")) {
		return line
	}
	payload := trimmed
	prefix := []byte(nil)
	if bytes.HasPrefix(trimmed, []byte("data:")) {
		prefix = []byte("data: ")
		payload = bytes.TrimSpace(trimmed[len("data:"):])
		if len(payload) == 0 || bytes.Equal(payload, []byte("[DONE]")) {
			return line
		}
	}
	if !gjson.ValidBytes(payload) {
		return line
	}
	var updated []byte
	switch downstream {
	case sdktranslator.FormatOpenAIResponse, sdktranslator.FormatCodex:
		updated = restoreResponsesEventNames(payload, lookupMuseReverse(reverseMap))
	case sdktranslator.FormatClaude:
		updated = restoreClaudeEventNames(payload, lookupMuseReverse(reverseMap))
	case sdktranslator.FormatGemini:
		updated = restoreGeminiPartNames(payload, lookupMuseReverse(reverseMap))
	default:
		updated = restoreOpenAIChatDeltaNames(payload, lookupMuseReverse(reverseMap))
	}
	if string(updated) == string(payload) {
		return line
	}
	var out []byte
	if prefix != nil {
		out = append(prefix, updated...)
	} else {
		out = updated
	}
	if newline {
		out = append(out, '\n')
	}
	return out
}

func lookupMuseReverse(reverseMap map[string]string) func(string) (string, bool) {
	return func(name string) (string, bool) {
		original, ok := reverseMap[name]
		return original, ok
	}
}

// restoreOpenAIChatNames restores choices[].message.tool_calls[].function.name.
func restoreOpenAIChatNames(payload []byte, lookup func(string) (string, bool)) []byte {
	return restoreOpenAIChatCallNames(payload, "choices", "message.tool_calls", lookup)
}

// restoreOpenAIChatDeltaNames restores choices[].delta.tool_calls[].function.name.
func restoreOpenAIChatDeltaNames(payload []byte, lookup func(string) (string, bool)) []byte {
	return restoreOpenAIChatCallNames(payload, "choices", "delta.tool_calls", lookup)
}

func restoreOpenAIChatCallNames(payload []byte, choicesPath, callsPath string, lookup func(string) (string, bool)) []byte {
	choices := gjson.GetBytes(payload, choicesPath)
	if !choices.Exists() || !choices.IsArray() {
		return payload
	}
	updatedChoices := make([]string, 0, len(choices.Array()))
	changed := false
	for _, choice := range choices.Array() {
		raw := []byte(choice.Raw)
		calls := choice.Get(callsPath)
		if calls.Exists() && calls.IsArray() {
			updatedCalls := make([]string, 0, len(calls.Array()))
			callsChanged := false
			for _, call := range calls.Array() {
				callRaw := []byte(call.Raw)
				name := call.Get("function.name")
				if name.Type == gjson.String {
					if original, ok := lookup(name.String()); ok {
						if updated, err := sjson.SetBytes(callRaw, "function.name", original); err == nil {
							callRaw = updated
							callsChanged = true
						}
					}
				}
				updatedCalls = append(updatedCalls, string(callRaw))
			}
			if callsChanged {
				if updated, err := sjson.SetRawBytes(raw, callsPath, helps.JoinRawJSONStrings(updatedCalls)); err == nil {
					raw = updated
					changed = true
				}
			}
		}
		updatedChoices = append(updatedChoices, string(raw))
	}
	if !changed {
		return payload
	}
	if updated, err := sjson.SetRawBytes(payload, choicesPath, helps.JoinRawJSONStrings(updatedChoices)); err == nil {
		return updated
	}
	return payload
}

// restoreResponsesOutputNames restores output[] function_call item names.
func restoreResponsesOutputNames(payload []byte, lookup func(string) (string, bool)) []byte {
	output := gjson.GetBytes(payload, "output")
	if !output.Exists() || !output.IsArray() {
		return payload
	}
	updatedOutput := make([]string, 0, len(output.Array()))
	changed := false
	for _, item := range output.Array() {
		raw := []byte(item.Raw)
		if strings.EqualFold(strings.TrimSpace(item.Get("type").String()), "function_call") {
			name := item.Get("name")
			if name.Type == gjson.String {
				if original, ok := lookup(name.String()); ok {
					if updated, err := sjson.SetBytes(raw, "name", original); err == nil {
						raw = updated
						changed = true
					}
				}
			}
		}
		updatedOutput = append(updatedOutput, string(raw))
	}
	if !changed {
		return payload
	}
	if updated, err := sjson.SetRawBytes(payload, "output", helps.JoinRawJSONStrings(updatedOutput)); err == nil {
		return updated
	}
	return payload
}

// restoreResponsesEventNames restores names in Responses stream events:
// output_item.added/done items and completed/incomplete response outputs.
func restoreResponsesEventNames(payload []byte, lookup func(string) (string, bool)) []byte {
	item := gjson.GetBytes(payload, "item")
	if item.Exists() && item.IsObject() {
		if strings.EqualFold(strings.TrimSpace(item.Get("type").String()), "function_call") {
			name := item.Get("name")
			if name.Type == gjson.String {
				if original, ok := lookup(name.String()); ok {
					if updated, err := sjson.SetBytes(payload, "item.name", original); err == nil {
						return updated
					}
				}
			}
		}
		return payload
	}
	response := gjson.GetBytes(payload, "response")
	if response.Exists() && response.IsObject() {
		restored := restoreResponsesOutputNames([]byte(response.Raw), lookup)
		if string(restored) != response.Raw {
			if updated, err := sjson.SetRawBytes(payload, "response", restored); err == nil {
				return updated
			}
		}
	}
	return payload
}

// restoreClaudeContentNames restores content[] tool_use block names.
func restoreClaudeContentNames(payload []byte, lookup func(string) (string, bool)) []byte {
	content := gjson.GetBytes(payload, "content")
	if !content.Exists() || !content.IsArray() {
		return payload
	}
	updatedContent := make([]string, 0, len(content.Array()))
	changed := false
	for _, block := range content.Array() {
		raw := []byte(block.Raw)
		if strings.EqualFold(strings.TrimSpace(block.Get("type").String()), "tool_use") {
			name := block.Get("name")
			if name.Type == gjson.String {
				if original, ok := lookup(name.String()); ok {
					if updated, err := sjson.SetBytes(raw, "name", original); err == nil {
						raw = updated
						changed = true
					}
				}
			}
		}
		updatedContent = append(updatedContent, string(raw))
	}
	if !changed {
		return payload
	}
	if updated, err := sjson.SetRawBytes(payload, "content", helps.JoinRawJSONStrings(updatedContent)); err == nil {
		return updated
	}
	return payload
}

// restoreClaudeEventNames restores content_block_start tool_use names.
func restoreClaudeEventNames(payload []byte, lookup func(string) (string, bool)) []byte {
	block := gjson.GetBytes(payload, "content_block")
	if !block.Exists() || !block.IsObject() {
		return payload
	}
	if !strings.EqualFold(strings.TrimSpace(block.Get("type").String()), "tool_use") {
		return payload
	}
	name := block.Get("name")
	if name.Type != gjson.String {
		return payload
	}
	if original, ok := lookup(name.String()); ok {
		if updated, err := sjson.SetBytes(payload, "content_block.name", original); err == nil {
			return updated
		}
	}
	return payload
}

// restoreGeminiPartNames restores candidates[].content.parts[].functionCall.name.
// Gemini uses the same shape for full responses and stream events.
func restoreGeminiPartNames(payload []byte, lookup func(string) (string, bool)) []byte {
	candidates := gjson.GetBytes(payload, "candidates")
	if !candidates.Exists() || !candidates.IsArray() {
		return payload
	}
	updatedCandidates := make([]string, 0, len(candidates.Array()))
	changed := false
	for _, candidate := range candidates.Array() {
		raw := []byte(candidate.Raw)
		parts := candidate.Get("content.parts")
		if parts.Exists() && parts.IsArray() {
			updatedParts := make([]string, 0, len(parts.Array()))
			partsChanged := false
			for _, part := range parts.Array() {
				partRaw := []byte(part.Raw)
				call := part.Get("functionCall")
				if call.Exists() && call.IsObject() {
					name := call.Get("name")
					if name.Type == gjson.String {
						if original, ok := lookup(name.String()); ok {
							if updated, err := sjson.SetBytes(partRaw, "functionCall.name", original); err == nil {
								partRaw = updated
								partsChanged = true
							}
						}
					}
				}
				updatedParts = append(updatedParts, string(partRaw))
			}
			if partsChanged {
				if updated, err := sjson.SetRawBytes(raw, "content.parts", helps.JoinRawJSONStrings(updatedParts)); err == nil {
					raw = updated
					changed = true
				}
			}
		}
		updatedCandidates = append(updatedCandidates, string(raw))
	}
	if !changed {
		return payload
	}
	if updated, err := sjson.SetRawBytes(payload, "candidates", helps.JoinRawJSONStrings(updatedCandidates)); err == nil {
		return updated
	}
	return payload
}
