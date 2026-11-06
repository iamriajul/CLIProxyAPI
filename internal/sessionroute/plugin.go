package sessionroute

import (
	"context"
	"strings"

	coreusage "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/usage"
)

func init() {
	coreusage.RegisterPlugin(&trackerPlugin{})
}

// trackerPlugin feeds the process-wide tracker from the usage record bus, in
// the same registration shape as internal/tps.
type trackerPlugin struct{}

func (p *trackerPlugin) HandleUsage(_ context.Context, record coreusage.Record) {
	if p == nil {
		return
	}
	observe(Default(), record)
}

// observe records generating requests that carry both a session and the
// credential that served them. Non-generating calls (token counting) are
// skipped: they may be served off the session's binding and would read as a
// switch.
func observe(tracker *Tracker, record coreusage.Record) {
	if tracker == nil || !coreusage.GenerateEnabled(record.Generate) {
		return
	}
	tracker.Observe(Observation{
		SessionID:  record.SessionID,
		AuthID:     record.AuthID,
		Provider:   strings.TrimSpace(record.Provider),
		Model:      strings.TrimSpace(record.Model),
		At:         record.RequestedAt,
		Failed:     record.Failed,
		StatusCode: record.Fail.StatusCode,
	})
}
