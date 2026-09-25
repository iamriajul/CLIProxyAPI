package api

import (
	"testing"
	"time"

	coreauth "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/auth"
)

func quotaWindowsByName(t *testing.T, windows []inferenceQuotaWindow) map[string]inferenceQuotaWindow {
	t.Helper()
	if windows == nil {
		t.Fatal("windows is nil, want empty array")
	}
	byName := make(map[string]inferenceQuotaWindow, len(windows))
	for _, window := range windows {
		byName[window.Name] = window
	}
	return byName
}

func requireUsedPercent(t *testing.T, window inferenceQuotaWindow, want float64) {
	t.Helper()
	if window.UsedPercent == nil || *window.UsedPercent != want {
		t.Fatalf("%s used_percent = %v, want %v", window.Name, window.UsedPercent, want)
	}
}

func requireResetAt(t *testing.T, window inferenceQuotaWindow, want time.Time) {
	t.Helper()
	if window.ResetAt == nil || !window.ResetAt.Equal(want) {
		t.Fatalf("%s reset_at = %v, want %v", window.Name, window.ResetAt, want)
	}
}

func TestInferenceQuotaWindowsCodex(t *testing.T) {
	observed := time.Date(2026, 8, 22, 12, 0, 0, 0, time.UTC)
	quota := coreauth.QuotaState{
		ObservedAt: observed,
		Signals: map[string]string{
			"X-Codex-Plan-Type":                        "pro",
			"X-Codex-Primary-Used-Percent":             "51",
			"X-Codex-Primary-Window-Minutes":           "300",
			"X-Codex-Primary-Reset-After-Seconds":      "600",
			"X-Codex-Primary-Reset-At":                 "1787588999",
			"X-Codex-Secondary-Used-Percent":           "35",
			"X-Codex-Secondary-Window-Minutes":         "10080",
			"X-Codex-Secondary-Reset-After-Seconds":    "3600",
			"X-Codex-Bengalfox-Secondary-Used-Percent": "12",
			"X-Codex-Bengalfox-Limit-Name":             "GPT-5.3-Codex-Spark",
			"X-Codex-Allowed":                          "true",
			"X-Codex-Primary-Bogus-Metric":             "1",
			"X-Codex-Secondary-Used-Percent-Broken":    "nan",
		},
	}
	windows := inferenceQuotaWindows("codex", quota)
	if len(windows) != 3 {
		t.Fatalf("windows = %+v, want 3", windows)
	}
	if windows[0].Name != "5h" || windows[1].Name != "7d" || windows[2].Name != "bengalfox/7d" {
		t.Fatalf("window order = %v", windows)
	}
	byName := quotaWindowsByName(t, windows)
	fiveHour := byName["5h"]
	requireUsedPercent(t, fiveHour, 51)
	// Absolute reset wins over relative.
	requireResetAt(t, fiveHour, time.Unix(1787588999, 0).UTC())
	weekly := byName["7d"]
	requireUsedPercent(t, weekly, 35)
	requireResetAt(t, weekly, observed.Add(time.Hour))
	namespaced := byName["bengalfox/7d"]
	requireUsedPercent(t, namespaced, 12)
	if namespaced.ResetAt != nil {
		t.Fatalf("bengalfox/7d reset_at = %v, want nil", namespaced.ResetAt)
	}
}

func TestInferenceQuotaWindowsCodexMonthlyAndFallback(t *testing.T) {
	quota := coreauth.QuotaState{
		Signals: map[string]string{
			"X-Codex-Secondary-Used-Percent":   "80",
			"X-Codex-Secondary-Window-Minutes": "43200",
		},
	}
	windows := inferenceQuotaWindows("codex", quota)
	if len(windows) != 1 || windows[0].Name != "monthly" {
		t.Fatalf("windows = %+v, want monthly", windows)
	}
	requireUsedPercent(t, windows[0], 80)

	// Without durations, position decides: primary to 5h, secondary to 7d.
	fallback := inferenceQuotaWindows("codex", coreauth.QuotaState{
		Signals: map[string]string{
			"X-Codex-Primary-Used-Percent":   "10",
			"X-Codex-Secondary-Used-Percent": "20",
		},
	})
	if len(fallback) != 2 || fallback[0].Name != "5h" || fallback[1].Name != "7d" {
		t.Fatalf("fallback windows = %+v", fallback)
	}
}

func TestInferenceQuotaWindowsClaude(t *testing.T) {
	quota := coreauth.QuotaState{
		Signals: map[string]string{
			"Anthropic-Ratelimit-Unified-5h-Status":            "allowed",
			"Anthropic-Ratelimit-Unified-5h-Utilization":       "0.0",
			"Anthropic-Ratelimit-Unified-5h-Reset":             "1787296800",
			"Anthropic-Ratelimit-Unified-7d-Status":            "allowed",
			"Anthropic-Ratelimit-Unified-7d-Utilization":       "0.53",
			"Anthropic-Ratelimit-Unified-7d-Reset":             "1787695200",
			"Anthropic-Ratelimit-Unified-Status":               "allowed",
			"Anthropic-Ratelimit-Unified-Representative-Claim": "five_hour",
		},
	}
	windows := inferenceQuotaWindows(" Claude ", quota)
	if len(windows) != 2 || windows[0].Name != "5h" || windows[1].Name != "7d" {
		t.Fatalf("windows = %+v", windows)
	}
	byName := quotaWindowsByName(t, windows)
	fiveHour := byName["5h"]
	requireUsedPercent(t, fiveHour, 0)
	requireResetAt(t, fiveHour, time.Unix(1787296800, 0).UTC())
	if fiveHour.Status != "allowed" {
		t.Fatalf("5h status = %q", fiveHour.Status)
	}
	sevenDay := byName["7d"]
	requireUsedPercent(t, sevenDay, 53)
	requireResetAt(t, sevenDay, time.Unix(1787695200, 0).UTC())
}

func TestInferenceQuotaWindowsDevin(t *testing.T) {
	quota := coreauth.QuotaState{
		Signals: map[string]string{
			"plan":                           "Pro",
			"daily_quota_remaining_percent":  "100%",
			"weekly_quota_remaining_percent": "50%",
			"daily_quota_reset_at":           "2026-09-26T00:00:00Z",
			"weekly_quota_reset_at":          "2026-09-30T00:00:00Z",
		},
	}
	windows := inferenceQuotaWindows("devin", quota)
	if len(windows) != 2 || windows[0].Name != "daily" || windows[1].Name != "weekly" {
		t.Fatalf("windows = %+v", windows)
	}
	byName := quotaWindowsByName(t, windows)
	requireUsedPercent(t, byName["daily"], 0)
	requireUsedPercent(t, byName["weekly"], 50)
	requireResetAt(t, byName["daily"], time.Date(2026, 9, 26, 0, 0, 0, 0, time.UTC))
}

func TestInferenceQuotaWindowsEmpty(t *testing.T) {
	for _, provider := range []string{"", "kimi", "xai", "meta", "opencode", "codex", "claude", "devin"} {
		windows := inferenceQuotaWindows(provider, coreauth.QuotaState{})
		if windows == nil || len(windows) != 0 {
			t.Fatalf("%s windows = %#v, want empty array", provider, windows)
		}
	}
	// Unparseable values never produce phantom windows.
	windows := inferenceQuotaWindows("codex", coreauth.QuotaState{
		Signals: map[string]string{"X-Codex-Primary-Used-Percent": "nan-percent"},
	})
	if len(windows) != 0 {
		t.Fatalf("windows = %+v, want empty", windows)
	}
}
