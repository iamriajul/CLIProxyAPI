package quota

import (
	"context"
	"encoding/json"
	"errors"
	"math"
	"net/http"
	"regexp"
	"strconv"
	"strings"
	"time"
)

// Claude OAuth quota endpoints and headers, mirroring the CPAMC management UI
// adapter (CLAUDE_USAGE_URL, CLAUDE_PROFILE_URL, CLAUDE_REQUEST_HEADERS).
const (
	claudeDefaultBaseURL = "https://api.anthropic.com"
	claudeUsagePath      = "/api/oauth/usage"
	claudeProfilePath    = "/api/oauth/profile"
	claudeAnthropicBeta  = "oauth-2025-04-20"
	claudeFiveHourName   = "5h"
	claudeSevenDayName   = "7d"
	claudeFableName      = "fable/7d"
)

// ClaudeFetcher refreshes Claude quota from the Anthropic OAuth usage and
// profile endpoints, the same sources the CPAMC management UI uses.
type ClaudeFetcher struct{}

// NewClaudeFetcher builds a ClaudeFetcher.
func NewClaudeFetcher() *ClaudeFetcher { return &ClaudeFetcher{} }

// Provider returns the canonical provider key.
func (*ClaudeFetcher) Provider() string { return "claude" }

// Fetch returns the 5h/7d usage windows plus the Fable weekly window and the
// plan, or an error. A failing profile call only drops the plan, never the
// windows. A nil snapshot with a nil error means the usage payload carried no
// parseable windows.
func (*ClaudeFetcher) Fetch(ctx context.Context, req FetchRequest) (*Snapshot, error) {
	token := BearerToken(req.Auth)
	if token == "" {
		return nil, errors.New("claude quota fetch: missing access token")
	}
	client := req.Client // nil-safe: DoJSON falls back to http.DefaultClient.
	base := claudeBaseURL(req)
	headers := map[string]string{
		"Authorization":  "Bearer " + token,
		"Content-Type":   "application/json",
		"anthropic-beta": claudeAnthropicBeta,
	}

	var usage claudeUsagePayload
	if err := DoJSON(ctx, client, http.MethodGet, base+claudeUsagePath, headers, nil, &usage); err != nil {
		return nil, err
	}
	windows := usage.windows()
	if len(windows) == 0 {
		return nil, nil
	}

	snapshot := &Snapshot{Windows: windows}
	// Best effort: a profile failure must not fail the windows.
	var profile claudeProfilePayload
	if err := DoJSON(ctx, client, http.MethodGet, base+claudeProfilePath, headers, nil, &profile); err == nil {
		snapshot.Plan = profile.planType()
	}
	return snapshot, nil
}

func claudeBaseURL(req FetchRequest) string {
	if req.Auth == nil {
		return claudeDefaultBaseURL
	}
	if req.Auth.Attributes != nil {
		if base := strings.TrimSpace(req.Auth.Attributes["base_url"]); base != "" {
			return strings.TrimSuffix(base, "/")
		}
	}
	if req.Auth.Metadata != nil {
		if base, _ := req.Auth.Metadata["base_url"].(string); strings.TrimSpace(base) != "" {
			return strings.TrimSuffix(strings.TrimSpace(base), "/")
		}
	}
	return claudeDefaultBaseURL
}

// claudeUsagePayload mirrors the ClaudeUsagePayload shape: named windows plus
// the modern scoped limits list that carries the Fable allowance.
type claudeUsagePayload struct {
	FiveHour       *claudeUsageWindow `json:"five_hour"`
	SevenDay       *claudeUsageWindow `json:"seven_day"`
	SevenDayOpus   *claudeUsageWindow `json:"seven_day_opus"`
	SevenDaySonnet *claudeUsageWindow `json:"seven_day_sonnet"`
	SevenDayCowork *claudeUsageWindow `json:"seven_day_cowork"`
	SevenDayApps   *claudeUsageWindow `json:"seven_day_oauth_apps"`
	Iguana         *claudeUsageWindow `json:"iguana_necktie"`
	Limits         []claudeUsageLimit `json:"limits"`
}

type claudeUsageWindow struct {
	Utilization any `json:"utilization"`
	ResetsAt    any `json:"resets_at"`
}

type claudeUsageLimit struct {
	Kind     any `json:"kind"`
	Percent  any `json:"percent"`
	ResetsAt any `json:"resets_at"`
	IsActive any `json:"is_active"`
	Scope    *struct {
		Model *struct {
			DisplayName any `json:"display_name"`
		} `json:"model"`
	} `json:"scope"`
}

func (p *claudeUsagePayload) windows() []Window {
	var windows []Window
	if w := claudeNamedWindow(p.FiveHour, claudeFiveHourName); w != nil {
		windows = append(windows, *w)
	}
	if w := claudeNamedWindow(p.SevenDay, claudeSevenDayName); w != nil {
		windows = append(windows, *w)
	}
	for _, entry := range []struct {
		raw  *claudeUsageWindow
		name string
	}{
		{p.SevenDayOpus, "opus/7d"},
		{p.SevenDaySonnet, "sonnet/7d"},
		{p.SevenDayCowork, "cowork/7d"},
		{p.SevenDayApps, "oauth-apps/7d"},
	} {
		if w := claudeNamedWindow(entry.raw, entry.name); w != nil {
			windows = append(windows, *w)
		}
	}
	if w := p.fableWindow(); w != nil {
		windows = append(windows, *w)
	}
	sortClaudeWindows(windows)
	return windows
}

// claudeNamedWindow maps one named usage window ({utilization, resets_at}) to
// a quota window. Utilization already reads as 0-100 consumed percent, exactly
// as the CPAMC adapter passes it through. Entries with neither a used percent
// nor a reset instant are skipped so they never surface as phantom windows.
func claudeNamedWindow(raw *claudeUsageWindow, name string) *Window {
	if raw == nil {
		return nil
	}
	window := Window{Name: name}
	if used, ok := claudeNumber(raw.Utilization); ok {
		window.UsedPercent = UsedPercent(used)
	}
	if reset, ok := claudeReset(raw.ResetsAt); ok {
		window.ResetAt = &reset
	}
	if window.UsedPercent == nil && window.ResetAt == nil {
		return nil
	}
	return &window
}

// fableWindow mirrors findFableUsageLimit plus the iguana_necktie legacy
// fallback: the first valid weekly_scoped Fable percent wins (preferring the
// active one), and the legacy named field applies only when no modern
// candidate carries a valid percent.
func (p *claudeUsagePayload) fableWindow() *Window {
	for _, pass := range []bool{true, false} {
		for i := range p.Limits {
			limit := &p.Limits[i]
			if !claudeIsFableLimit(limit) {
				continue
			}
			active, _ := claudeFlag(limit.IsActive)
			if active != pass {
				continue
			}
			used, ok := claudeNumber(limit.Percent)
			if !ok {
				continue
			}
			window := Window{Name: claudeFableName, UsedPercent: UsedPercent(used)}
			if reset, ok := claudeReset(limit.ResetsAt); ok {
				window.ResetAt = &reset
			}
			return &window
		}
	}
	return claudeNamedWindow(p.Iguana, claudeFableName)
}

func claudeIsFableLimit(limit *claudeUsageLimit) bool {
	if limit == nil {
		return false
	}
	if !strings.EqualFold(strings.TrimSpace(claudeString(limit.Kind)), "weekly_scoped") {
		return false
	}
	if _, ok := claudeNumber(limit.Percent); !ok {
		return false
	}
	var display string
	if limit.Scope != nil && limit.Scope.Model != nil {
		display = strings.ToLower(strings.TrimSpace(claudeString(limit.Scope.Model.DisplayName)))
	}
	return display == "fable" || display == "fable 5"
}

type claudeProfilePayload struct {
	Account *struct {
		HasMax any `json:"has_claude_max"`
		HasPro any `json:"has_claude_pro"`
	} `json:"account"`
	Organization *struct {
		OrganizationType   any `json:"organization_type"`
		SubscriptionStatus any `json:"subscription_status"`
	} `json:"organization"`
}

// planType mirrors resolveClaudePlanType: team membership wins, then Max, then
// Pro, then explicit opt-outs mean free.
func (p *claudeProfilePayload) planType() string {
	if p.Organization != nil &&
		strings.EqualFold(strings.TrimSpace(claudeString(p.Organization.OrganizationType)), "claude_team") &&
		strings.EqualFold(strings.TrimSpace(claudeString(p.Organization.SubscriptionStatus)), "active") {
		return "Team"
	}
	var hasMax, hasPro *bool
	if p.Account != nil {
		if flag, ok := claudeFlag(p.Account.HasMax); ok {
			hasMax = &flag
		}
		if flag, ok := claudeFlag(p.Account.HasPro); ok {
			hasPro = &flag
		}
	}
	if hasMax != nil && *hasMax {
		return "Max"
	}
	if hasPro != nil && *hasPro {
		return "Pro"
	}
	if hasMax != nil && hasPro != nil && !*hasMax && !*hasPro {
		return "Free"
	}
	return ""
}

func sortClaudeWindows(windows []Window) {
	rank := map[string]int{claudeFiveHourName: 0, claudeSevenDayName: 1}
	for i := 1; i < len(windows); i++ {
		for j := i; j > 0; j-- {
			if claudeWindowLess(windows[j], windows[j-1], rank) {
				windows[j], windows[j-1] = windows[j-1], windows[j]
			} else {
				break
			}
		}
	}
}

func claudeWindowLess(a, b Window, rank map[string]int) bool {
	ra, oka := rank[a.Name]
	rb, okb := rank[b.Name]
	if oka && okb {
		return ra < rb
	}
	if oka {
		return true
	}
	if okb {
		return false
	}
	return a.Name < b.Name
}

// claudeNumber mirrors normalizeNumberValue: finite numbers pass through and
// numeric strings parse; anything else is not a number.
func claudeNumber(value any) (float64, bool) {
	switch v := value.(type) {
	case float64:
		if isClaudeFinite(v) {
			return v, true
		}
	case float32:
		f := float64(v)
		if isClaudeFinite(f) {
			return f, true
		}
	case int:
		return float64(v), true
	case int64:
		return float64(v), true
	case json.Number:
		if f, err := v.Float64(); err == nil && isClaudeFinite(f) {
			return f, true
		}
	case string:
		trimmed := strings.TrimSpace(v)
		if trimmed == "" {
			return 0, false
		}
		if f, err := strconv.ParseFloat(trimmed, 64); err == nil && isClaudeFinite(f) {
			return f, true
		}
	}
	return 0, false
}

func isClaudeFinite(f float64) bool {
	return !math.IsNaN(f) && !math.IsInf(f, 0)
}

func claudeString(value any) string {
	switch v := value.(type) {
	case string:
		return strings.TrimSpace(v)
	case float64:
		return strconv.FormatFloat(v, 'f', -1, 64)
	case bool:
		return strconv.FormatBool(v)
	default:
		return ""
	}
}

// claudeFlag mirrors the adapter's normalizeFlagValue: booleans pass through,
// numbers test nonzero, and common truthy/falsy spellings parse.
func claudeFlag(value any) (bool, bool) {
	switch v := value.(type) {
	case bool:
		return v, true
	case float64:
		return v != 0, true
	case int:
		return v != 0, true
	case int64:
		return v != 0, true
	case string:
		switch strings.ToLower(strings.TrimSpace(v)) {
		case "true", "1", "yes", "y", "on":
			return true, true
		case "false", "0", "no", "n", "off":
			return false, true
		}
	}
	return false, false
}

// claudeReset mirrors resolveResetMs over a single candidate: ISO-8601 strings
// (tolerating over-precise fractions) or unix seconds/milliseconds by
// magnitude.
func claudeReset(value any) (time.Time, bool) {
	switch v := value.(type) {
	case string:
		return claudeParseTime(strings.TrimSpace(v))
	case float64:
		return claudeUnixTime(v)
	case int:
		return claudeUnixTime(float64(v))
	case int64:
		return claudeUnixTime(float64(v))
	case json.Number:
		if f, err := v.Float64(); err == nil {
			return claudeUnixTime(f)
		}
	}
	return time.Time{}, false
}

var claudeFractionTrim = regexp.MustCompile(`(\.\d{6})\d+`)

func claudeParseTime(raw string) (time.Time, bool) {
	if raw == "" {
		return time.Time{}, false
	}
	normalized := claudeFractionTrim.ReplaceAllString(raw, "$1")
	for _, layout := range []string{time.RFC3339Nano, time.RFC3339, "2006-01-02T15:04:05.999999999Z0700", "2006-01-02 15:04:05Z0700"} {
		if parsed, err := time.Parse(layout, normalized); err == nil {
			return parsed, true
		}
	}
	return time.Time{}, false
}

func claudeUnixTime(value float64) (time.Time, bool) {
	if !isClaudeFinite(value) || value <= 0 {
		return time.Time{}, false
	}
	if value < 1e11 {
		return time.Unix(int64(value), 0).UTC(), true
	}
	return time.UnixMilli(int64(value)).UTC(), true
}
