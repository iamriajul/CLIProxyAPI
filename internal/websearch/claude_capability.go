package websearch

import "strings"

const (
	// CPAClientHeader is the stable opt-in for CPA-only catalog fields.
	// Paseo's Anthropic model fetch already qualifies via its User-Agent;
	// this header is the contract that does not depend on that string.
	CPAClientHeader = "X-CPA-Client"

	// CPAClientPaseo is the X-CPA-Client value that asks for Paseo catalog fields.
	CPAClientPaseo = "paseo"

	// ClaudeWebSearchCapabilityQuery opts a model-list request into the
	// Claude web-search servability bit. "1" and "true" are accepted.
	ClaudeWebSearchCapabilityQuery = "cpa_capabilities"

	// ClaudeWebSearchCapabilityKey is the cpa_capabilities field that reports
	// whether this proxy will execute a Claude Messages server web_search tool
	// for that model. It is not the Codex catalog's web_search bit.
	ClaudeWebSearchCapabilityKey = "claude_web_search"

	// PaseoClaudeUserAgent is the User-Agent Paseo already sends on
	// GET /v1/models. A trailing version or token after this prefix also matches.
	PaseoClaudeUserAgent = "claude-cli/paseo"
)

// claudeWebSearchProbe is the nested side-query Claude Code sends: a
// search-only server tool, forced by name. Servability is decided against
// this shape, not against an empty or mixed tool list.
const claudeWebSearchProbe = `{"tools":[{"type":"web_search_20250305","name":"web_search"}],"tool_choice":{"type":"tool","name":"web_search"}}`

// RequestWantsClaudeWebSearchCapability reports whether a model-list caller
// should receive cpa_capabilities.claude_web_search. Official Claude Code
// fetches stay untouched.
func RequestWantsClaudeWebSearchCapability(userAgent, cpaClient, query string) bool {
	if strings.EqualFold(strings.TrimSpace(cpaClient), CPAClientPaseo) {
		return true
	}
	switch strings.ToLower(strings.TrimSpace(query)) {
	case "1", "true":
		return true
	}
	return isPaseoClaudeUserAgent(userAgent)
}

func isPaseoClaudeUserAgent(userAgent string) bool {
	ua := strings.TrimSpace(userAgent)
	if len(ua) < len(PaseoClaudeUserAgent) || !strings.EqualFold(ua[:len(PaseoClaudeUserAgent)], PaseoClaudeUserAgent) {
		return false
	}
	if len(ua) == len(PaseoClaudeUserAgent) {
		return true
	}
	switch ua[len(PaseoClaudeUserAgent)] {
	case '/', ' ', '+', ';':
		return true
	default:
		return false
	}
}

// ClaudeWebSearchServable reports whether a Claude Messages request that
// declares a server web_search tool for model will be executed by this proxy.
//
// True means every registered route executes it, either natively or through
// the proxy fallback. False means at least one route would drop the tool.
// Nil means there is no routing evidence; callers omit the field so an old
// or unknown catalog stays fail-closed.
//
// The check follows the request path. A native route suppresses fallback for
// the whole provider list, so a model that round-robins onto a non-native
// route is not servable even when fallback is enabled.
func ClaudeWebSearchServable(cfg Config, providers []string, model string) *bool {
	providers = compactProviders(providers)
	if len(providers) == 0 {
		return nil
	}
	payload := []byte(claudeWebSearchProbe)
	if NativeSearchSupported(FallbackFormatClaude, providers, model, payload) {
		for _, provider := range providers {
			if !NativeSearchSupported(FallbackFormatClaude, []string{provider}, model, payload) {
				return new(false)
			}
		}
		return new(true)
	}
	if claudeFallbackServes(cfg, providers, model, payload) {
		return new(true)
	}
	return new(false)
}

// AnnotateClaudeWebSearch sets cpa_capabilities.claude_web_search on each
// model whose servability is known. Models with no routes are left unchanged.
// model maps must be caller-owned; this mutates them.
func AnnotateClaudeWebSearch(models []map[string]any, cfg Config, providersFor func(string) []string) {
	if providersFor == nil {
		return
	}
	for _, model := range models {
		if model == nil {
			continue
		}
		id, _ := model["id"].(string)
		servable := ClaudeWebSearchServable(cfg, providersFor(id), id)
		if servable == nil {
			continue
		}
		caps, _ := model["cpa_capabilities"].(map[string]any)
		if caps == nil {
			caps = map[string]any{}
			model["cpa_capabilities"] = caps
		}
		caps[ClaudeWebSearchCapabilityKey] = *servable
	}
}

func claudeFallbackServes(cfg Config, providers []string, model string, payload []byte) bool {
	return ShouldFallback(cfg, FallbackFormatClaude, providers, model, payload, false) &&
		ShouldFallback(cfg, FallbackFormatClaude, providers, model, payload, true)
}

func compactProviders(providers []string) []string {
	if len(providers) == 0 {
		return nil
	}
	out := make([]string, 0, len(providers))
	for _, provider := range providers {
		provider = strings.TrimSpace(provider)
		if provider == "" {
			continue
		}
		out = append(out, provider)
	}
	if len(out) == 0 {
		return nil
	}
	return out
}
