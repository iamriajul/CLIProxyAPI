// Package sessionroute remembers which credential is serving each client
// session, and when and why the router moved a session to another credential.
//
// Records arrive on the usage plugin bus (see plugin.go), so the read side
// never consumes anything: the management usage queue pops records, and a
// client UI polling it would take them away from dashboards. State lives in
// memory only, bounded per session and in total, and expires with the same
// one-hour horizon as the default session-affinity TTL.
package sessionroute

import (
	"strings"
	"sync"
	"time"
)

const (
	// maxSessions bounds how many sessions keep a route in memory.
	maxSessions = 4096
	// maxSwitches bounds the switch history kept per session (newest win).
	maxSwitches = 8
	// DefaultTTL drops a session that has not been served for this long.
	DefaultTTL = time.Hour
)

// Reason explains why a session moved from one credential to another.
type Reason string

const (
	// ReasonQuota: the previous credential answered 429 in this session, or
	// it is marked quota-exceeded.
	ReasonQuota Reason = "quota"
	// ReasonCooldown: the previous credential is cooling down (unavailable or
	// waiting on a retry-after) without a quota signal.
	ReasonCooldown Reason = "cooldown"
	// ReasonError: the previous credential failed in this session with a
	// non-quota error.
	ReasonError Reason = "error"
	// ReasonRebind: the router moved the session with no failure or cooldown
	// observed: affinity expiry, the rebind policy, or no affinity at all.
	ReasonRebind Reason = "rebind"
)

// Switch is one move of a session from one credential to another.
type Switch struct {
	At           time.Time
	FromAuthID   string
	FromProvider string
	ToAuthID     string
	ToProvider   string
	Reason       Reason
	// StatusCode is the upstream status that caused the switch, zero when the
	// reason did not come from a failure observed in this session.
	StatusCode int
}

// Route is the credential currently serving a session.
type Route struct {
	SessionID string
	AuthID    string
	Provider  string
	Model     string
	// Since is when the current credential first served the session.
	Since time.Time
	// LastSeen is the newest successful request on the session.
	LastSeen time.Time
	// Switches is the recent switch history, oldest first.
	Switches []Switch
}

// AuthStateFunc reports why a credential is blocked right now, or "" when it
// is healthy or unknown. The server wires it to the auth manager so a session
// moved off a credential that another session cooled down still gets a cause.
type AuthStateFunc func(authID, model string) Reason

// Observation is one usage record reduced to what routing history needs.
type Observation struct {
	SessionID  string
	AuthID     string
	Provider   string
	Model      string
	At         time.Time
	Failed     bool
	StatusCode int
}

type entry struct {
	route Route
	// failures holds the last failure status per credential since the
	// session's last success, so the switch that follows names its cause.
	failures map[string]int
}

// Tracker is a concurrency-safe, bounded map of session routes.
type Tracker struct {
	mu        sync.Mutex
	sessions  map[string]*entry
	ttl       time.Duration
	now       func() time.Time
	authState AuthStateFunc
}

// New returns an empty tracker with the default TTL and the wall clock.
func New() *Tracker {
	return &Tracker{sessions: make(map[string]*entry), ttl: DefaultTTL, now: time.Now}
}

var defaultTracker = New()

// Default returns the process-wide tracker fed by the usage plugin.
func Default() *Tracker { return defaultTracker }

// SetAuthState installs the credential state lookup used to name a switch
// that no failure in the session explains.
func (t *Tracker) SetAuthState(fn AuthStateFunc) {
	if t == nil {
		return
	}
	t.mu.Lock()
	t.authState = fn
	t.mu.Unlock()
}

// SetClock replaces the clock. It exists for tests.
func (t *Tracker) SetClock(now func() time.Time) {
	if t == nil || now == nil {
		return
	}
	t.mu.Lock()
	t.now = now
	t.mu.Unlock()
}

// Reset clears every session. It exists for tests, which share the
// process-wide tracker.
func (t *Tracker) Reset() {
	if t == nil {
		return
	}
	t.mu.Lock()
	t.sessions = make(map[string]*entry)
	t.mu.Unlock()
}

// Observe folds one request outcome into its session's route.
func (t *Tracker) Observe(obs Observation) {
	if t == nil {
		return
	}
	sessionID := strings.TrimSpace(obs.SessionID)
	authID := strings.TrimSpace(obs.AuthID)
	if sessionID == "" || authID == "" {
		return
	}

	t.mu.Lock()
	defer t.mu.Unlock()
	now := t.now()
	at := obs.At
	if at.IsZero() {
		at = now
	}

	e, ok := t.sessions[sessionID]
	if ok && now.Sub(e.route.LastSeen) > t.ttl {
		delete(t.sessions, sessionID)
		ok = false
	}

	if obs.Failed {
		if !ok {
			// A failure alone does not name a serving credential; keep it
			// only for a session that already has one.
			return
		}
		e.failures[authID] = obs.StatusCode
		return
	}

	if !ok {
		if len(t.sessions) >= maxSessions {
			t.evictLocked(now)
		}
		t.sessions[sessionID] = &entry{
			route: Route{
				SessionID: sessionID, AuthID: authID, Provider: obs.Provider,
				Model: obs.Model, Since: at, LastSeen: at,
			},
			failures: make(map[string]int),
		}
		return
	}

	route := &e.route
	if authID != route.AuthID {
		// A late record of an earlier request must not read as a switch back.
		if at.Before(route.LastSeen) {
			return
		}
		reason, status := t.classifyLocked(e, route.AuthID, obs.Model)
		route.Switches = append(route.Switches, Switch{
			At: at, FromAuthID: route.AuthID, FromProvider: route.Provider,
			ToAuthID: authID, ToProvider: obs.Provider, Reason: reason, StatusCode: status,
		})
		if len(route.Switches) > maxSwitches {
			route.Switches = append([]Switch(nil), route.Switches[len(route.Switches)-maxSwitches:]...)
		}
		route.AuthID, route.Provider, route.Since = authID, obs.Provider, at
	}
	if at.After(route.LastSeen) {
		route.LastSeen = at
	}
	if strings.TrimSpace(obs.Model) != "" {
		route.Model = obs.Model
	}
	clear(e.failures)
}

// classifyLocked names why the session left fromAuthID. Callers hold t.mu.
func (t *Tracker) classifyLocked(e *entry, fromAuthID, model string) (Reason, int) {
	if status, failed := e.failures[fromAuthID]; failed {
		if status == 429 {
			return ReasonQuota, status
		}
		return ReasonError, status
	}
	if t.authState != nil {
		if reason := t.authState(fromAuthID, model); reason != "" {
			return reason, 0
		}
	}
	return ReasonRebind, 0
}

// evictLocked drops expired sessions, then the least recently served one if
// the map is still full. Callers hold t.mu.
func (t *Tracker) evictLocked(now time.Time) {
	var (
		victim   string
		victimAt time.Time
	)
	for id, e := range t.sessions {
		if now.Sub(e.route.LastSeen) > t.ttl {
			delete(t.sessions, id)
			continue
		}
		if victim == "" || e.route.LastSeen.Before(victimAt) {
			victim, victimAt = id, e.route.LastSeen
		}
	}
	if len(t.sessions) >= maxSessions && victim != "" {
		delete(t.sessions, victim)
	}
}

// Lookup returns the route serving sessionID. The bool is false for a session
// never served, or one idle past the TTL.
func (t *Tracker) Lookup(sessionID string) (Route, bool) {
	if t == nil {
		return Route{}, false
	}
	sessionID = strings.TrimSpace(sessionID)
	t.mu.Lock()
	defer t.mu.Unlock()
	e, ok := t.sessions[sessionID]
	if !ok {
		return Route{}, false
	}
	if t.now().Sub(e.route.LastSeen) > t.ttl {
		delete(t.sessions, sessionID)
		return Route{}, false
	}
	route := e.route
	route.Switches = append([]Switch(nil), e.route.Switches...)
	return route, true
}
