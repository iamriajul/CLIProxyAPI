// Z.AI GLM Coding Plan quota fetcher.
//
// Port of the CPAMC management-UI quota adapter
// (src/utils/quota/zai.ts + src/features/quota/providers/zai/data.ts).
// Upstream: GET https://api.z.ai/api/monitor/usage/quota/limit, authorized
// with the provisioned key verbatim (no Bearer prefix, per ZaiCreds). The
// endpoint reports one entry per meter/window; only the enforced 5-hour and
// weekly windows across the credit, request, and token meters are surfaced.
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

	zaiauth "github.com/router-for-me/CLIProxyAPI/v7/internal/auth/zai"
)

// zaiQuotaURL is the subscription quota endpoint. It is a variable (not a
// constant) so tests can redirect it at an httptest server; production code
// always uses the default.
var zaiQuotaURL = zaiauth.ZaiQuotaURL

// ZaiFetcher refreshes Z.AI quota windows via the monitor quota endpoint.
type ZaiFetcher struct{}

// NewZaiFetcher builds a ZaiFetcher.
func NewZaiFetcher() *ZaiFetcher { return &ZaiFetcher{} }

// Provider returns the canonical provider key.
func (*ZaiFetcher) Provider() string { return "zai" }

// Fetch returns the 5h/weekly meter windows plus the subscription tier.
func (*ZaiFetcher) Fetch(ctx context.Context, req FetchRequest) (*Snapshot, error) {
	var metadata map[string]any
	var attributes map[string]string
	if req.Auth != nil {
		metadata = req.Auth.Metadata
		attributes = req.Auth.Attributes
	}
	key := zaiauth.ZaiCreds(metadata, attributes)
	if key == "" {
		return nil, errors.New("zai quota fetch: missing api key")
	}
	var payload zaiQuotaPayload
	headers := map[string]string{
		"Accept":        "application/json",
		"Content-Type":  "application/json",
		"Authorization": key,
	}
	if err := DoJSON(ctx, req.Client, http.MethodGet, zaiQuotaURL, headers, nil, &payload); err != nil {
		return nil, err
	}
	return zaiSnapshotFromPayload(&payload)
}

type zaiLimitItem struct {
	Type          string `json:"type"`
	Usage         any    `json:"usage"`
	CurrentValue  any    `json:"currentValue"`
	Percentage    any    `json:"percentage"`
	NextResetTime any    `json:"nextResetTime"`
	Unit          any    `json:"unit"`
	Number        any    `json:"number"`
}

type zaiQuotaData struct {
	Limits []zaiLimitItem `json:"limits"`
	Level  string         `json:"level"`
}

type zaiQuotaPayload struct {
	Success bool          `json:"success"`
	Data    *zaiQuotaData `json:"data"`
}

func zaiNumber(value any) (float64, bool) {
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

// zaiWindowID resolves the enforced window identity. Only the 5-hour window
// (unit 3, count 5) and the weekly window (unit 6) are subscription windows;
// anything else returns false.
func zaiWindowID(unit, count float64, hasCount bool) (string, float64, bool) {
	if unit == 3 {
		// A missing count defaults to 1, which never matches the 5-hour window.
		actual := 1.0
		if hasCount {
			actual = count
		}
		if actual == 5 {
			return "5h", 5, true
		}
		return "", 0, false
	}
	if unit == 6 {
		return "7d", 24 * 7, true
	}
	return "", 0, false
}

func zaiResetTime(value any) *time.Time {
	num, ok := zaiNumber(value)
	if !ok || num <= 0 {
		return nil
	}
	var ms int64
	if num < 1e12 {
		ms = int64(math.Round(num * 1000))
	} else {
		ms = int64(math.Round(num))
	}
	at := time.UnixMilli(ms).UTC()
	return &at
}

func zaiSnapshotFromPayload(payload *zaiQuotaPayload) (*Snapshot, error) {
	if payload == nil || !payload.Success || payload.Data == nil {
		return nil, errors.New("zai quota fetch: empty quota data")
	}
	meters := map[string]string{
		"CREDIT_LIMIT": "credits",
		"TIME_LIMIT":   "requests",
		"TOKENS_LIMIT": "tokens",
	}
	snapshot := &Snapshot{Plan: strings.TrimSpace(payload.Data.Level)}
	for _, item := range payload.Data.Limits {
		meter, ok := meters[item.Type]
		if !ok {
			continue
		}
		unit, ok := zaiNumber(item.Unit)
		if !ok {
			continue
		}
		count, hasCount := zaiNumber(item.Number)
		windowID, _, ok := zaiWindowID(unit, count, hasCount)
		if !ok {
			continue
		}
		// Prefer the exact used/limit ratio; fall back to the server-rounded percent.
		var usedPercent *float64
		if used, ok := zaiNumber(item.CurrentValue); ok {
			if limit, ok := zaiNumber(item.Usage); ok && limit > 0 {
				usedPercent = UsedPercent(math.Min(math.Max(used/limit*100, 0), 100))
			}
		}
		if usedPercent == nil {
			if fallback, ok := zaiNumber(item.Percentage); ok && fallback >= 0 {
				usedPercent = UsedPercent(math.Min(fallback, 100))
			}
		}
		if usedPercent == nil {
			continue
		}
		window := Window{Name: meter + "/" + windowID, UsedPercent: usedPercent}
		if reset := zaiResetTime(item.NextResetTime); reset != nil {
			window.ResetAt = reset
		}
		snapshot.Windows = append(snapshot.Windows, window)
	}
	if len(snapshot.Windows) == 0 {
		return nil, nil
	}
	sort.Slice(snapshot.Windows, func(i, j int) bool { return snapshot.Windows[i].Name < snapshot.Windows[j].Name })
	return snapshot, nil
}
