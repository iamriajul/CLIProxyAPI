package websearch

import (
	"context"
	"io"
	"net/http"
	"strings"
	"testing"
)

// An operator who sets an endpoint in the config file must not be silently
// overridden by a globally exported variable. The xAI, Codex, OpenRouter and
// SearXNG resolvers already resolved explicit-first; these three did not.
func TestBaseURLExplicitConfigBeatsEnvironment(t *testing.T) {
	t.Setenv("ANTHROPIC_BASE_URL", "https://env-anthropic.example")
	t.Setenv("ANTHROPIC_SEARCH_BASE_URL", "https://env-search.example")
	t.Setenv("GEMINI_BASE_URL", "https://env-gemini.example")
	t.Setenv("FIRECRAWL_BASE_URL", "https://env-firecrawl.example")

	cfg := Config{
		AnthropicEndpoint: "https://cfg.example/",
		GeminiEndpoint:    "https://cfg-gemini.example",
		FirecrawlEndpoint: "https://cfg-firecrawl.example",
	}.WithDefaults()
	for _, tc := range []struct{ name, got, want string }{
		{"anthropic", cfg.AnthropicBaseURL(), "https://cfg.example"},
		{"gemini", cfg.GeminiBaseURL(), "https://cfg-gemini.example"},
		{"firecrawl", cfg.FirecrawlBaseURL(), "https://cfg-firecrawl.example"},
	} {
		if tc.got != tc.want {
			t.Errorf("%s = %q, want the explicit config %q", tc.name, tc.got, tc.want)
		}
	}

	// With nothing configured the environment still applies, and the
	// search-specific variable outranks the general one.
	empty := Config{}.WithDefaults()
	if got := empty.AnthropicBaseURL(); got != "https://env-search.example" {
		t.Errorf("anthropic env fallback = %q, want the search-specific variable to win", got)
	}
	if got := empty.GeminiBaseURL(); got != "https://env-gemini.example" {
		t.Errorf("gemini env fallback = %q", got)
	}
}

// When every retry is exhausted the last response is handed back so the
// caller can report it, but its body was already drained and closed. The
// caller must still see the upstream message rather than "empty error body".
func TestExhaustedRetriesStillReportTheUpstreamBody(t *testing.T) {
	attempts := 0
	cfg := Config{}.WithDefaults()
	cfg = cfg.WithDoer(stubDoer{handle: func(*http.Request) (*http.Response, error) {
		attempts++
		return &http.Response{
			StatusCode: http.StatusServiceUnavailable,
			Header:     http.Header{},
			Body:       io.NopCloser(strings.NewReader(`{"error":"upstream says quota exhausted"}`)),
		}, nil
	}})
	resp, errFetch := fetchWithRetry(context.Background(), cfg, "Test", "https://upstream.example/search", nil, nil)
	if errFetch != nil {
		t.Fatalf("expected the last response to be returned, got %v", errFetch)
	}
	if attempts < 2 {
		t.Fatalf("attempts = %d, want the retry loop to have run", attempts)
	}
	if resp == nil {
		t.Fatal("expected a response to report")
	}
	defer func() { _ = resp.Body.Close() }()
	bodyBytes, errRead := io.ReadAll(resp.Body)
	if errRead != nil {
		t.Fatalf("read retained body: %v", errRead)
	}
	body := string(bodyBytes)
	if !strings.Contains(body, "quota exhausted") {
		t.Fatalf("body = %q, want the upstream message to survive the exhausted retry path", body)
	}
}
