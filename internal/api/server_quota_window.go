package api

import (
	"math"
	"sort"
	"strconv"
	"strings"
	"time"

	coreauth "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/auth"
)

// inferenceQuotaWindow is one normalized usage window: the stable view clients
// should render instead of parsing provider-specific signals keys.
// Well-known names: "5h", "7d" (codex five-hour/weekly and claude 5h/7d),
// "monthly" (codex team secondary), "daily"/"weekly" (devin). Grouped limits
// (antigravity model groups, namespaced codex limits) prefix their group
// ("gemini-models/5h", "bengalfox/7d").
type inferenceQuotaWindow struct {
	Name        string     `json:"name"`
	UsedPercent *float64   `json:"used_percent,omitempty"`
	ResetAt     *time.Time `json:"reset_at,omitempty"`
	Status      string     `json:"status,omitempty"`
}

// inferenceQuotaWindows normalizes passive quota signals into stable usage
// windows. Providers without a parser yield an empty (non-nil) slice so the
// wire shape stays an array.
func inferenceQuotaWindows(provider string, quota coreauth.QuotaState) []inferenceQuotaWindow {
	switch strings.ToLower(strings.TrimSpace(provider)) {
	case "codex":
		return codexQuotaWindows(quota.Signals, quota.ObservedAt)
	case "claude":
		return claudeQuotaWindows(quota.Signals)
	case "devin":
		return devinQuotaWindows(quota.Signals)
	default:
		return []inferenceQuotaWindow{}
	}
}

type quotaWindowBuild struct {
	used    *float64
	resetAt *time.Time
	resetIn *int64
	status  string
}

func quotaWindowBuildFor(builds map[string]*quotaWindowBuild, name string) *quotaWindowBuild {
	build := builds[name]
	if build == nil {
		build = &quotaWindowBuild{}
		builds[name] = build
	}
	return build
}

func finishQuotaWindows(builds map[string]*quotaWindowBuild, observedAt time.Time, rank func(string) (int, string)) []inferenceQuotaWindow {
	windows := make([]inferenceQuotaWindow, 0, len(builds))
	for name, build := range builds {
		if build == nil {
			continue
		}
		window := inferenceQuotaWindow{Name: name}
		if build.used != nil {
			window.UsedPercent = build.used
		}
		if build.status != "" {
			window.Status = build.status
		}
		switch {
		case build.resetAt != nil:
			window.ResetAt = build.resetAt
		case build.resetIn != nil && !observedAt.IsZero():
			at := observedAt.Add(time.Duration(*build.resetIn) * time.Second)
			window.ResetAt = &at
		}
		if window.UsedPercent == nil && window.ResetAt == nil && window.Status == "" {
			continue
		}
		windows = append(windows, window)
	}
	sort.Slice(windows, func(i, j int) bool {
		rankI, nameI := rank(windows[i].Name)
		rankJ, nameJ := rank(windows[j].Name)
		if rankI != rankJ {
			return rankI < rankJ
		}
		return nameI < nameJ
	})
	return windows
}
func alphabeticalWindowRank(name string) (int, string) {
	return 0, name
}

func codexWindowRank(name string) (int, string) {
	switch name {
	case "5h":
		return 0, name
	case "7d":
		return 1, name
	case "monthly":
		return 2, name
	default:
		return 3, name
	}
}

func quotaWindowRankFor(provider string) func(string) (int, string) {
	if strings.EqualFold(strings.TrimSpace(provider), "codex") {
		return codexWindowRank
	}
	return alphabeticalWindowRank
}

// mergeQuotaWindows overlays model-specific windows onto credential-level
// ones by name and re-sorts with the provider's rank.
func mergeQuotaWindows(provider string, base, overlay []inferenceQuotaWindow) []inferenceQuotaWindow {
	merged := make(map[string]inferenceQuotaWindow, len(base)+len(overlay))
	for _, window := range base {
		merged[window.Name] = window
	}
	for _, window := range overlay {
		merged[window.Name] = window
	}
	out := make([]inferenceQuotaWindow, 0, len(merged))
	for _, window := range merged {
		out = append(out, window)
	}
	rank := quotaWindowRankFor(provider)
	sort.Slice(out, func(i, j int) bool {
		rankI, nameI := rank(out[i].Name)
		rankJ, nameJ := rank(out[j].Name)
		if rankI != rankJ {
			return rankI < rankJ
		}
		return nameI < nameJ
	})
	return out
}

const (
	// codexFiveHourSeconds and codexWeekSeconds classify Codex windows by
	// duration, mirroring the CPAMC quota UI (five-hour/weekly/monthly).
	codexFiveHourSeconds = 18000
	codexWeekSeconds     = 604800
	codexMinMonthSeconds = 28 * 24 * 60 * 60
	codexMaxMonthSeconds = 31 * 24 * 60 * 60
)

// codexQuotaWindows groups X-Codex-* signals by window segment, then names
// each window by duration: 18000s becomes "5h", 604800s "7d", 28-31 days
// "monthly". Without a recognized duration it falls back positionally
// (primary to "5h", secondary to "7d"), mirroring the CPAMC quota UI.
// Namespaced variants keep their namespace ("bengalfox/7d"). Percentages are
// 0-100; resets prefer absolute Reset-At over relative Reset-After-Seconds
// resolved against the observation time.
func codexQuotaWindows(signals map[string]string, observedAt time.Time) []inferenceQuotaWindow {
	grouped := make(map[string]*codexWindowSignals)
	for key, value := range signals {
		name, metric, ok := splitCodexWindowMetric(key)
		if !ok {
			continue
		}
		group := grouped[name]
		if group == nil {
			group = &codexWindowSignals{}
			grouped[name] = group
		}
		switch metric {
		case "used-percent":
			if percent, err := strconv.ParseFloat(strings.TrimSpace(value), 64); err == nil {
				rounded := roundQuotaPercent(percent)
				group.used = &rounded
			}
		case "reset-at":
			if at, ok := parseQuotaUnixTime(value); ok {
				group.resetAt = &at
			}
		case "reset-after-seconds":
			if seconds, err := strconv.ParseInt(strings.TrimSpace(value), 10, 64); err == nil {
				group.resetIn = &seconds
			}
		case "window-minutes":
			if minutes, err := strconv.ParseInt(strings.TrimSpace(value), 10, 64); err == nil {
				seconds := minutes * 60
				group.windowSeconds = &seconds
			}
		}
	}
	// Classify in sorted raw-key order so same-name collisions (two groups
	// reporting one duration) merge deterministically, first wins.
	rawKeys := make([]string, 0, len(grouped))
	for rawKey := range grouped {
		rawKeys = append(rawKeys, rawKey)
	}
	sort.Strings(rawKeys)
	builds := make(map[string]*quotaWindowBuild)
	for _, rawKey := range rawKeys {
		group := grouped[rawKey]
		if group == nil {
			continue
		}
		namespace, positional := "", rawKey
		if separator := strings.LastIndex(rawKey, "/"); separator >= 0 {
			namespace, positional = rawKey[:separator], rawKey[separator+1:]
		}
		name := classifyCodexSlot(group.windowSeconds, positional)
		if namespace != "" {
			name = namespace + "/" + name
		}
		build := builds[name]
		if build == nil {
			build = &quotaWindowBuild{}
			builds[name] = build
		}
		if build.used == nil {
			build.used = group.used
		}
		if build.resetAt == nil {
			build.resetAt = group.resetAt
		}
		if build.resetIn == nil {
			build.resetIn = group.resetIn
		}
	}
	return finishQuotaWindows(builds, observedAt, codexWindowRank)
}

type codexWindowSignals struct {
	used          *float64
	resetAt       *time.Time
	resetIn       *int64
	windowSeconds *int64
}

func classifyCodexSlot(windowSeconds *int64, positional string) string {
	if windowSeconds != nil {
		switch seconds := *windowSeconds; {
		case seconds == codexFiveHourSeconds:
			return "5h"
		case seconds == codexWeekSeconds:
			return "7d"
		case seconds >= codexMinMonthSeconds && seconds <= codexMaxMonthSeconds:
			return "monthly"
		}
	}
	if positional == "primary" {
		return "5h"
	}
	return "7d"
}

func splitCodexWindowMetric(header string) (string, string, bool) {
	rest, found := strings.CutPrefix(strings.ToLower(strings.TrimSpace(header)), "x-codex-")
	if !found || rest == "" {
		return "", "", false
	}
	segments := strings.Split(rest, "-")
	windowAt := -1
	for i, segment := range segments {
		if segment == "primary" || segment == "secondary" {
			windowAt = i
			break
		}
	}
	if windowAt < 0 || windowAt == len(segments)-1 {
		return "", "", false
	}
	var name strings.Builder
	for _, segment := range segments[:windowAt] {
		name.WriteString(segment)
		name.WriteString("/")
	}
	name.WriteString(segments[windowAt])
	return name.String(), strings.Join(segments[windowAt+1:], "-"), true
}

// claudeQuotaWindows normalizes Anthropic-Ratelimit-Unified-<window>-* signals.
// Utilization is a 0-1 fraction scaled to percent; resets are unix seconds.
func claudeQuotaWindows(signals map[string]string) []inferenceQuotaWindow {
	builds := make(map[string]*quotaWindowBuild)
	for key, value := range signals {
		rest, found := strings.CutPrefix(strings.ToLower(strings.TrimSpace(key)), "anthropic-ratelimit-unified-")
		if !found || rest == "" {
			continue
		}
		segments := strings.Split(rest, "-")
		if len(segments) < 2 {
			continue
		}
		metric := segments[len(segments)-1]
		if metric != "utilization" && metric != "status" && metric != "reset" {
			continue
		}
		build := quotaWindowBuildFor(builds, strings.Join(segments[:len(segments)-1], "-"))
		switch metric {
		case "utilization":
			if fraction, err := strconv.ParseFloat(strings.TrimSpace(value), 64); err == nil {
				used := roundQuotaPercent(fraction * 100)
				build.used = &used
			}
		case "status":
			build.status = strings.TrimSpace(value)
		case "reset":
			if at, ok := parseQuotaUnixTime(value); ok {
				build.resetAt = &at
			}
		}
	}
	return finishQuotaWindows(builds, time.Time{}, alphabeticalWindowRank)
}

// devinQuotaWindows normalizes daily/weekly remaining percents ("100%" style)
// and RFC 3339 reset times recorded at login.
func devinQuotaWindows(signals map[string]string) []inferenceQuotaWindow {
	builds := make(map[string]*quotaWindowBuild)
	for key, value := range signals {
		lower := strings.ToLower(strings.TrimSpace(key))
		var name string
		switch {
		case strings.HasPrefix(lower, "daily_"):
			name = "daily"
		case strings.HasPrefix(lower, "weekly_"):
			name = "weekly"
		default:
			continue
		}
		build := quotaWindowBuildFor(builds, name)
		switch {
		case strings.HasSuffix(lower, "_quota_remaining_percent"):
			number := strings.TrimSpace(strings.TrimSuffix(strings.TrimSpace(value), "%"))
			if remaining, err := strconv.ParseFloat(number, 64); err == nil {
				used := roundQuotaPercent(100 - remaining)
				build.used = &used
			}
		case strings.HasSuffix(lower, "_quota_reset_at"):
			if at, ok := parseQuotaResetTime(value); ok {
				build.resetAt = &at
			}
		}
	}
	return finishQuotaWindows(builds, time.Time{}, alphabeticalWindowRank)
}

func roundQuotaPercent(value float64) float64 {
	return math.Round(value*100) / 100
}

func parseQuotaUnixTime(value string) (time.Time, bool) {
	seconds, err := strconv.ParseInt(strings.TrimSpace(value), 10, 64)
	if err != nil {
		return time.Time{}, false
	}
	return time.Unix(seconds, 0).UTC(), true
}

func parseQuotaResetTime(value string) (time.Time, bool) {
	value = strings.TrimSpace(value)
	if value == "" {
		return time.Time{}, false
	}
	if at, err := time.Parse(time.RFC3339, value); err == nil {
		return at, true
	}
	return parseQuotaUnixTime(value)
}
