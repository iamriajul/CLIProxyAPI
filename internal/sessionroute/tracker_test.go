package sessionroute

import (
	"fmt"
	"testing"
	"time"
)

type fakeClock struct{ now time.Time }

func (c *fakeClock) Now() time.Time { return c.now }

func newTestTracker(t *testing.T) (*Tracker, *fakeClock) {
	t.Helper()
	clock := &fakeClock{now: time.Date(2026, 11, 6, 12, 0, 0, 0, time.UTC)}
	tracker := New()
	tracker.SetClock(clock.Now)
	return tracker, clock
}

func served(session, auth string, at time.Time) Observation {
	return Observation{SessionID: session, AuthID: auth, Provider: "claude", Model: "claude-haiku-5-5", At: at}
}

func TestTrackerReportsServingAccountWithoutSwitches(t *testing.T) {
	tracker, clock := newTestTracker(t)
	tracker.Observe(served("claude:s1", "auth-a", clock.now))
	tracker.Observe(served("claude:s1", "auth-a", clock.now.Add(time.Second)))

	route, ok := tracker.Lookup("claude:s1")
	if !ok {
		t.Fatal("session not found")
	}
	if route.AuthID != "auth-a" || len(route.Switches) != 0 {
		t.Fatalf("route = %+v, want auth-a with no switches", route)
	}
	if !route.Since.Equal(clock.now) || !route.LastSeen.Equal(clock.now.Add(time.Second)) {
		t.Fatalf("since/last_seen = %v/%v", route.Since, route.LastSeen)
	}
}

func TestTrackerNamesSwitchCause(t *testing.T) {
	cases := []struct {
		name       string
		failStatus int
		authState  Reason
		want       Reason
		wantStatus int
	}{
		{name: "429 in session", failStatus: 429, want: ReasonQuota, wantStatus: 429},
		{name: "5xx in session", failStatus: 529, want: ReasonError, wantStatus: 529},
		{name: "cooled by another session", authState: ReasonCooldown, want: ReasonCooldown},
		{name: "quota-exceeded elsewhere", authState: ReasonQuota, want: ReasonQuota},
		{name: "no cause observed", want: ReasonRebind},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			tracker, clock := newTestTracker(t)
			tracker.SetAuthState(func(authID, _ string) Reason {
				if authID == "auth-a" {
					return tc.authState
				}
				return ""
			})
			tracker.Observe(served("claude:s1", "auth-a", clock.now))
			if tc.failStatus != 0 {
				tracker.Observe(Observation{SessionID: "claude:s1", AuthID: "auth-a", Failed: true, StatusCode: tc.failStatus, At: clock.now.Add(time.Second)})
			}
			tracker.Observe(served("claude:s1", "auth-b", clock.now.Add(2*time.Second)))

			route, _ := tracker.Lookup("claude:s1")
			if route.AuthID != "auth-b" || len(route.Switches) != 1 {
				t.Fatalf("route = %+v, want one switch to auth-b", route)
			}
			sw := route.Switches[0]
			if sw.FromAuthID != "auth-a" || sw.ToAuthID != "auth-b" || sw.Reason != tc.want || sw.StatusCode != tc.wantStatus {
				t.Fatalf("switch = %+v, want a->b %s/%d", sw, tc.want, tc.wantStatus)
			}
			if !route.Since.Equal(clock.now.Add(2 * time.Second)) {
				t.Fatalf("since = %v, want the switch time", route.Since)
			}
		})
	}
}

func TestTrackerFailureIsForgottenAfterSuccess(t *testing.T) {
	tracker, clock := newTestTracker(t)
	tracker.Observe(served("claude:s1", "auth-a", clock.now))
	tracker.Observe(Observation{SessionID: "claude:s1", AuthID: "auth-a", Failed: true, StatusCode: 429, At: clock.now.Add(time.Second)})
	tracker.Observe(served("claude:s1", "auth-a", clock.now.Add(2*time.Second)))
	tracker.Observe(served("claude:s1", "auth-b", clock.now.Add(3*time.Second)))

	route, _ := tracker.Lookup("claude:s1")
	if got := route.Switches[0].Reason; got != ReasonRebind {
		t.Fatalf("reason = %s, want rebind: the 429 was recovered from before the switch", got)
	}
}

func TestTrackerLateRecordIsNotASwitchBack(t *testing.T) {
	tracker, clock := newTestTracker(t)
	tracker.Observe(served("claude:s1", "auth-a", clock.now))
	tracker.Observe(served("claude:s1", "auth-b", clock.now.Add(2*time.Second)))
	tracker.Observe(served("claude:s1", "auth-a", clock.now.Add(time.Second)))

	route, _ := tracker.Lookup("claude:s1")
	if route.AuthID != "auth-b" || len(route.Switches) != 1 {
		t.Fatalf("route = %+v, want auth-b with one switch", route)
	}
}

func TestTrackerFailureAloneDoesNotCreateSession(t *testing.T) {
	tracker, clock := newTestTracker(t)
	tracker.Observe(Observation{SessionID: "claude:s1", AuthID: "auth-a", Failed: true, StatusCode: 429, At: clock.now})
	if _, ok := tracker.Lookup("claude:s1"); ok {
		t.Fatal("a failed request alone must not name a serving account")
	}
}

func TestTrackerBoundsSwitchHistory(t *testing.T) {
	tracker, clock := newTestTracker(t)
	for i := 0; i <= maxSwitches+3; i++ {
		tracker.Observe(served("claude:s1", fmt.Sprintf("auth-%d", i), clock.now.Add(time.Duration(i)*time.Second)))
	}
	route, _ := tracker.Lookup("claude:s1")
	if len(route.Switches) != maxSwitches {
		t.Fatalf("switches = %d, want %d", len(route.Switches), maxSwitches)
	}
	if last := route.Switches[maxSwitches-1]; last.ToAuthID != fmt.Sprintf("auth-%d", maxSwitches+3) {
		t.Fatalf("newest switch = %+v, want the latest account", last)
	}
}

func TestTrackerExpiresIdleSessions(t *testing.T) {
	tracker, clock := newTestTracker(t)
	tracker.Observe(served("claude:s1", "auth-a", clock.now))
	clock.now = clock.now.Add(DefaultTTL + time.Second)
	if _, ok := tracker.Lookup("claude:s1"); ok {
		t.Fatal("session idle past the TTL must expire")
	}
	// A session served again after expiring starts fresh, with no switch.
	tracker.Observe(served("claude:s1", "auth-b", clock.now))
	route, ok := tracker.Lookup("claude:s1")
	if !ok || route.AuthID != "auth-b" || len(route.Switches) != 0 {
		t.Fatalf("route = %+v, want a fresh auth-b route", route)
	}
}

func TestTrackerBoundsSessionCount(t *testing.T) {
	tracker, clock := newTestTracker(t)
	for i := 0; i < maxSessions+10; i++ {
		tracker.Observe(served(fmt.Sprintf("claude:s%d", i), "auth-a", clock.now.Add(time.Duration(i)*time.Millisecond)))
	}
	if got := len(tracker.sessions); got != maxSessions {
		t.Fatalf("sessions = %d, want %d", got, maxSessions)
	}
	if _, ok := tracker.Lookup("claude:s0"); ok {
		t.Fatal("the least recently served session must be evicted first")
	}
	if _, ok := tracker.Lookup(fmt.Sprintf("claude:s%d", maxSessions+9)); !ok {
		t.Fatal("the newest session must survive")
	}
}
