// Kimi quota fetcher.
//
// Port of the CPAMC management-UI quota adapter
// (src/utils/quota/builders.ts buildKimiQuotaRows + helpers +
// src/features/quota/providers/kimi/data.ts).
// Upstream: GET https://api.kimi.com/coding/v1/usages with Bearer auth. Each
// limit entry (plus the usage summary) becomes a window named by its explicit
// name or duration token; used counts derive from remaining when the payload
// omits them (remaining converts to used), and resets resolve from absolute
// timestamps or relative countdowns against fetch time.
package quota

import (
	"context"
	"errors"
	"math"
	"net/http"
	"sort"
	"strconv"
	"strings"
	"time"
)

// kimiUsageURL is the usage endpoint. It is a variable (not a constant) so
// tests can redirect it at an httptest server; production code always uses
// the default.
var kimiUsageURL = "https://api.kimi.com/coding/v1/usages"

// KimiFetcher refreshes Kimi usage rows via the coding usages endpoint.
type KimiFetcher struct{}

// NewKimiFetcher builds a KimiFetcher.
func NewKimiFetcher() *KimiFetcher { return &KimiFetcher{} }

// Provider returns the canonical provider key.
func (*KimiFetcher) Provider() string { return "kimi" }

// Fetch returns one window per usage row with a measurable quota.
func (*KimiFetcher) Fetch(ctx context.Context, req FetchRequest) (*Snapshot, error) {
	token := BearerToken(req.Auth)
	if token == "" {
		return nil, errors.New("kimi quota fetch: missing access token")
	}
	var payload kimiUsagePayload
	headers := map[string]string{"Authorization": "Bearer " + token}
	if err := DoJSON(ctx, req.Client, http.MethodGet, kimiUsageURL, headers, nil, &payload); err != nil {
		return nil, err
	}
	now := time.Now()
	snapshot := &Snapshot{}
	for i, item := range payload.Limits {
		window, ok := kimiWindowFromLimit(item, i, now)
		if !ok {
			continue
		}
		snapshot.Windows = append(snapshot.Windows, window)
	}
	if summary, ok := kimiWindowFromSummary(payload.Usage, now); ok {
		snapshot.Windows = append(snapshot.Windows, summary)
	}
	if len(snapshot.Windows) == 0 {
		return nil, nil
	}
	sort.SliceStable(snapshot.Windows, func(i, j int) bool { return snapshot.Windows[i].Name < snapshot.Windows[j].Name })
	return snapshot, nil
}

type kimiUsagePayload struct {
	Usage  map[string]any   `json:"usage"`
	Limits []map[string]any `json:"limits"`
}

func kimiInt(value any) (int64, bool) {
	switch num := value.(type) {
	case float64:
		if math.IsNaN(num) || math.IsInf(num, 0) {
			return 0, false
		}
		return int64(math.Floor(num)), true
	case float32:
		converted := float64(num)
		if math.IsNaN(converted) || math.IsInf(converted, 0) {
			return 0, false
		}
		return int64(math.Floor(converted)), true
	case int:
		return int64(num), true
	case int64:
		return num, true
	case string:
		trimmed := strings.TrimSpace(num)
		if trimmed == "" {
			return 0, false
		}
		parsed, err := strconv.ParseFloat(trimmed, 64)
		if err != nil || math.IsNaN(parsed) || math.IsInf(parsed, 0) {
			return 0, false
		}
		return int64(math.Floor(parsed)), true
	default:
		return 0, false
	}
}

func kimiString(record map[string]any, keys ...string) string {
	for _, key := range keys {
		if value, ok := record[key].(string); ok && strings.TrimSpace(value) != "" {
			return strings.TrimSpace(value)
		}
	}
	return ""
}

func kimiRecord(value any) map[string]any {
	record, ok := value.(map[string]any)
	if !ok {
		return nil
	}
	return record
}

// kimiTimeUnit normalizes protobuf-style units such as TIME_UNIT_MINUTE.
// An absent or unknown unit means minutes, matching the card-label fallback.
func kimiTimeUnit(raw any) string {
	unit, _ := raw.(string)
	normalized := strings.Replace(strings.ToUpper(strings.TrimSpace(unit)), "TIME_UNIT_", "", 1)
	switch normalized {
	case "SECONDS", "SECOND":
		return "second"
	case "", "MINUTES", "MINUTE":
		return "minute"
	case "HOURS", "HOUR":
		return "hour"
	case "DAYS", "DAY":
		return "day"
	case "WEEKS", "WEEK":
		return "week"
	default:
		return ""
	}
}

func kimiDurationToken(duration int64, rawUnit any) string {
	switch kimiTimeUnit(rawUnit) {
	case "second":
		return strconv.FormatInt(duration, 10) + "s"
	case "hour":
		return strconv.FormatInt(duration, 10) + "h"
	case "day":
		return strconv.FormatInt(duration, 10) + "d"
	case "week":
		return strconv.FormatInt(duration, 10) + "w"
	default:
		if duration%60 == 0 {
			return strconv.FormatInt(duration/60, 10) + "h"
		}
		return strconv.FormatInt(duration, 10) + "m"
	}
}

// kimiResetMs resolves the absolute reset instant: absolute timestamps first
// (ISO-8601, then unix seconds-or-milliseconds by magnitude), then relative
// countdowns in seconds against the fetch time.
func kimiResetMs(data map[string]any, now time.Time) *time.Time {
	for _, key := range []string{"reset_at", "resetAt", "reset_time", "resetTime"} {
		if at := kimiParseInstant(data[key]); at != nil {
			return at
		}
	}
	for _, key := range []string{"reset_in", "resetIn", "ttl"} {
		if seconds, ok := kimiInt(data[key]); ok && seconds > 0 {
			at := now.Add(time.Duration(seconds) * time.Second).UTC()
			return &at
		}
	}
	return nil
}

func kimiParseInstant(value any) *time.Time {
	text, isString := value.(string)
	if isString {
		trimmed := strings.TrimSpace(text)
		if trimmed != "" {
			if at, err := time.Parse(time.RFC3339Nano, trimmed); err == nil {
				utc := at.UTC()
				return &utc
			}
			if at := kimiParseTruncatedFraction(trimmed); at != nil {
				return at
			}
		}
	}
	var num float64
	switch raw := value.(type) {
	case float64:
		num = raw
	case float32:
		num = float64(raw)
	case int:
		num = float64(raw)
	case int64:
		num = float64(raw)
	case string:
		trimmed := strings.TrimSpace(raw)
		if trimmed == "" {
			return nil
		}
		parsed, err := strconv.ParseFloat(trimmed, 64)
		if err != nil {
			return nil
		}
		num = parsed
	default:
		return nil
	}
	if math.IsNaN(num) || math.IsInf(num, 0) || num <= 0 {
		return nil
	}
	var ms int64
	if num < 1e11 {
		ms = int64(num * 1000)
	} else {
		ms = int64(num)
	}
	at := time.UnixMilli(ms).UTC()
	return &at
}

// kimiParseTruncatedFraction tolerates over-precise fractional seconds (such
// as the microsecond stamps Kimi emits) by capping the fraction at 9 digits.
func kimiParseTruncatedFraction(value string) *time.Time {
	dot := strings.LastIndex(value, ".")
	if dot < 0 {
		return nil
	}
	end := dot + 1
	for end < len(value) && value[end] >= '0' && value[end] <= '9' {
		end++
	}
	if end-dot-1 <= 9 {
		return nil
	}
	candidate := value[:dot+10] + value[end:]
	at, err := time.Parse(time.RFC3339Nano, candidate)
	if err != nil {
		return nil
	}
	utc := at.UTC()
	return &utc
}

// kimiWindowName ports kimiLimitLabel: explicit name/title/scope first, then
// the duration token, then the summary/limit-index fallback.
func kimiWindowName(item, detail, window map[string]any, index int, summary bool) string {
	for _, record := range []map[string]any{item, detail} {
		for _, key := range []string{"name", "title", "scope"} {
			if name := kimiString(record, key); name != "" {
				if slug := Slugify(name); slug != "" {
					return slug
				}
				return name
			}
		}
	}
	duration, hasDuration := kimiInt(window["duration"])
	if !hasDuration {
		duration, hasDuration = kimiInt(item["duration"])
	}
	if !hasDuration {
		duration, hasDuration = kimiInt(detail["duration"])
	}
	timeUnit := window["timeUnit"]
	if timeUnit == nil {
		timeUnit = item["timeUnit"]
	}
	if timeUnit == nil {
		timeUnit = detail["timeUnit"]
	}
	if hasDuration && duration > 0 {
		return strings.ToLower(kimiDurationToken(duration, timeUnit))
	}
	if summary {
		return "7d"
	}
	return "limit-" + strconv.Itoa(index+1)
}

// kimiUsagePercent converts a used/limit (or remaining-derived used) row to a
// consumed percent. Rows without measurable quota report false.
func kimiUsagePercent(detail map[string]any) (*float64, bool) {
	limit, hasLimit := kimiInt(detail["limit"])
	used, hasUsed := kimiInt(detail["used"])
	if !hasUsed {
		if remaining, ok := kimiInt(detail["remaining"]); ok && hasLimit {
			used, hasUsed = limit-remaining, true
		}
	}
	if !hasUsed && !hasLimit {
		return nil, false
	}
	if !hasLimit {
		limit = 0
	}
	if !hasUsed {
		used = 0
	}
	var percent float64
	switch {
	case limit > 0:
		percent = float64(used) / float64(limit) * 100
	case used > 0:
		percent = 100
	default:
		return nil, false
	}
	return UsedPercent(math.Min(math.Max(percent, 0), 100)), true
}

func kimiWindowFromLimit(item map[string]any, index int, now time.Time) (Window, bool) {
	detail := kimiRecord(item["detail"])
	if detail == nil {
		detail = item
	}
	window := kimiRecord(item["window"])
	if window == nil {
		window = map[string]any{}
	}
	used, ok := kimiUsagePercent(detail)
	if !ok {
		return Window{}, false
	}
	result := Window{Name: kimiWindowName(item, detail, window, index, false), UsedPercent: used}
	if reset := kimiResetMs(detail, now); reset != nil {
		result.ResetAt = reset
	}
	return result, true
}

func kimiWindowFromSummary(usage map[string]any, now time.Time) (Window, bool) {
	if len(usage) == 0 {
		return Window{}, false
	}
	used, ok := kimiUsagePercent(usage)
	if !ok {
		return Window{}, false
	}
	result := Window{Name: kimiWindowName(nil, usage, nil, 0, true), UsedPercent: used}
	if reset := kimiResetMs(usage, now); reset != nil {
		result.ResetAt = reset
	}
	return result, true
}
