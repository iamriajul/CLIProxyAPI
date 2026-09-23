package tui

import (
	"strings"
	"testing"
	"time"
)

func TestCatalogAge(t *testing.T) {
	for _, testCase := range []struct {
		in   time.Duration
		want string
	}{
		{45 * time.Second, "45s"},
		{12 * time.Minute, "12m"},
		{3 * time.Hour, "3h"},
		{49 * time.Hour, "2d"},
	} {
		if got := catalogAge(testCase.in); got != testCase.want {
			t.Fatalf("catalogAge(%v) = %q, want %q", testCase.in, got, testCase.want)
		}
	}
}

func TestRenderCatalogSectionStates(t *testing.T) {
	now := time.Now().UTC().Format(time.RFC3339)
	var model dashboardModel
	out := model.renderCatalogSection([]map[string]any{
		{"id": "opencode-go", "source": "live", "models": float64(39), "fetched_at": now},
		{"id": "zai-coding-plan", "source": "fallback", "models": float64(7), "last_error": "boom"},
	})
	for _, want := range []string{"opencode-go", "zai-coding-plan", "39", "7", "boom"} {
		if !strings.Contains(out, want) {
			t.Fatalf("card missing %q:\n%s", want, out)
		}
	}
	// Nil catalog (old server) renders nothing.
	var empty dashboardModel
	if got := empty.renderCatalogSection(nil); got != "" {
		t.Fatalf("nil catalog = %q, want empty", got)
	}
}
