// Package quota provides live per-credential quota snapshots for the
// inference quota endpoint. Each supported provider has a Fetcher that talks
// to the provider's quota/usage API directly (the same sources the CPAMC
// management UI uses, minus the management api-call indirection) and
// normalizes the result into stable usage windows. Service adds TTL caching
// with stale fallback plus a global concurrency bound; callers fall back to
// passive signal snapshots whenever live data is unavailable.
package quota

import (
	"context"
	"math"
	"net/http"
	"strings"
	"time"

	coreauth "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/auth"
)

type Window struct {
	Name        string
	UsedPercent *float64
	ResetAt     *time.Time
	Status      string
}

// Snapshot is one credential's live quota data. ObservedAt is stamped by the
// Service at fetch time.
type Snapshot struct {
	Windows    []Window
	Plan       string
	ObservedAt time.Time
}

// for all upstream calls and MUST NOT log tokens or credentials.
type FetchRequest struct {
	Auth   *coreauth.Auth
	Client *http.Client
}

// Fetcher retrieves live quota for one canonical provider. Implementations
// MUST be safe for concurrent use, MUST bound response bodies (see DoJSON),
// and MUST return errors free of tokens, keys, and emails.
type Fetcher interface {
	// Provider returns the canonical provider key (see CanonicalProvider).
	Provider() string
	// Fetch returns the live snapshot or an error; a nil snapshot with a
	// nil error means "no quota data" (treated as unavailable).
	Fetch(ctx context.Context, req FetchRequest) (*Snapshot, error)
}

// RefreshMode selects live-fetch behavior for one lookup.
type RefreshMode int

const (
	// RefreshAuto serves a fresh cached snapshot, fetching live on
	// miss/stale and falling back to a recent stale snapshot on error.
	RefreshAuto RefreshMode = iota
	// RefreshLive bypasses the cache read (the fetch still populates it).
	RefreshLive
	// RefreshCached never fetches; it serves a fresh cached snapshot or misses.
	RefreshCached
)

// ParseRefreshMode maps the ?refresh= query value. Unknown values yield Auto.
func ParseRefreshMode(raw string) RefreshMode {
	switch strings.ToLower(strings.TrimSpace(raw)) {
	case "live", "force", "true", "1", "refresh":
		return RefreshLive
	case "cached", "cache", "false", "0":
		return RefreshCached
	default:
		return RefreshAuto
	}
}

// providerAliases maps every known provider spelling to its canonical key.
var providerAliases = map[string]string{
	"antigravity": "antigravity",
	"claude":      "claude",
	"codex":       "codex",
	"devin":       "devin",
	"kimi":        "kimi",
	"kimi-ai":     "kimi",
	"kimi.ai":     "kimi",
	"kimi.com":    "kimi",
	"meta":        "meta",
	"muse":        "meta",
	"opencode":    "opencode",
	"opencode-go": "opencode",
	"opencode_go": "opencode",
	"zai":         "zai",
	"glm":         "zai",
	"zhipu":       "zai",
	"xai":         "xai",
	"x-ai":        "xai",
	"grok":        "xai",
}

// CanonicalProvider returns the canonical fetcher key for a provider string,
// or "" when the provider has no live fetcher.
func CanonicalProvider(provider string) string {
	return providerAliases[strings.ToLower(strings.TrimSpace(provider))]
}

// BearerToken resolves the OAuth-style credential: metadata access_token,
// then attributes access_token/api_key, then metadata api_key.
func BearerToken(auth *coreauth.Auth) string {
	if auth == nil {
		return ""
	}
	if auth.Metadata != nil {
		if token, _ := auth.Metadata["access_token"].(string); strings.TrimSpace(token) != "" {
			return strings.TrimSpace(token)
		}
	}
	if auth.Attributes != nil {
		for _, key := range []string{"access_token", "api_key"} {
			if token := strings.TrimSpace(auth.Attributes[key]); token != "" {
				return token
			}
		}
	}
	if auth.Metadata != nil {
		if token, _ := auth.Metadata["api_key"].(string); strings.TrimSpace(token) != "" {
			return strings.TrimSpace(token)
		}
	}
	return ""
}

// APIKey resolves an API-key credential: attributes api_key first, then
// metadata api_key.
func APIKey(auth *coreauth.Auth) string {
	if auth == nil {
		return ""
	}
	if auth.Attributes != nil {
		if key := strings.TrimSpace(auth.Attributes["api_key"]); key != "" {
			return key
		}
	}
	if auth.Metadata != nil {
		if key, _ := auth.Metadata["api_key"].(string); strings.TrimSpace(key) != "" {
			return strings.TrimSpace(key)
		}
	}
	return ""
}

// RoundPercent rounds a percentage to two decimals, keeping integer values
// exact and taming float artifacts from fraction scaling (0.53*100).
func RoundPercent(value float64) float64 {
	return math.Round(value*100) / 100
}

// UsedPercent returns a rounded percentage pointer.
func UsedPercent(value float64) *float64 {
	rounded := RoundPercent(value)
	return &rounded
}

// Slugify lowercases text and collapses non-alphanumeric runs into single
// hyphens, for stable group/window slugs ("Gemini Models" to "gemini-models").
func Slugify(text string) string {
	var b strings.Builder
	b.Grow(len(text))
	dash := false
	for _, r := range strings.ToLower(strings.TrimSpace(text)) {
		if r >= 'a' && r <= 'z' || r >= '0' && r <= '9' {
			b.WriteRune(r)
			dash = false
			continue
		}
		if !dash {
			b.WriteRune('-')
			dash = true
		}
	}
	return strings.Trim(b.String(), "-")
}
