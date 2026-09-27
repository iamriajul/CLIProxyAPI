package handlers

import (
	"encoding/json"
	"os"
	"strings"

	"github.com/router-for-me/CLIProxyAPI/v7/internal/websearch"
	coreauth "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/auth"
)

// borrowedCredential lists a provider's login sessions, keyed by the
// provider name stored in the auth record.
type borrowedCredential struct {
	auths []*coreauth.Auth
	apply []func(cfg *websearch.Config, token string) bool
}

// routeSearchProviders maps a *route* provider name — what the request
// handlers actually pass, e.g. "claude" or "openai-compatible-kimi" — to
// the auth-store provider whose session can be borrowed, and the search
// key to fill in. Keying this by search ID ("anthropic") instead would
// never match a real route name and borrowing would silently do nothing.
//
// Borrowing is a last resort: an explicitly configured search key always
// wins, and this only fills in for deployments that already logged in to
// the model provider and would otherwise get no grounded search at all.
var routeSearchProviders = []struct {
	// route matches the prefix of the request's provider list entry.
	route string
	// auth is the provider name as recorded in the auth store.
	auth string
	// apply fills the borrowed token into the search configuration.
	apply func(cfg *websearch.Config, token string) bool
}{
	{route: "claude", auth: "claude", apply: func(cfg *websearch.Config, token string) bool { return fillIfEmpty(&cfg.AnthropicAPIKey, token) }},
	{route: "anthropic", auth: "claude", apply: func(cfg *websearch.Config, token string) bool { return fillIfEmpty(&cfg.AnthropicAPIKey, token) }},
	{route: "gemini", auth: "gemini", apply: func(cfg *websearch.Config, token string) bool { return fillIfEmpty(&cfg.GeminiAPIKey, token) }},
	{route: "antigravity", auth: "antigravity", apply: func(cfg *websearch.Config, token string) bool { return fillIfEmpty(&cfg.GeminiAPIKey, token) }},
	{route: "codex", auth: "codex", apply: func(cfg *websearch.Config, token string) bool { return fillIfEmpty(&cfg.CodexAPIKey, token) }},
	{route: "openai", auth: "codex", apply: func(cfg *websearch.Config, token string) bool { return fillIfEmpty(&cfg.CodexAPIKey, token) }},
	{route: "xai", auth: "xai", apply: func(cfg *websearch.Config, token string) bool { return fillIfEmpty(&cfg.XAIAPIKey, token) }},
	{route: "zai", auth: "zai", apply: func(cfg *websearch.Config, token string) bool { return fillIfEmpty(&cfg.ZAIAPIKey, token) }},
	{route: "kimi", auth: "kimi", apply: func(cfg *websearch.Config, token string) bool { return fillIfEmpty(&cfg.KimiAPIKey, token) }},
}

// borrowedSearchCredentials returns the usable login sessions for the given
// route providers, each paired with the search key it should fill. A
// session marked disabled or unavailable is skipped: it is a credential the
// operator has already taken out of rotation, and using it for search would
// burn quota the model traffic no longer needs.
func borrowedSearchCredentials(manager *coreauth.Manager, providers []string) borrowedCredential {
	out := borrowedCredential{}
	if manager == nil || len(providers) == 0 {
		return out
	}
	auths := manager.List()
	if len(auths) == 0 {
		return out
	}
	for _, route := range providers {
		route = strings.ToLower(strings.TrimSpace(route))
		if route == "" {
			continue
		}
		for _, candidate := range routeSearchProviders {
			if !strings.HasPrefix(route, candidate.route) {
				continue
			}
			if auth := firstUsableSession(auths, candidate.auth); auth != nil {
				out.auths = append(out.auths, auth)
				out.apply = append(out.apply, candidate.apply)
			}
			break
		}
	}
	return out
}

// fillIfEmpty sets a search key only when the operator left it blank. A
// configured key is a deliberate choice and must survive borrowing.
func fillIfEmpty(dst *string, token string) bool {
	if strings.TrimSpace(*dst) != "" {
		return false
	}
	*dst = token
	return true
}

// firstUsableSession returns one live session for an auth provider, or nil.
func firstUsableSession(auths []*coreauth.Auth, provider string) *coreauth.Auth {
	for _, auth := range auths {
		if auth == nil || auth.Disabled || auth.Unavailable {
			continue
		}
		if strings.TrimSpace(auth.Provider) != provider {
			continue
		}
		// One session per provider is enough: search requests are
		// independent of each other and cycling the rest would only
		// spread rate-limit rejections.
		return auth
	}
	return nil
}

// applyBorrowedSearchCredentials fills in provider credentials that the
// search configuration left empty, using sessions already present in the
// auth store. It mutates cfg in place and reports whether anything changed.
func applyBorrowedSearchCredentials(cfg *websearch.Config, manager *coreauth.Manager, providers []string) bool {
	borrowed := borrowedSearchCredentials(manager, providers)
	if len(borrowed.auths) == 0 {
		return false
	}
	changed := false
	for index, auth := range borrowed.auths {
		if index >= len(borrowed.apply) {
			break
		}
		access := authSearchAccessToken(auth)
		if access == "" {
			continue
		}
		if borrowed.apply[index](cfg, access) {
			changed = true
		}
	}
	return changed
}

// authSearchAccessToken reads the bearer credential out of a login session.
//
// The auth file is the authority here, not the in-memory Metadata: Claude
// and Codex register their token straight into TokenStorage and leave
// Metadata holding only identity fields, so a session created by a login in
// this process would otherwise look credential-less. Reading the backing
// file covers both cases, and the metadata is only a fallback for records
// with no readable file (a test double, or a store that has not flushed).
func authSearchAccessToken(auth *coreauth.Auth) string {
	if auth == nil {
		return ""
	}
	if token := credentialFileAccessToken(auth.FileName); token != "" {
		return token
	}
	for _, key := range []string{"access_token", "accessToken", "api_key", "apiKey", "token"} {
		if value, okLookup := auth.Metadata[key]; okLookup {
			if text, okString := value.(string); okString {
				if trimmed := strings.TrimSpace(text); trimmed != "" {
					return trimmed
				}
			}
		}
	}
	return ""
}

// credentialFileAccessToken reads the access token out of a stored auth
// file. A missing or malformed file yields an empty token: borrowing is a
// best-effort fill, and a credential we cannot read is one we must not use.
func credentialFileAccessToken(path string) string {
	if strings.TrimSpace(path) == "" {
		return ""
	}
	data, errRead := os.ReadFile(path)
	if errRead != nil {
		return ""
	}
	var document map[string]any
	if errUnmarshal := json.Unmarshal(data, &document); errUnmarshal != nil {
		return ""
	}
	for _, key := range []string{"access_token", "accessToken", "api_key", "apiKey", "token"} {
		if text, okString := document[key].(string); okString {
			if trimmed := strings.TrimSpace(text); trimmed != "" {
				return trimmed
			}
		}
	}
	return ""
}
