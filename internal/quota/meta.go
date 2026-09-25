// Meta Muse quota fetcher.
//
// Port of the CPAMC management-UI quota adapter
// (src/features/quota/providers/meta/requests.ts +
// src/services/api/metaQuota.ts parseMetaQuotaPayload).
// Upstream: POST https://api.meta.ai/muse-code/key with body '{}',
// authorized as Bearer with the persisted DCA token (dca:...). The minted
// LLM key (LLM|...) is never a valid credential here. Only the subscription
// fields (plan name, rolling and weekly used percents, reset instants) are
// kept; api_key and identity fields the endpoint echoes are discarded and
// never logged.
package quota

import (
	"context"
	"errors"
	"math"
	"net/http"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"
)

// metaMuseQuotaURL is the Muse key/quota endpoint. It is a variable (not a
// constant) so tests can redirect it at an httptest server; production code
// always uses the default.
var metaMuseQuotaURL = "https://api.meta.ai/muse-code/key"

var metaDcaTokenPattern = regexp.MustCompile(`^dca:\S+$`)

// MetaFetcher refreshes Meta Muse quota via the key endpoint.
type MetaFetcher struct{}

// NewMetaFetcher builds a MetaFetcher.
func NewMetaFetcher() *MetaFetcher { return &MetaFetcher{} }

// Provider returns the canonical provider key.
func (*MetaFetcher) Provider() string { return "meta" }

// Fetch returns the rolling and weekly quota windows plus the plan name.
func (*MetaFetcher) Fetch(ctx context.Context, req FetchRequest) (*Snapshot, error) {
	token := metaDCAToken(req)
	if token == "" {
		return nil, errors.New("meta quota fetch: missing dca_token")
	}
	var raw any
	headers := map[string]string{
		"Accept":        "application/json",
		"Content-Type":  "application/json",
		"Authorization": "Bearer " + token,
		"x-api-version": "1.0.0",
	}
	if err := DoJSON(ctx, req.Client, http.MethodPost, metaMuseQuotaURL, headers, []byte("{}"), &raw); err != nil {
		return nil, err
	}
	return metaSnapshotFromPayload(raw)
}

// metaDCAToken extracts only the persisted DCA field; like the adapter's
// readDcaToken it never falls back to the minted LLM key.
func metaDCAToken(req FetchRequest) string {
	if req.Auth == nil {
		return ""
	}
	var candidates []string
	if req.Auth.Attributes != nil {
		candidates = append(candidates, req.Auth.Attributes["dca_token"])
	}
	if req.Auth.Metadata != nil {
		if token, _ := req.Auth.Metadata["dca_token"].(string); token != "" {
			candidates = append(candidates, token)
		}
	}
	for _, candidate := range candidates {
		trimmed := strings.TrimSpace(candidate)
		if metaDcaTokenPattern.MatchString(trimmed) {
			return trimmed
		}
	}
	return ""
}

func metaFiniteNumber(value any) (float64, bool) {
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

func metaUsedPercent(value any) *float64 {
	percent, ok := metaFiniteNumber(value)
	if !ok {
		return nil
	}
	return UsedPercent(math.Min(math.Max(percent, 0), 100))
}

// metaResetTime converts resets_at epoch seconds to an instant. Non-positive
// or unparseable values carry no reset.
func metaResetTime(value any) *time.Time {
	seconds, ok := metaFiniteNumber(value)
	if !ok || seconds <= 0 {
		return nil
	}
	at := time.Unix(int64(seconds), 0).UTC()
	return &at
}

func metaRecord(value any) map[string]any {
	record, ok := value.(map[string]any)
	if !ok {
		return map[string]any{}
	}
	return record
}

func metaSnapshotFromPayload(payload any) (*Snapshot, error) {
	root, ok := payload.(map[string]any)
	if !ok {
		return nil, errors.New("meta quota fetch: invalid quota data")
	}
	usage := metaRecord(root["subs_usage"])
	plan := ""
	if name, _ := root["subs_tier_name"].(string); strings.TrimSpace(name) != "" {
		plan = strings.TrimSpace(name)
	} else if tier, _ := usage["tier"].(string); strings.TrimSpace(tier) != "" {
		plan = strings.TrimSpace(tier)
	}
	snapshot := &Snapshot{Plan: plan}
	for _, entry := range []struct{ key, name string }{{"window", "window"}, {"weekly", "7d"}} {
		raw := metaRecord(usage[entry.key])
		window := Window{Name: entry.name, UsedPercent: metaUsedPercent(raw["used_percent"])}
		if reset := metaResetTime(raw["resets_at"]); reset != nil {
			window.ResetAt = reset
		}
		// Skip entries with neither a used percent nor a reset instant.
		if window.UsedPercent == nil && window.ResetAt == nil {
			continue
		}
		snapshot.Windows = append(snapshot.Windows, window)
	}
	if len(snapshot.Windows) == 0 && snapshot.Plan == "" {
		return nil, nil
	}
	sort.Slice(snapshot.Windows, func(i, j int) bool { return snapshot.Windows[i].Name < snapshot.Windows[j].Name })
	return snapshot, nil
}
