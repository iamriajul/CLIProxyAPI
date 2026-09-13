package executor

import (
	"net/http"
	"strings"

	museauth "github.com/router-for-me/CLIProxyAPI/v7/internal/auth/muse"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/config"
	cliproxyauth "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/auth"
)

// museUserAgent is the neutral official-client fingerprint sent on Muse Model
// API requests when cloaking. It names the client family without fabricating
// version specifics, and replaces both Go's default transport UA and any
// foreign-harness UA that would otherwise identify Claude Code, the Claude
// Agent SDK, Codex, or other harnesses to Meta's server.
const museUserAgent = "muse-code"

// Muse cloak modes, mirroring the Claude cloak contract in miniature:
//   - "auto" (default): cloak unless the downstream request is already a
//     native Muse client.
//   - "always": cloak every request.
//   - "never": never cloak; upstream requests keep whatever identity the
//     transport would otherwise carry.
//
// Per-credential override lives in the muse auth JSON as "cloak_mode".
// The global disable-muse-cloak-mode config forces "never" for everything.
const (
	museCloakModeAuto   = "auto"
	museCloakModeAlways = "always"
	museCloakModeNever  = "never"
)

// resolveMuseCloakMode returns the effective cloak mode for a credential.
func resolveMuseCloakMode(cfg *config.Config, auth *cliproxyauth.Auth) string {
	if cfg != nil && cfg.DisableMuseCloakMode {
		return museCloakModeNever
	}
	if auth != nil {
		if auth.Attributes != nil {
			if mode := normalizeMuseCloakMode(auth.Attributes["cloak_mode"]); mode != "" {
				return mode
			}
		}
		if auth.Metadata != nil {
			if raw, ok := auth.Metadata["cloak_mode"].(string); ok {
				if mode := normalizeMuseCloakMode(raw); mode != "" {
					return mode
				}
			}
		}
	}
	return museCloakModeAuto
}

func normalizeMuseCloakMode(raw string) string {
	switch strings.ToLower(strings.TrimSpace(raw)) {
	case museCloakModeAuto:
		return museCloakModeAuto
	case museCloakModeAlways:
		return museCloakModeAlways
	case museCloakModeNever:
		return museCloakModeNever
	default:
		return ""
	}
}

// detectMuseNativeRequest reports whether downstream client headers already
// identify a native Muse client. Only an explicit muse product token counts;
// anything else (including empty) is treated as foreign.
func detectMuseNativeRequest(headers http.Header) bool {
	if len(headers) == 0 {
		return false
	}
	return strings.Contains(strings.ToLower(headers.Get("User-Agent")), "muse")
}

// museShouldCloak applies the mode contract for one request.
func museShouldCloak(mode string, native bool) bool {
	switch normalizeMuseCloakMode(mode) {
	case museCloakModeNever:
		return false
	case museCloakModeAlways:
		return true
	default:
		return !native
	}
}

// museCloakForRequest resolves whether the upstream request for auth should
// be cloaked given the downstream client headers (nil when unavailable).
func museCloakForRequest(cfg *config.Config, auth *cliproxyauth.Auth, clientHeaders http.Header) bool {
	return museShouldCloak(resolveMuseCloakMode(cfg, auth), detectMuseNativeRequest(clientHeaders))
}

// applyMuseCloakHeaders enforces the official-client fingerprint on an
// already-built upstream request. The x-api-version header is mandatory on
// the Model API and is (re)asserted here so custom-header expansion can never
// drop it; the User-Agent is pinned to the Muse family only when cloaking.
func applyMuseCloakHeaders(r *http.Request, cloak bool) {
	if r == nil {
		return
	}
	r.Header.Set("x-api-version", museauth.MuseAPIVersion)
	if cloak {
		r.Header.Set("User-Agent", museUserAgent)
	}
}
