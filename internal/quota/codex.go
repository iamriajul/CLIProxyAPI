package quota

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"math"
	"net/http"
	"regexp"
	"strconv"
	"strings"
	"time"
)

// Codex quota endpoint and headers, mirroring the CPAMC management UI adapter
// (CODEX_USAGE_URL, CODEX_REQUEST_HEADERS).
const (
	codexDefaultBaseURL = "https://chatgpt.com"
	codexUsagePath      = "/backend-api/wham/usage"
	codexUserAgent      = "codex-tui/0.149.1 (Mac OS 26.5.2; arm64) iTerm.app/3.6.11 (codex-tui; 0.149.1)"
	codexAccountHeader  = "Chatgpt-Account-Id"
)

const (
	codexFiveHourSeconds = 18000
	codexWeekSeconds     = 604800
	codexMinMonthSeconds = 28 * 24 * 60 * 60
	codexMaxMonthSeconds = 31 * 24 * 60 * 60
)

// CodexFetcher refreshes Codex quota from the wham usage endpoint, the same
// source the CPAMC management UI uses.
type CodexFetcher struct{}

// NewCodexFetcher builds a CodexFetcher.
func NewCodexFetcher() *CodexFetcher { return &CodexFetcher{} }

// Provider returns the canonical provider key.
func (*CodexFetcher) Provider() string { return "codex" }

// Fetch returns the classified rate-limit windows (5h/7d/monthly, code-review
// and additional grouped windows) plus the plan, or an error. A nil snapshot
// with a nil error means the usage payload carried no parseable windows.
//
// Reset credits and subscription-active-until are intentionally not fetched:
// the former lives on a separate credits endpoint and the latter on the
// subscriptions endpoint, and neither maps onto Snapshot windows — Snapshot
// carries only Windows plus Plan, and the usage payload's windows are the
// complete quota signal.
func (*CodexFetcher) Fetch(ctx context.Context, req FetchRequest) (*Snapshot, error) {
	token := BearerToken(req.Auth)
	if token == "" {
		return nil, errors.New("codex quota fetch: missing access token")
	}
	client := req.Client
	if client == nil {
		client = http.DefaultClient
	}
	headers := map[string]string{
		"Authorization": "Bearer " + token,
		"Content-Type":  "application/json",
		"User-Agent":    codexUserAgent,
	}
	if accountID := codexAccountID(req); accountID != "" {
		headers[codexAccountHeader] = accountID
	}

	now := time.Now().Truncate(time.Second)
	var payload codexUsagePayload
	if err := DoJSON(ctx, client, http.MethodGet, codexBaseURL(req)+codexUsagePath, headers, nil, &payload); err != nil {
		return nil, err
	}
	windows := payload.windows(now)
	if len(windows) == 0 {
		return nil, nil
	}
	return &Snapshot{Windows: windows, Plan: payload.planType(req)}, nil
}

func codexBaseURL(req FetchRequest) string {
	if req.Auth == nil {
		return codexDefaultBaseURL
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
	return codexDefaultBaseURL
}

// codexUsagePayload mirrors the CodexUsagePayload shape, accepting both
// snake_case and camelCase spellings with snake_case preferred.
type codexUsagePayload struct {
	PlanType        any               `json:"plan_type"`
	PlanTypeCamel   any               `json:"planType"`
	RateLimit       *codexRateLimit   `json:"rate_limit"`
	RateLimitCamel  *codexRateLimit   `json:"rateLimit"`
	CodeReview      *codexRateLimit   `json:"code_review_rate_limit"`
	CodeReviewCamel *codexRateLimit   `json:"codeReviewRateLimit"`
	Additional      []codexAdditional `json:"additional_rate_limits"`
	AdditionalCamel []codexAdditional `json:"additionalRateLimits"`
}

type codexRateLimit struct {
	Allowed           any          `json:"allowed"`
	LimitReached      any          `json:"limit_reached"`
	LimitReachedCamel any          `json:"limitReached"`
	Primary           *codexWindow `json:"primary_window"`
	PrimaryCamel      *codexWindow `json:"primaryWindow"`
	Secondary         *codexWindow `json:"secondary_window"`
	SecondaryCamel    *codexWindow `json:"secondaryWindow"`
}

type codexWindow struct {
	UsedPercent             any `json:"used_percent"`
	UsedPercentCamel        any `json:"usedPercent"`
	LimitWindowSeconds      any `json:"limit_window_seconds"`
	LimitWindowSecondsCamel any `json:"limitWindowSeconds"`
	ResetAfterSeconds       any `json:"reset_after_seconds"`
	ResetAfterSecondsCamel  any `json:"resetAfterSeconds"`
	ResetAt                 any `json:"reset_at"`
	ResetAtCamel            any `json:"resetAt"`
}

type codexAdditional struct {
	LimitName           any             `json:"limit_name"`
	LimitNameCamel      any             `json:"limitName"`
	MeteredFeature      any             `json:"metered_feature"`
	MeteredFeatureCamel any             `json:"meteredFeature"`
	RateLimit           *codexRateLimit `json:"rate_limit"`
	RateLimitCamel      *codexRateLimit `json:"rateLimit"`
}

func (p *codexUsagePayload) rateLimit() *codexRateLimit {
	if p.RateLimit != nil {
		return p.RateLimit
	}
	return p.RateLimitCamel
}

func (p *codexUsagePayload) codeReview() *codexRateLimit {
	if p.CodeReview != nil {
		return p.CodeReview
	}
	return p.CodeReviewCamel
}

func (p *codexUsagePayload) additional() []codexAdditional {
	if p.Additional != nil {
		return p.Additional
	}
	return p.AdditionalCamel
}

func (l *codexRateLimit) primary() *codexWindow {
	if l == nil {
		return nil
	}
	if l.Primary != nil {
		return l.Primary
	}
	return l.PrimaryCamel
}

func (l *codexRateLimit) secondary() *codexWindow {
	if l == nil {
		return nil
	}
	if l.Secondary != nil {
		return l.Secondary
	}
	return l.SecondaryCamel
}

func (a *codexAdditional) rateLimit() *codexRateLimit {
	if a.RateLimit != nil {
		return a.RateLimit
	}
	return a.RateLimitCamel
}

func (p *codexUsagePayload) planType(req FetchRequest) string {
	if plan := codexPlanString(p.PlanType); plan != "" {
		return plan
	}
	if plan := codexPlanString(p.PlanTypeCamel); plan != "" {
		return plan
	}
	// Fallback mirrors resolveCodexPlanType's auth-side candidates.
	if req.Auth != nil {
		if req.Auth.Attributes != nil {
			if plan := codexPlanString(req.Auth.Attributes["plan_type"]); plan != "" {
				return plan
			}
			if plan := codexPlanString(req.Auth.Attributes["planType"]); plan != "" {
				return plan
			}
		}
		if req.Auth.Metadata != nil {
			if plan := codexPlanString(req.Auth.Metadata["plan_type"]); plan != "" {
				return plan
			}
			if plan := codexPlanString(req.Auth.Metadata["planType"]); plan != "" {
				return plan
			}
		}
	}
	return ""
}

// CodexPlanDisplay maps raw plan types to render-ready labels, mirroring the
// CPAMC getPlanLabel table. Unknown values pass through verbatim.
func CodexPlanDisplay(raw string) string {
	switch strings.ToLower(strings.TrimSpace(raw)) {
	case "":
		return ""
	case "self_serve_business_prolite":
		return "Business Premium"
	case "pro":
		return "Pro 20x"
	case "prolite", "pro-lite", "pro_lite":
		return "Pro 5x"
	case "plus":
		return "Plus"
	case "team":
		return "Team"
	case "free":
		return "Free"
	default:
		return strings.TrimSpace(raw)
	}
}

func codexPlanString(value any) string {
	var raw string
	switch v := value.(type) {
	case string:
		raw = v
	case float64:
		raw = strconv.FormatFloat(v, 'f', -1, 64)
	default:
		return ""
	}
	return CodexPlanDisplay(raw)
}

func (p *codexUsagePayload) windows(now time.Time) []Window {
	var windows []Window
	if five, weekly := codexClassify(p.rateLimit()); five != nil || weekly != nil {
		windows = codexAppendWindow(windows, "5h", five, p.rateLimit(), now)
		windows = codexAppendWindow(windows, codexSecondaryName(weekly, ""), weekly, p.rateLimit(), now)
	}
	if review := p.codeReview(); review != nil {
		if five, weekly := codexClassify(review); five != nil || weekly != nil {
			windows = codexAppendWindow(windows, "code-review/5h", five, review, now)
			windows = codexAppendWindow(windows, codexSecondaryName(weekly, "code-review/"), weekly, review, now)
		}
	}
	for i, item := range p.additional() {
		rateInfo := item.rateLimit()
		if rateInfo == nil {
			continue
		}
		name := codexString(item.LimitName)
		if name == "" {
			name = codexString(item.LimitNameCamel)
		}
		if name == "" {
			name = codexString(item.MeteredFeature)
		}
		if name == "" {
			name = codexString(item.MeteredFeatureCamel)
		}
		prefix := Slugify(name)
		if prefix == "" {
			prefix = "additional-" + strconv.Itoa(i+1)
		}
		if five, weekly := codexClassify(rateInfo); five != nil || weekly != nil {
			windows = codexAppendWindow(windows, prefix+"/5h", five, rateInfo, now)
			windows = codexAppendWindow(windows, codexSecondaryName(weekly, prefix+"/"), weekly, rateInfo, now)
		}
	}
	sortCodexWindows(windows)
	return windows
}

// codexSecondaryName picks the 7d vs monthly suffix for a classified secondary
// window, mirroring selectSecondaryWindowMeta: monthly durations read as
// monthly, everything else (weekly or legacy duration-less) as 7d.
func codexSecondaryName(weekly *codexWindow, prefix string) string {
	if weekly != nil {
		if secs, ok := weekly.windowSeconds(); ok && codexIsMonthly(secs) {
			return prefix + "monthly"
		}
	}
	return prefix + "7d"
}

// codexClassify mirrors pickClassifiedWindows: duration decides the bucket
// (18000s to 5h, a week or a 28-31 day month to the secondary slot), and
// legacy payloads without durations fall back to primary/secondary ordering.
func codexClassify(limit *codexRateLimit) (fiveHour, weekly *codexWindow) {
	if limit == nil {
		return nil, nil
	}
	primary, secondary := limit.primary(), limit.secondary()
	for _, w := range []*codexWindow{primary, secondary} {
		if w == nil {
			continue
		}
		secs, ok := w.windowSeconds()
		if !ok {
			continue
		}
		if secs == codexFiveHourSeconds && fiveHour == nil {
			fiveHour = w
		} else if (secs == codexWeekSeconds || codexIsMonthly(secs)) && weekly == nil {
			weekly = w
		}
	}
	if fiveHour == nil && primary != nil && primary != weekly {
		fiveHour = primary
	}
	if weekly == nil && secondary != nil && secondary != fiveHour {
		weekly = secondary
	}
	return fiveHour, weekly
}

func codexIsMonthly(secs float64) bool {
	return secs >= codexMinMonthSeconds && secs <= codexMaxMonthSeconds
}

// codexAppendWindow mirrors addWindow: the used percent passes through, and a
// reached limit with a known reset reads as fully consumed. Windows with
// neither a used percent nor a reset instant are skipped so they never surface
// as phantom windows.
func codexAppendWindow(windows []Window, name string, raw *codexWindow, limit *codexRateLimit, now time.Time) []Window {
	if raw == nil {
		return windows
	}
	window := Window{Name: name}
	if used, ok := raw.usedPercent(); ok {
		window.UsedPercent = UsedPercent(used)
	}
	if reset, ok := raw.resetAt(now); ok {
		window.ResetAt = &reset
	}
	if window.UsedPercent == nil {
		reached, _ := codexFlag(limit.LimitReached)
		if !reached {
			reached, _ = codexFlag(limit.LimitReachedCamel)
		}
		allowed, allowedOK := codexFlag(limit.Allowed)
		if (reached || (allowedOK && !allowed)) && window.ResetAt != nil {
			window.UsedPercent = UsedPercent(100)
		}
	}
	if window.UsedPercent == nil && window.ResetAt == nil {
		return windows
	}
	return append(windows, window)
}

func (w *codexWindow) usedPercent() (float64, bool) {
	if used, ok := codexNumber(w.UsedPercent); ok {
		return used, true
	}
	return codexNumber(w.UsedPercentCamel)
}

func (w *codexWindow) windowSeconds() (float64, bool) {
	if secs, ok := codexNumber(w.LimitWindowSeconds); ok {
		return secs, true
	}
	return codexNumber(w.LimitWindowSecondsCamel)
}

// resetAt mirrors resolveResetMs plus the reset_after_seconds offset fallback:
// an absolute reset_at/resetAt wins (unix seconds or milliseconds by magnitude,
// else ISO-8601), otherwise the offset resolves against the fetch time.
func (w *codexWindow) resetAt(now time.Time) (time.Time, bool) {
	for _, candidate := range []any{w.ResetAt, w.ResetAtCamel} {
		if reset, ok := codexAbsoluteReset(candidate); ok {
			return reset, true
		}
	}
	for _, candidate := range []any{w.ResetAfterSeconds, w.ResetAfterSecondsCamel} {
		if offset, ok := codexNumber(candidate); ok && offset > 0 {
			return now.Add(time.Duration(offset) * time.Second), true
		}
	}
	return time.Time{}, false
}

func codexAbsoluteReset(value any) (time.Time, bool) {
	switch v := value.(type) {
	case string:
		if reset, ok := codexParseTime(strings.TrimSpace(v)); ok {
			return reset, true
		}
		if num, ok := codexNumber(v); ok {
			return codexUnixTime(num)
		}
	case float64:
		return codexUnixTime(v)
	case int:
		return codexUnixTime(float64(v))
	case int64:
		return codexUnixTime(float64(v))
	case json.Number:
		if f, err := v.Float64(); err == nil {
			return codexUnixTime(f)
		}
	}
	return time.Time{}, false
}

// codexAccountID mirrors resolveCodexChatgptAccountId's server-side inputs:
// direct attributes/metadata fields first, then the id_token payload (object,
// JSON, or JWT) including the nested OpenAI auth claim.
func codexAccountID(req FetchRequest) string {
	if req.Auth == nil {
		return ""
	}
	if req.Auth.Attributes != nil {
		for _, key := range []string{"chatgpt_account_id", "chatgptAccountId"} {
			if id := strings.TrimSpace(req.Auth.Attributes[key]); id != "" {
				return id
			}
		}
	}
	if req.Auth.Metadata != nil {
		for _, key := range []string{"chatgpt_account_id", "chatgptAccountId"} {
			if id, _ := req.Auth.Metadata[key].(string); strings.TrimSpace(id) != "" {
				return strings.TrimSpace(id)
			}
		}
		for _, key := range []string{"id_token", "idToken"} {
			if id := codexAccountFromToken(req.Auth.Metadata[key]); id != "" {
				return id
			}
		}
	}
	if req.Auth.Attributes != nil {
		if id := codexAccountFromToken(req.Auth.Attributes["id_token"]); id != "" {
			return id
		}
	}
	return ""
}

func codexAccountFromToken(value any) string {
	var record map[string]any
	switch v := value.(type) {
	case map[string]any:
		record = v
	case string:
		trimmed := strings.TrimSpace(v)
		if trimmed == "" {
			return ""
		}
		if err := json.Unmarshal([]byte(trimmed), &record); err != nil || record == nil {
			record = codexJWTPayload(trimmed)
		}
	default:
		return ""
	}
	if record == nil {
		return ""
	}
	if nested, ok := record["https://api.openai.com/auth"].(map[string]any); ok && nested != nil {
		record = nested
	}
	for _, key := range []string{"chatgpt_account_id", "chatgptAccountId"} {
		switch id := record[key].(type) {
		case string:
			if strings.TrimSpace(id) != "" {
				return strings.TrimSpace(id)
			}
		case float64:
			return strconv.FormatFloat(id, 'f', -1, 64)
		}
	}
	return ""
}

func codexJWTPayload(token string) map[string]any {
	segments := strings.Split(token, ".")
	if len(segments) < 2 {
		return nil
	}
	raw, err := base64.RawURLEncoding.DecodeString(strings.TrimSpace(segments[1]))
	if err != nil {
		padded := segments[1]
		if rem := len(padded) % 4; rem != 0 {
			padded += strings.Repeat("=", 4-rem)
		}
		raw, err = base64.URLEncoding.DecodeString(padded)
		if err != nil {
			return nil
		}
	}
	var record map[string]any
	if err := json.Unmarshal(raw, &record); err != nil {
		return nil
	}
	return record
}

func sortCodexWindows(windows []Window) {
	rank := map[string]int{"5h": 0, "7d": 1, "monthly": 2}
	for i := 1; i < len(windows); i++ {
		for j := i; j > 0; j-- {
			if codexWindowLess(windows[j], windows[j-1], rank) {
				windows[j], windows[j-1] = windows[j-1], windows[j]
			} else {
				break
			}
		}
	}
}

func codexWindowLess(a, b Window, rank map[string]int) bool {
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

// codexNumber mirrors normalizeNumberValue: finite numbers pass through and
// numeric strings parse; anything else is not a number.
func codexNumber(value any) (float64, bool) {
	switch v := value.(type) {
	case float64:
		if isCodexFinite(v) {
			return v, true
		}
	case float32:
		f := float64(v)
		if isCodexFinite(f) {
			return f, true
		}
	case int:
		return float64(v), true
	case int64:
		return float64(v), true
	case json.Number:
		if f, err := v.Float64(); err == nil && isCodexFinite(f) {
			return f, true
		}
	case string:
		if f, err := strconv.ParseFloat(strings.TrimSpace(v), 64); err == nil && isCodexFinite(f) {
			if strings.TrimSpace(v) == "" {
				return 0, false
			}
			return f, true
		}
	}
	return 0, false
}
func isCodexFinite(f float64) bool {
	return !math.IsNaN(f) && !math.IsInf(f, 0)
}

func codexString(value any) string {
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

// codexFlag mirrors truthiness for allowed/limit_reached: booleans pass
// through, numbers test nonzero, and common truthy/falsy spellings parse.
func codexFlag(value any) (bool, bool) {
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

var codexFractionTrim = regexp.MustCompile(`(\.\d{6})\d+`)

func codexParseTime(raw string) (time.Time, bool) {
	if raw == "" {
		return time.Time{}, false
	}
	normalized := codexFractionTrim.ReplaceAllString(raw, "$1")
	for _, layout := range []string{time.RFC3339Nano, time.RFC3339, "2006-01-02T15:04:05.999999999Z0700", "2006-01-02 15:04:05Z0700"} {
		if parsed, err := time.Parse(layout, normalized); err == nil {
			return parsed, true
		}
	}
	return time.Time{}, false
}

func codexUnixTime(value float64) (time.Time, bool) {
	if !isCodexFinite(value) || value <= 0 {
		return time.Time{}, false
	}
	if value < 1e11 {
		return time.Unix(int64(value), 0).UTC(), true
	}
	return time.UnixMilli(int64(value)).UTC(), true
}
