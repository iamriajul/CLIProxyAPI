package common

import (
	"regexp"
	"strings"

	"github.com/router-for-me/CLIProxyAPI/v7/internal/registry"
)

// claudeWebSearchTypePattern matches a versioned Claude web_search tool
// type. Anthropic dates the version (web_search_20250305,
// web_search_20260209), so the date segment is matched rather than listed:
// a bare `web_search_` or an unversioned custom type is not a server tool
// declaration and must keep falling through to normal tool translation.
var claudeWebSearchTypePattern = regexp.MustCompile(`^web_search_\d{8}$`)

// IsClaudeWebSearchToolType reports whether a Claude tool type is a
// versioned server web_search declaration.
func IsClaudeWebSearchToolType(toolType string) bool {
	return claudeWebSearchTypePattern.MatchString(strings.ToLower(strings.TrimSpace(toolType)))
}

// IsResponsesWebSearchToolType reports whether an OpenAI Responses tool
// type is a web search declaration, including versioned and preview
// aliases.
func IsResponsesWebSearchToolType(toolType string) bool {
	switch strings.TrimSpace(toolType) {
	case "web_search", "web_search_2025_08_26", "web_search_preview", "web_search_preview_2025_03_11":
		return true
	default:
		return false
	}
}

// GeminiModelSupportsWebSearch reports whether a model can run native
// googleSearch grounding, checking static catalog capabilities first
// (explicit false vetoes) and dynamic probe flags second.
func GeminiModelSupportsWebSearch(modelID string) bool {
	info := registry.LookupModelInfo(modelID)
	infoAG := registry.LookupModelInfo(modelID, "antigravity")

	// 1. Explicit false in static definitions acts as an absolute veto.
	if info != nil && info.NativeCapabilities != nil && info.NativeCapabilities.WebSearch != nil && !*info.NativeCapabilities.WebSearch {
		return false
	}
	if infoAG != nil && infoAG.NativeCapabilities != nil && infoAG.NativeCapabilities.WebSearch != nil && !*infoAG.NativeCapabilities.WebSearch {
		return false
	}

	// 2. Explicit true in static definitions.
	if info != nil && info.NativeCapabilities != nil && info.NativeCapabilities.WebSearch != nil && *info.NativeCapabilities.WebSearch {
		return true
	}
	if infoAG != nil && infoAG.NativeCapabilities != nil && infoAG.NativeCapabilities.WebSearch != nil && *infoAG.NativeCapabilities.WebSearch {
		return true
	}

	// 3. Dynamic capability checks via Antigravity probes and registry flags.
	if registry.AntigravityWebSearchModelFor(modelID) != "" {
		return true
	}
	if (info != nil && info.SupportsWebSearch) || (infoAG != nil && infoAG.SupportsWebSearch) {
		return true
	}
	return false
}
