// OpenCode Zen Go quota fetcher.
//
// Port of the CPAMC management-UI quota adapter
// (src/utils/quota/opencode.ts + src/features/quota/providers/opencode/data.ts).
// Upstream: GET <gateway>/v1/usage (default
// https://opencode.ai/zen/go/v1/usage) with Bearer auth, reporting floored
// integer percents plus an ISO reset per window. All three windows (rolling
// 5h, weekly, monthly) are required: a partial report is rejected rather than
// silently dropping windows.
package quota

import (
	"context"
	"errors"
	"math"
	"net/http"
	"sort"
	"strings"
	"time"

	opencodeauth "github.com/router-for-me/CLIProxyAPI/v7/internal/auth/opencode"
)

// OpenCodeFetcher refreshes OpenCode Zen Go usage via the gateway endpoint.
type OpenCodeFetcher struct{}

// NewOpenCodeFetcher builds an OpenCodeFetcher.
func NewOpenCodeFetcher() *OpenCodeFetcher { return &OpenCodeFetcher{} }

// Provider returns the canonical provider key.
func (*OpenCodeFetcher) Provider() string { return "opencode" }

// Fetch returns the 5h, weekly, and monthly usage windows.
func (*OpenCodeFetcher) Fetch(ctx context.Context, req FetchRequest) (*Snapshot, error) {
	var metadata map[string]any
	var attributes map[string]string
	if req.Auth != nil {
		metadata = req.Auth.Metadata
		attributes = req.Auth.Attributes
	}
	key := opencodeauth.OpenCodeCreds(metadata, attributes)
	if key == "" {
		return nil, errors.New("opencode quota fetch: missing api key")
	}
	// UsageURL normalizes the gateway base (tolerating a missing /v1 suffix)
	// and falls back to the default gateway when no override is stored.
	usageURL := opencodeauth.UsageURL(opencodeauth.ResolveBaseURL(metadata, attributes))
	var payload opencodeUsagePayload
	headers := map[string]string{
		"Accept":        "application/json",
		"Authorization": "Bearer " + key,
	}
	if err := DoJSON(ctx, req.Client, http.MethodGet, usageURL, headers, nil, &payload); err != nil {
		return nil, err
	}
	return opencodeSnapshotFromPayload(&payload)
}

type opencodeUsageWindow struct {
	Percent  float64 `json:"percent"`
	Status   string  `json:"status"`
	ResetsAt string  `json:"resetsAt"`
}

type opencodeUsage struct {
	Rolling *opencodeUsageWindow `json:"rolling"`
	Weekly  *opencodeUsageWindow `json:"weekly"`
	Monthly *opencodeUsageWindow `json:"monthly"`
}

type opencodeUsagePayload struct {
	Usage *opencodeUsage `json:"usage"`
}

// opencodeWindow validates one window: a finite 0-100 percent, an ok or
// rate-limited status, and a parseable reset instant. Anything else is not a
// report the UI could render, so the whole payload is rejected.
func opencodeWindow(name string, raw *opencodeUsageWindow) (Window, bool) {
	if raw == nil {
		return Window{}, false
	}
	if math.IsNaN(raw.Percent) || math.IsInf(raw.Percent, 0) || raw.Percent < 0 || raw.Percent > 100 {
		return Window{}, false
	}
	if raw.Status != "ok" && raw.Status != "rate-limited" {
		return Window{}, false
	}
	reset, err := time.Parse(time.RFC3339Nano, strings.TrimSpace(raw.ResetsAt))
	if err != nil {
		return Window{}, false
	}
	used := math.Floor(raw.Percent)
	window := Window{
		Name:        name,
		UsedPercent: UsedPercent(used),
		Status:      raw.Status,
	}
	at := reset.UTC()
	window.ResetAt = &at
	return window, true
}

func opencodeSnapshotFromPayload(payload *opencodeUsagePayload) (*Snapshot, error) {
	if payload == nil || payload.Usage == nil {
		return nil, errors.New("opencode quota fetch: empty quota data")
	}
	windows := make([]Window, 0, 3)
	for _, entry := range []struct {
		name string
		raw  *opencodeUsageWindow
	}{
		{"5h", payload.Usage.Rolling},
		{"7d", payload.Usage.Weekly},
		{"monthly", payload.Usage.Monthly},
	} {
		window, ok := opencodeWindow(entry.name, entry.raw)
		if !ok {
			return nil, errors.New("opencode quota fetch: empty quota data")
		}
		windows = append(windows, window)
	}
	snapshot := &Snapshot{Windows: windows}
	sort.Slice(snapshot.Windows, func(i, j int) bool { return snapshot.Windows[i].Name < snapshot.Windows[j].Name })
	return snapshot, nil
}
