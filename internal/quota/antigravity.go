package quota

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"sort"
	"strconv"
	"strings"
	"time"

	coreauth "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/auth"
)

// Antigravity upstream endpoints, tried in order. The first 2xx response
// carrying a non-empty groups array wins, mirroring the CPAMC management UI
// adapter fallback.
var antigravityQuotaURLs = []string{
	"https://daily-cloudcode-pa.googleapis.com/v1internal:retrieveUserQuotaSummary",
	"https://daily-cloudcode-pa.sandbox.googleapis.com/v1internal:retrieveUserQuotaSummary",
	"https://cloudcode-pa.googleapis.com/v1internal:retrieveUserQuotaSummary",
}

// Antigravity subscription endpoint, queried best-effort for the plan label.
var antigravitySubscriptionURL = "https://daily-cloudcode-pa.googleapis.com/v1internal:loadCodeAssist"

const antigravityUserAgent = "antigravity/cli/1.0.13 (aidev_client; os_type=darwin; arch=arm64)"

const antigravitySubscriptionBody = `{"metadata":{"ideType":"ANTIGRAVITY"}}`

// AntigravityFetcher refreshes Antigravity quota via the user quota summary
// endpoints, the same sources the CPAMC management UI uses.
type AntigravityFetcher struct{}

// NewAntigravityFetcher builds an AntigravityFetcher.
func NewAntigravityFetcher() *AntigravityFetcher { return &AntigravityFetcher{} }

// Provider returns the canonical provider key.
func (*AntigravityFetcher) Provider() string { return "antigravity" }

// Fetch returns per-group quota windows plus the best-effort plan, or an error.
func (*AntigravityFetcher) Fetch(ctx context.Context, req FetchRequest) (*Snapshot, error) {
	projectID := antigravityProjectID(req.Auth)
	if projectID == "" {
		return nil, errors.New("antigravity quota fetch: missing project_id")
	}
	token := BearerToken(req.Auth)
	if token == "" {
		return nil, errors.New("antigravity quota fetch: missing access_token")
	}
	client := req.Client // nil-safe: DoJSON falls back to http.DefaultClient.
	headers := map[string]string{
		"Authorization": "Bearer " + token,
		"Content-Type":  "application/json",
		"User-Agent":    antigravityUserAgent,
	}
	body, err := json.Marshal(map[string]string{"project": projectID})
	if err != nil {
		return nil, fmt.Errorf("antigravity quota fetch: encode request: %w", err)
	}

	var lastErr error
	for _, url := range antigravityQuotaURLs {
		var payload antigravityQuotaPayload
		if err := DoJSON(ctx, client, http.MethodPost, url, headers, body, &payload); err != nil {
			lastErr = err
			continue
		}
		groups := payload.unwrappedGroups()
		if len(groups) == 0 {
			lastErr = fmt.Errorf("antigravity quota fetch POST %s: no quota groups", url)
			continue
		}
		windows := antigravityWindows(groups)
		if len(windows) == 0 {
			lastErr = fmt.Errorf("antigravity quota fetch POST %s: no quota groups", url)
			continue
		}
		return &Snapshot{
			Windows: windows,
			Plan:    antigravityPlan(ctx, client, headers),
		}, nil
	}
	if lastErr != nil {
		return nil, lastErr
	}
	return nil, errors.New("antigravity quota fetch: no quota groups")
}

func antigravityProjectID(auth *coreauth.Auth) string {
	if auth == nil {
		return ""
	}
	if auth.Metadata != nil {
		for _, key := range []string{"project_id", "projectId"} {
			if value, _ := auth.Metadata[key].(string); strings.TrimSpace(value) != "" {
				return strings.TrimSpace(value)
			}
		}
	}
	if auth.Attributes != nil {
		for _, key := range []string{"project_id", "projectId"} {
			if value := strings.TrimSpace(auth.Attributes[key]); value != "" {
				return value
			}
		}
	}
	return ""
}

type antigravityQuotaPayload struct {
	Groups []antigravityQuotaGroup `json:"groups"`
	Body   json.RawMessage         `json:"body"`
}

type antigravityQuotaGroup struct {
	DisplayName      string                   `json:"displayName"`
	DisplayNameSnake string                   `json:"display_name"`
	Description      string                   `json:"description"`
	Buckets          []antigravityQuotaBucket `json:"buckets"`
}

type antigravityQuotaBucket struct {
	BucketID               string `json:"bucketId"`
	BucketIDSnake          string `json:"bucket_id"`
	DisplayName            string `json:"displayName"`
	DisplayNameSnake       string `json:"display_name"`
	Window                 string `json:"window"`
	ResetTime              string `json:"resetTime"`
	ResetTimeSnake         string `json:"reset_time"`
	RemainingFraction      any    `json:"remainingFraction"`
	RemainingFractionSnake any    `json:"remaining_fraction"`
}

func (p antigravityQuotaPayload) unwrappedGroups() []antigravityQuotaGroup {
	if len(p.Groups) > 0 {
		return p.Groups
	}
	if len(p.Body) == 0 {
		return nil
	}
	var nested antigravityQuotaPayload
	if err := json.Unmarshal(p.Body, &nested); err == nil && len(nested.Groups) > 0 {
		return nested.Groups
	}
	var text string
	if err := json.Unmarshal(p.Body, &text); err == nil && strings.TrimSpace(text) != "" {
		var parsed antigravityQuotaPayload
		if err := json.Unmarshal([]byte(text), &parsed); err == nil {
			return parsed.Groups
		}
	}
	return nil
}

func (g antigravityQuotaGroup) label() string {
	if strings.TrimSpace(g.DisplayName) != "" {
		return strings.TrimSpace(g.DisplayName)
	}
	return strings.TrimSpace(g.DisplayNameSnake)
}

func (b antigravityQuotaBucket) label() string {
	if strings.TrimSpace(b.DisplayName) != "" {
		return strings.TrimSpace(b.DisplayName)
	}
	return strings.TrimSpace(b.DisplayNameSnake)
}

func (b antigravityQuotaBucket) window() string {
	return strings.TrimSpace(b.Window)
}

func (b antigravityQuotaBucket) resetTime() string {
	if strings.TrimSpace(b.ResetTime) != "" {
		return strings.TrimSpace(b.ResetTime)
	}
	return strings.TrimSpace(b.ResetTimeSnake)
}

func (b antigravityQuotaBucket) fraction() (float64, bool) {
	raw := b.RemainingFraction
	if raw == nil {
		raw = b.RemainingFractionSnake
	}
	fraction, ok := antigravityFraction(raw)
	if !ok {
		return 0, false
	}
	if fraction < 0 {
		fraction = 0
	}
	if fraction > 1 {
		fraction = 1
	}
	return fraction, true
}

// antigravityFraction mirrors normalizeQuotaFraction: plain numbers, numeric
// strings, and trailing-percent strings.
func antigravityFraction(raw any) (float64, bool) {
	switch value := raw.(type) {
	case float64:
		if value != value || value > 1e308 || value < -1e308 {
			return 0, false
		}
		return value, true
	case float32:
		return float64(value), true
	case int:
		return float64(value), true
	case int64:
		return float64(value), true
	case json.Number:
		if parsed, err := value.Float64(); err == nil {
			return parsed, true
		}
		return 0, false
	case string:
		trimmed := strings.TrimSpace(value)
		if trimmed == "" {
			return 0, false
		}
		if strings.HasSuffix(trimmed, "%") {
			parsed, err := strconv.ParseFloat(strings.TrimSpace(strings.TrimSuffix(trimmed, "%")), 64)
			if err != nil {
				return 0, false
			}
			return parsed / 100, true
		}
		parsed, err := strconv.ParseFloat(trimmed, 64)
		if err != nil {
			return 0, false
		}
		return parsed, true
	default:
		return 0, false
	}
}

// antigravitySlot maps a bucket to its short window slot using the same
// vocabulary the CPAMC body component translates: five-hour, weekly, daily,
// and monthly limits.
func antigravitySlot(bucket antigravityQuotaBucket) string {
	window := strings.ToLower(strings.TrimSpace(bucket.window()))
	switch window {
	case "5h", "five-hour", "five_hour":
		return "5h"
	case "weekly", "week":
		return "7d"
	case "monthly", "month":
		return "monthly"
	case "daily", "day":
		return "daily"
	}
	label := strings.ToLower(bucket.label())
	label = strings.Join(strings.Fields(label), " ")
	switch {
	case strings.Contains(label, "5 hour"),
		strings.Contains(label, "5-hour"),
		strings.Contains(label, "five hour"),
		strings.Contains(label, "five-hour"),
		strings.Contains(label, "5h"):
		return "5h"
	case strings.Contains(label, "week"):
		return "7d"
	case strings.Contains(label, "month"):
		return "monthly"
	case strings.Contains(label, "day"), strings.Contains(label, "daily"):
		return "daily"
	default:
		return Slugify(bucket.label())
	}
}

func antigravitySlotRank(slot string) int {
	switch slot {
	case "5h":
		return 0
	case "7d":
		return 1
	case "monthly":
		return 2
	default:
		return 3
	}
}

func antigravityParseReset(raw string) *time.Time {
	trimmed := strings.TrimSpace(raw)
	if trimmed == "" {
		return nil
	}
	for _, layout := range []string{
		time.RFC3339Nano,
		time.RFC3339,
		"2006-01-02T15:04:05Z07:00",
		"2006-01-02 15:04:05Z07:00",
		time.RFC1123Z,
		time.RFC1123,
	} {
		if parsed, err := time.Parse(layout, trimmed); err == nil {
			utc := parsed.UTC()
			return &utc
		}
	}
	if epoch, err := strconv.ParseFloat(trimmed, 64); err == nil {
		var at time.Time
		switch {
		case epoch >= 1e12:
			at = time.UnixMilli(int64(epoch)).UTC()
		case epoch >= 1e9:
			at = time.Unix(int64(epoch), 0).UTC()
		default:
			return nil
		}
		return &at
	}
	return nil
}

func antigravityWindows(groups []antigravityQuotaGroup) []Window {
	windows := []Window{}
	for index, group := range groups {
		label := group.label()
		if label == "" {
			label = fmt.Sprintf("Quota Group %d", index+1)
		}
		slug := Slugify(label)
		if slug == "" {
			slug = fmt.Sprintf("quota-group-%d", index+1)
		}
		for _, bucket := range group.Buckets {
			fraction, ok := bucket.fraction()
			if !ok {
				continue
			}
			slot := antigravitySlot(bucket)
			if slot == "" {
				continue
			}
			window := Window{
				Name:        slug + "/" + slot,
				UsedPercent: UsedPercent((1 - fraction) * 100),
			}
			if reset := antigravityParseReset(bucket.resetTime()); reset != nil {
				window.ResetAt = reset
			}
			windows = append(windows, window)
		}
	}
	sort.SliceStable(windows, func(i, j int) bool {
		groupI, slotI, _ := strings.Cut(windows[i].Name, "/")
		groupJ, slotJ, _ := strings.Cut(windows[j].Name, "/")
		if groupI != groupJ {
			return groupI < groupJ
		}
		rankI, rankJ := antigravitySlotRank(slotI), antigravitySlotRank(slotJ)
		if rankI != rankJ {
			return rankI < rankJ
		}
		return slotI < slotJ
	})
	return windows
}

var antigravityPlanByTierID = map[string]string{
	"free-tier":          "free",
	"g1-pro-tier":        "pro",
	"g1-ultra-tier":      "ultra",
	"g1-ultra-lite-tier": "ultra-lite",
}

type antigravityTier struct {
	ID   string `json:"id"`
	Name string `json:"name"`
}

type antigravitySubscriptionPayload struct {
	CurrentTier      antigravityTier `json:"currentTier"`
	CurrentTierSnake antigravityTier `json:"current_tier"`
	PaidTier         antigravityTier `json:"paidTier"`
	PaidTierSnake    antigravityTier `json:"paid_tier"`
	Body             json.RawMessage `json:"body"`
}

// antigravityPlan fetches the subscription summary best-effort: any failure
// yields an empty plan and never fails the quota windows.
func antigravityPlan(ctx context.Context, client *http.Client, headers map[string]string) string {
	var payload antigravitySubscriptionPayload
	if err := DoJSON(ctx, client, http.MethodPost, antigravitySubscriptionURL, headers, []byte(antigravitySubscriptionBody), &payload); err != nil {
		return ""
	}
	payload = payload.unwrapped()
	payload.normalize()
	effective := payload.CurrentTier
	if strings.TrimSpace(payload.PaidTier.ID) != "" {
		effective = payload.PaidTier
	}
	if plan, ok := antigravityPlanByTierID[strings.TrimSpace(effective.ID)]; ok {
		return plan
	}
	if strings.TrimSpace(effective.Name) != "" {
		return strings.TrimSpace(effective.Name)
	}
	return strings.TrimSpace(effective.ID)
}

func (p *antigravitySubscriptionPayload) normalize() {
	if strings.TrimSpace(p.CurrentTier.ID) == "" {
		p.CurrentTier.ID = strings.TrimSpace(p.CurrentTierSnake.ID)
	}
	if strings.TrimSpace(p.CurrentTier.Name) == "" {
		p.CurrentTier.Name = strings.TrimSpace(p.CurrentTierSnake.Name)
	}
	if strings.TrimSpace(p.PaidTier.ID) == "" {
		p.PaidTier.ID = strings.TrimSpace(p.PaidTierSnake.ID)
	}
	if strings.TrimSpace(p.PaidTier.Name) == "" {
		p.PaidTier.Name = strings.TrimSpace(p.PaidTierSnake.Name)
	}
}

func (p antigravitySubscriptionPayload) unwrapped() antigravitySubscriptionPayload {
	if strings.TrimSpace(p.CurrentTier.ID) != "" ||
		strings.TrimSpace(p.CurrentTier.Name) != "" ||
		strings.TrimSpace(p.PaidTier.ID) != "" ||
		strings.TrimSpace(p.PaidTier.Name) != "" ||
		strings.TrimSpace(p.CurrentTierSnake.ID) != "" ||
		strings.TrimSpace(p.PaidTierSnake.ID) != "" ||
		len(p.Body) == 0 {
		return p
	}
	var nested antigravitySubscriptionPayload
	if err := json.Unmarshal(p.Body, &nested); err == nil {
		return nested
	}
	var text string
	if err := json.Unmarshal(p.Body, &text); err == nil && strings.TrimSpace(text) != "" {
		var parsed antigravitySubscriptionPayload
		if err := json.Unmarshal([]byte(text), &parsed); err == nil {
			return parsed
		}
	}
	return p
}
