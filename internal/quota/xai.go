// xAI/Grok quota fetcher.
//
// Port of the CPAMC management-UI quota adapter
// (src/features/quota/providers/xai/data.ts + src/utils/quota/builders.ts +
// src/utils/quota/xaiPaid.ts). Free-tier credentials query the cli-chat-proxy
// billing endpoints (weekly + monthly, merged with the weekly period kept
// atomic); when both are unavailable the adapter falls back to a paid-health
// probe (POST chat completions with grok-4.5), which reports the paid plan
// with no usage windows.
package quota

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"net/http"
	"sort"
	"strconv"
	"strings"
	"time"
)

// xAI billing and health endpoints. Variables (not constants) so tests can
// redirect them at httptest servers; production code uses the defaults.
var (
	xaiBillingWeeklyURL  = "https://cli-chat-proxy.grok.com/v1/billing?format=credits"
	xaiBillingMonthlyURL = "https://cli-chat-proxy.grok.com/v1/billing"
	xaiChatURL           = "https://api.x.ai/v1/chat/completions"
)

const (
	xaiGrokClientVersion = "0.2.91"
	xaiGrokUserAgent     = "grok-pager/0.2.91 grok-shell/0.2.91 (macos; aarch64)"
	xaiPaidHealthModel   = "grok-4.5"

	// xaiSuperGrokLimitCents and xaiSuperGrokHeavyLimitCents mirror the
	// adapter's SuperGrok / SuperGrok Heavy plan-chip thresholds.
	xaiSuperGrokLimitCents      = 15000
	xaiSuperGrokHeavyLimitCents = 150000
)

// XaiFetcher refreshes xAI billing quota via the cli-chat-proxy endpoints.
type XaiFetcher struct{}

// NewXaiFetcher builds an XaiFetcher.
func NewXaiFetcher() *XaiFetcher { return &XaiFetcher{} }

// Provider returns the canonical provider key.
func (*XaiFetcher) Provider() string { return "xai" }

// Fetch returns weekly/monthly/on-demand windows, or the paid plan when only
// the health probe succeeds.
func (*XaiFetcher) Fetch(ctx context.Context, req FetchRequest) (*Snapshot, error) {
	token := BearerToken(req.Auth)
	if token == "" {
		return nil, errors.New("xai quota fetch: missing access token")
	}
	headers := map[string]string{
		"Authorization":         "Bearer " + token,
		"x-xai-token-auth":      "xai-grok-cli",
		"x-grok-client-version": xaiGrokClientVersion,
		"accept":                "*/*",
		"user-agent":            xaiGrokUserAgent,
	}
	if userID := xaiUserID(req); userID != "" {
		headers["x-userid"] = userID
	}
	weekly, weeklyErr := xaiRequestBilling(ctx, req.Client, xaiBillingWeeklyURL, headers)
	monthly, monthlyErr := xaiRequestBilling(ctx, req.Client, xaiBillingMonthlyURL, headers)
	if weeklyErr != nil && monthlyErr != nil {
		return xaiPaidHealthFallback(ctx, req.Client, token, weeklyErr)
	}
	summary := xaiMergeSummaries(weekly, monthly)
	if summary == nil {
		return xaiPaidHealthFallback(ctx, req.Client, token, errors.New("xai quota fetch: empty quota data"))
	}
	return xaiSnapshotFromSummary(summary), nil
}

// xaiPaidHealthFallback probes chat completions like the adapter's paid path.
// Success means a paid credential with no measurable quota: report the plan
// with no windows. Failure preserves the original billing error.
func xaiPaidHealthFallback(ctx context.Context, client *http.Client, token string, billingErr error) (*Snapshot, error) {
	body, _ := json.Marshal(map[string]any{
		"model":      xaiPaidHealthModel,
		"messages":   []any{map[string]string{"role": "user", "content": "ping"}},
		"max_tokens": 1,
		"stream":     false,
	})
	headers := map[string]string{
		"Authorization": "Bearer " + token,
		"accept":        "application/json",
		"Content-Type":  "application/json",
	}
	if err := DoJSON(ctx, client, http.MethodPost, xaiChatURL, headers, body, nil); err != nil {
		return nil, billingErr
	}
	return &Snapshot{Plan: "Paid"}, nil
}

func xaiRequestBilling(ctx context.Context, client *http.Client, url string, headers map[string]string) (*xaiBillingSummary, error) {
	var payload xaiBillingPayload
	if err := DoJSON(ctx, client, http.MethodGet, url, headers, nil, &payload); err != nil {
		return nil, err
	}
	return xaiBuildSummary(payload.Config), nil
}

// xaiUserID resolves the x-userid header the billing endpoints expect,
// mirroring the adapter's candidate list (never logged or errored on).
func xaiUserID(req FetchRequest) string {
	if req.Auth == nil {
		return ""
	}
	var candidates []any
	for _, key := range []string{"sub", "subject", "user_id", "userId"} {
		if req.Auth.Attributes != nil {
			candidates = append(candidates, req.Auth.Attributes[key])
		}
		if req.Auth.Metadata != nil {
			candidates = append(candidates, req.Auth.Metadata[key])
		}
	}
	if req.Auth.Metadata != nil {
		for _, nested := range []string{"oauth", "user"} {
			if record, ok := req.Auth.Metadata[nested].(map[string]any); ok {
				candidates = append(candidates, record["sub"], record["subject"], record["id"])
			}
		}
	}
	for _, candidate := range candidates {
		if id, ok := candidate.(string); ok && strings.TrimSpace(id) != "" {
			return strings.TrimSpace(id)
		}
	}
	return ""
}

type xaiBillingPeriod struct {
	Type  string `json:"type"`
	Start string `json:"start"`
	End   string `json:"end"`
}

type xaiProductUsage struct {
	Product           string `json:"product"`
	UsagePercent      any    `json:"usagePercent"`
	UsagePercentSnake any    `json:"usage_percent"`
}

type xaiBillingConfig struct {
	CurrentPeriod      *xaiBillingPeriod `json:"currentPeriod"`
	CurrentPeriodSnake *xaiBillingPeriod `json:"current_period"`
	CreditUsagePercent any               `json:"creditUsagePercent"`
	CreditUsageSnake   any               `json:"credit_usage_percent"`
	ProductUsage       []xaiProductUsage `json:"productUsage"`
	ProductUsageSnake  []xaiProductUsage `json:"product_usage"`
	MonthlyLimit       any               `json:"monthlyLimit"`
	MonthlyLimitSnake  any               `json:"monthly_limit"`
	Used               any               `json:"used"`
	OnDemandCap        any               `json:"onDemandCap"`
	OnDemandCapSnake   any               `json:"on_demand_cap"`
	OnDemandUsed       any               `json:"onDemandUsed"`
	OnDemandUsedSnake  any               `json:"on_demand_used"`
	BillingPeriodStart string            `json:"billingPeriodStart"`
	BillingStartSnake  string            `json:"billing_period_start"`
	BillingPeriodEnd   string            `json:"billingPeriodEnd"`
	BillingEndSnake    string            `json:"billing_period_end"`
}

type xaiBillingPayload struct {
	Config *xaiBillingConfig `json:"config"`
}

type xaiProductSummary struct {
	product      string
	usagePercent *float64
}

// xaiBillingSummary is the merged billing state the windows derive from,
// mirroring XaiBillingSummary.
type xaiBillingSummary struct {
	periodType          string // weekly, monthly, or unknown
	usagePercent        *float64
	periodStart         string
	periodEnd           string
	resetAtMs           *int64
	productUsage        []xaiProductSummary
	monthlyLimitCents   *float64
	usedCents           *float64
	includedUsedCents   *float64
	onDemandCapCents    *float64
	onDemandUsedCents   *float64
	onDemandUsedPercent *float64
	usedPercent         *float64
}

func xaiNumber(value any) (float64, bool) {
	switch num := value.(type) {
	case float64:
		if math.IsNaN(num) || math.IsInf(num, 0) {
			return 0, false
		}
		return num, true
	case float32:
		converted := float64(num)
		if math.IsNaN(converted) || math.IsInf(converted, 0) {
			return 0, false
		}
		return converted, true
	case int:
		return float64(num), true
	case int64:
		return float64(num), true
	case string:
		trimmed := strings.TrimSpace(num)
		if trimmed == "" {
			return 0, false
		}
		parsed, err := strconv.ParseFloat(trimmed, 64)
		if err != nil || math.IsNaN(parsed) || math.IsInf(parsed, 0) {
			return 0, false
		}
		return parsed, true
	default:
		return 0, false
	}
}

func xaiCentValue(value any) *float64 {
	if record, ok := value.(map[string]any); ok {
		value = record["val"]
	}
	if num, ok := xaiNumber(value); ok {
		return &num
	}
	return nil
}

func xaiFirstString(values ...string) string {
	for _, value := range values {
		if strings.TrimSpace(value) != "" {
			return strings.TrimSpace(value)
		}
	}
	return ""
}

func xaiPeriodType(period *xaiBillingPeriod) string {
	var raw string
	if period != nil {
		raw = strings.ToLower(strings.TrimSpace(period.Type))
	}
	if strings.Contains(raw, "weekly") {
		return "weekly"
	}
	if strings.Contains(raw, "monthly") {
		return "monthly"
	}
	return "unknown"
}

// xaiResetMs parses an ISO-8601 or unix (seconds or milliseconds by
// magnitude) timestamp to epoch milliseconds.
func xaiResetMs(value string) *int64 {
	trimmed := strings.TrimSpace(value)
	if trimmed == "" {
		return nil
	}
	if ms, err := time.Parse(time.RFC3339Nano, trimmed); err == nil {
		at := ms.UTC().UnixMilli()
		return &at
	}
	// Tolerate over-precise fractional seconds some providers emit.
	if dot := strings.LastIndex(trimmed, "."); dot >= 0 {
		end := dot + 1
		for end < len(trimmed) && trimmed[end] >= '0' && trimmed[end] <= '9' {
			end++
		}
		if digits := end - dot - 1; digits > 9 {
			candidate := trimmed[:dot+10] + trimmed[end:]
			if ms, err := time.Parse(time.RFC3339Nano, candidate); err == nil {
				at := ms.UTC().UnixMilli()
				return &at
			}
		}
	}
	if num, ok := xaiNumber(trimmed); ok && num > 0 {
		var ms int64
		if num < 1e11 {
			ms = int64(num * 1000)
		} else {
			ms = int64(num)
		}
		return &ms
	}
	return nil
}

func xaiClampPercent(value float64) float64 {
	return math.Min(math.Max(value, 0), 100)
}

func xaiBuildSummary(config *xaiBillingConfig) *xaiBillingSummary {
	if config == nil {
		return nil
	}
	period := config.CurrentPeriod
	if period == nil {
		period = config.CurrentPeriodSnake
	}
	periodType := xaiPeriodType(period)
	var creditUsagePercent *float64
	if num, ok := xaiNumber(config.CreditUsagePercent); ok {
		creditUsagePercent = &num
	} else if num, ok := xaiNumber(config.CreditUsageSnake); ok {
		creditUsagePercent = &num
	}
	productUsage := config.ProductUsage
	if productUsage == nil {
		productUsage = config.ProductUsageSnake
	}
	var products []xaiProductSummary
	for i, item := range productUsage {
		product := strings.TrimSpace(item.Product)
		if product == "" {
			product = fmt.Sprintf("Product %d", i+1)
		}
		var percent *float64
		if num, ok := xaiNumber(item.UsagePercent); ok {
			percent = &num
		} else if num, ok := xaiNumber(item.UsagePercentSnake); ok {
			percent = &num
		}
		products = append(products, xaiProductSummary{product: product, usagePercent: percent})
	}
	monthlyLimit := xaiCentValue(config.MonthlyLimit)
	if monthlyLimit == nil {
		monthlyLimit = xaiCentValue(config.MonthlyLimitSnake)
	}
	used := xaiCentValue(config.Used)
	onDemandCap := xaiCentValue(config.OnDemandCap)
	if onDemandCap == nil {
		onDemandCap = xaiCentValue(config.OnDemandCapSnake)
	}
	explicitOnDemandUsed := xaiCentValue(config.OnDemandUsed)
	if explicitOnDemandUsed == nil {
		explicitOnDemandUsed = xaiCentValue(config.OnDemandUsedSnake)
	}
	periodStart := xaiFirstString(periodStartOf(period), config.BillingPeriodStart, config.BillingStartSnake)
	periodEnd := xaiFirstString(periodEndOf(period), config.BillingPeriodEnd, config.BillingEndSnake)
	billingStart := xaiFirstString(config.BillingPeriodStart, config.BillingStartSnake)
	billingEnd := xaiFirstString(config.BillingPeriodEnd, config.BillingEndSnake)

	var includedUsed *float64
	if used != nil {
		if monthlyLimit != nil && *monthlyLimit > 0 {
			capped := math.Min(*used, *monthlyLimit)
			includedUsed = &capped
		} else {
			includedUsed = used
		}
	}
	var derivedOnDemand *float64
	if used != nil && monthlyLimit != nil {
		over := math.Max(0, *used-*monthlyLimit)
		derivedOnDemand = &over
	}
	onDemandUsed := explicitOnDemandUsed
	if onDemandUsed == nil {
		onDemandUsed = derivedOnDemand
	}
	var usedPercent *float64
	if monthlyLimit != nil && *monthlyLimit > 0 && includedUsed != nil {
		percent := *includedUsed / *monthlyLimit * 100
		usedPercent = &percent
	}
	var onDemandUsedPercent *float64
	if onDemandCap != nil && *onDemandCap > 0 && onDemandUsed != nil {
		percent := *onDemandUsed / *onDemandCap * 100
		onDemandUsedPercent = &percent
	}

	hasWeeklyData := creditUsagePercent != nil || periodType == "weekly" || len(products) > 0
	hasMonthlyData := monthlyLimit != nil || used != nil ||
		(!hasWeeklyData && (onDemandCap != nil || billingEnd != ""))
	if !hasWeeklyData && !hasMonthlyData {
		return nil
	}
	summary := &xaiBillingSummary{}
	if hasWeeklyData {
		if periodType == "unknown" {
			summary.periodType = "weekly"
		} else {
			summary.periodType = periodType
		}
		summary.usagePercent = creditUsagePercent
		summary.periodStart = periodStart
		summary.periodEnd = periodEnd
	} else {
		summary.periodType = "monthly"
		summary.periodStart = billingStart
		summary.periodEnd = billingEnd
	}
	summary.productUsage = products
	summary.monthlyLimitCents = monthlyLimit
	summary.usedCents = used
	summary.includedUsedCents = includedUsed
	summary.onDemandCapCents = onDemandCap
	summary.onDemandUsedCents = onDemandUsed
	summary.onDemandUsedPercent = onDemandUsedPercent
	summary.usedPercent = usedPercent
	summary.resetAtMs = xaiResetMs(summary.periodEnd)
	return summary
}

func periodStartOf(period *xaiBillingPeriod) string {
	if period == nil {
		return ""
	}
	return period.Start
}

func periodEndOf(period *xaiBillingPeriod) string {
	if period == nil {
		return ""
	}
	return period.End
}

// xaiMergeSummaries keeps the active period atomic: the weekly and monthly
// endpoints describe different clocks, so one endpoint's dates never back the
// other's period type.
func xaiMergeSummaries(primary, fallback *xaiBillingSummary) *xaiBillingSummary {
	if primary == nil {
		return fallback
	}
	if fallback == nil {
		return primary
	}
	periodSummary := primary
	if primary.periodType == "unknown" && fallback.periodType != "unknown" {
		periodSummary = fallback
	}
	merged := &xaiBillingSummary{
		periodType:  periodSummary.periodType,
		periodStart: periodSummary.periodStart,
		periodEnd:   periodSummary.periodEnd,
		resetAtMs:   xaiResetMs(periodSummary.periodEnd),
	}
	if len(primary.productUsage) > 0 {
		merged.productUsage = primary.productUsage
	} else {
		merged.productUsage = fallback.productUsage
	}
	merged.usagePercent = periodSummary.usagePercent
	merged.monthlyLimitCents = firstFloat(primary.monthlyLimitCents, fallback.monthlyLimitCents)
	merged.usedCents = firstFloat(primary.usedCents, fallback.usedCents)
	merged.includedUsedCents = firstFloat(primary.includedUsedCents, fallback.includedUsedCents)
	merged.onDemandCapCents = firstFloat(primary.onDemandCapCents, fallback.onDemandCapCents)
	merged.onDemandUsedCents = firstFloat(primary.onDemandUsedCents, fallback.onDemandUsedCents)
	merged.onDemandUsedPercent = firstFloat(primary.onDemandUsedPercent, fallback.onDemandUsedPercent)
	merged.usedPercent = firstFloat(primary.usedPercent, fallback.usedPercent)
	return merged
}

func firstFloat(values ...*float64) *float64 {
	for _, value := range values {
		if value != nil {
			return value
		}
	}
	return nil
}

func xaiSnapshotFromSummary(summary *xaiBillingSummary) *Snapshot {
	snapshot := &Snapshot{}
	switch {
	case summary.monthlyLimitCents != nil && *summary.monthlyLimitCents == float64(xaiSuperGrokHeavyLimitCents):
		snapshot.Plan = "SuperGrok Heavy"
	case summary.monthlyLimitCents != nil && *summary.monthlyLimitCents == float64(xaiSuperGrokLimitCents):
		snapshot.Plan = "SuperGrok"
	}
	// Only the weekly figure is a quota window; the monthly figure is a
	// billing cycle, so it carries no reset instant.
	if summary.periodType == "weekly" && summary.usagePercent != nil {
		weekly := Window{Name: "7d", UsedPercent: UsedPercent(xaiClampPercent(*summary.usagePercent))}
		if summary.resetAtMs != nil {
			at := time.UnixMilli(*summary.resetAtMs).UTC()
			weekly.ResetAt = &at
		}
		snapshot.Windows = append(snapshot.Windows, weekly)
	}
	for i, product := range summary.productUsage {
		if product.usagePercent == nil {
			continue
		}
		name := "product/" + Slugify(product.product)
		if strings.Trim(name, "-/") == "product" {
			name = fmt.Sprintf("product-%d", i+1)
		}
		snapshot.Windows = append(snapshot.Windows, Window{
			Name:        name,
			UsedPercent: UsedPercent(xaiClampPercent(*product.usagePercent)),
		})
	}
	if summary.usedPercent != nil {
		snapshot.Windows = append(snapshot.Windows, Window{
			Name:        "monthly",
			UsedPercent: UsedPercent(xaiClampPercent(*summary.usedPercent)),
		})
	}
	if summary.onDemandCapCents != nil && *summary.onDemandCapCents > 0 && summary.onDemandUsedPercent != nil {
		snapshot.Windows = append(snapshot.Windows, Window{
			Name:        "on-demand",
			UsedPercent: UsedPercent(xaiClampPercent(*summary.onDemandUsedPercent)),
		})
	}
	if len(snapshot.Windows) == 0 {
		return nil
	}
	sort.Slice(snapshot.Windows, func(i, j int) bool { return snapshot.Windows[i].Name < snapshot.Windows[j].Name })
	return snapshot
}
