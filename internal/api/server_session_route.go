package api

import (
	"net/http"
	"strings"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/sessionroute"
	coreauth "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/auth"
)

// sessionRouteResponse is the body of GET /v1/session-route?session=<id>: the
// credential currently serving one client session and its recent switches.
//
// Compatibility policy (this endpoint is consumed by external integrations):
//   - The route and the required "session" query parameter are stable under
//     /v1. Changes are additive only: new optional fields may appear, existing
//     fields never change type or meaning.
//   - "session" is the canonical session id the router derives from the
//     client's headers: "claude:<X-Claude-Code-Session-Id>",
//     "codex:<session_id>", "affinity:<X-Opencode-Session>", and so on. Only
//     an exact match resolves.
//   - Account labels carry the same exposure as GET /v1/quota (masked name,
//     provider, type, plan); never tokens, emails or auth ids.
//   - "switches" is always an array, oldest first, at most 8 entries.
//   - "reason" vocabulary: "quota", "cooldown", "error", "rebind". Clients
//     MUST tolerate values they do not know.
//   - There is no in-body empty state: a session never served, or idle past
//     an hour, is 404 with {"error": string}. 400 means no session was given.
//   - The machine-readable contract lives in api/v1-session-route.schema.json
//     and is enforced by TestSessionRouteSchemaParity.
type sessionRouteResponse struct {
	Session string              `json:"session"`
	Account sessionRouteAccount `json:"account"`
	Model   string              `json:"model,omitempty"`
	// Since is when the current account first served the session.
	Since time.Time `json:"since"`
	// LastSeen is the newest successful request on the session.
	LastSeen time.Time            `json:"last_seen"`
	Switches []sessionRouteSwitch `json:"switches"`
}

// sessionRouteAccount labels a credential the way GET /v1/quota does. Name is
// absent once the credential has been removed from the proxy.
type sessionRouteAccount struct {
	Provider     string `json:"provider"`
	ProviderName string `json:"provider_name,omitempty"`
	Name         string `json:"name,omitempty"`
	Type         string `json:"type,omitempty"`
	Plan         string `json:"plan,omitempty"`
	InCooldown   bool   `json:"in_cooldown"`
	// CooldownUntil is when the cooldown ends, when the account is cooling and the end is known.
	CooldownUntil *time.Time `json:"cooldown_until,omitempty"`
}

type sessionRouteSwitch struct {
	At     time.Time           `json:"at"`
	From   sessionRouteAccount `json:"from"`
	To     sessionRouteAccount `json:"to"`
	Reason string              `json:"reason"`
	// StatusCode is the upstream status behind the switch, when one was seen.
	StatusCode int `json:"status_code,omitempty"`
}

// handleSessionRoute reports which credential serves a session. It reads the
// in-process tracker fed by the usage bus, so it never consumes usage records
// and never calls upstream; a client UI can poll it.
func (s *Server) handleSessionRoute(c *gin.Context) {
	session := strings.TrimSpace(c.Query("session"))
	if session == "" {
		c.JSON(http.StatusBadRequest, gin.H{"error": "session is required"})
		return
	}
	route, ok := sessionroute.Default().Lookup(session)
	if !ok {
		c.JSON(http.StatusNotFound, gin.H{"error": "no request served for session"})
		return
	}
	now := time.Now()
	switches := make([]sessionRouteSwitch, 0, len(route.Switches))
	for _, sw := range route.Switches {
		switches = append(switches, sessionRouteSwitch{
			At:         sw.At.UTC(),
			From:       s.sessionRouteAccount(sw.FromAuthID, sw.FromProvider, route.Model, now),
			To:         s.sessionRouteAccount(sw.ToAuthID, sw.ToProvider, route.Model, now),
			Reason:     string(sw.Reason),
			StatusCode: sw.StatusCode,
		})
	}
	c.JSON(http.StatusOK, sessionRouteResponse{
		Session:  route.SessionID,
		Account:  s.sessionRouteAccount(route.AuthID, route.Provider, route.Model, now),
		Model:    route.Model,
		Since:    route.Since.UTC(),
		LastSeen: route.LastSeen.UTC(),
		Switches: switches,
	})
}

func (s *Server) sessionRouteAuth(authID string) *coreauth.Auth {
	if s == nil || s.handlers == nil || s.handlers.AuthManager == nil {
		return nil
	}
	auth, ok := s.handlers.AuthManager.GetByID(authID)
	if !ok {
		return nil
	}
	return auth
}

func (s *Server) sessionRouteAccount(authID, provider, model string, now time.Time) sessionRouteAccount {
	auth := s.sessionRouteAuth(authID)
	if auth == nil {
		return sessionRouteAccount{Provider: provider, ProviderName: inferenceQuotaProviderDisplayName(provider)}
	}
	inCooldown, recoverAt := inferenceQuotaCooldown(auth, inferenceQuotaModelState(auth, model), now)
	return sessionRouteAccount{
		Provider:      strings.TrimSpace(auth.Provider),
		ProviderName:  inferenceQuotaProviderDisplayName(auth.Provider),
		Name:          inferenceQuotaAccountName(auth),
		Type:          inferenceQuotaAccountType(auth),
		Plan:          inferenceQuotaPlan(auth),
		InCooldown:    inCooldown,
		CooldownUntil: cooldownUntil(inCooldown, recoverAt),
	}
}

// sessionRouteAuthState names why a credential is blocked right now, for a
// switch that no failure in the session itself explains.
func (s *Server) sessionRouteAuthState(authID, model string) sessionroute.Reason {
	auth := s.sessionRouteAuth(authID)
	if auth == nil {
		return ""
	}
	modelState := inferenceQuotaModelState(auth, model)
	if blocked, _ := inferenceQuotaCooldown(auth, modelState, time.Now()); !blocked {
		return ""
	}
	if auth.Quota.Exceeded || (modelState != nil && modelState.Quota.Exceeded) {
		return sessionroute.ReasonQuota
	}
	return sessionroute.ReasonCooldown
}

// cooldownUntil reports a cooling account's recovery time, in UTC; nil when the account is not
// cooling or its recovery time is unknown. Shared by /v1/quota and /v1/session-route.
func cooldownUntil(inCooldown bool, recoverAt time.Time) *time.Time {
	if !inCooldown || recoverAt.IsZero() {
		return nil
	}
	at := recoverAt.UTC()
	return &at
}
