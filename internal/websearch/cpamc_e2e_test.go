package websearch

import (
	"testing"

	"gopkg.in/yaml.v3"
)

// The CPAMC v1.24.907 visual editor emits the config below. Reproduce its
// exact serialization and confirm the released backend accepts it —
// PutConfigYAML unmarshals before writing, so a shape mismatch fails the
// whole save.
func TestCpamcVisualConfigRoundTrips(t *testing.T) {
	// Unquoted ints, as the fixed int setter emits them.
	emitted := `web-search:
  enabled: true
  order:
    - duckduckgo
    - ecosia
  exclude:
    - parallel
  timeout-seconds: 30
  limit: 10
  max-searches: 3
  public-fanout-soft-seconds: 5
  public-fanout-hard-seconds: 30
  perplexity-api-key: sk-test
  openrouter-api-key: sk-or-test
`
	// web-search is a block on the parent config, not a bare websearch.Config.
	var root struct {
		WebSearch Config `yaml:"web-search"`
	}
	if err := yaml.Unmarshal([]byte(emitted), &root); err != nil {
		t.Fatalf("released backend rejected the UI's config: %v", err)
	}
	cfg := root.WebSearch
	if !cfg.Enabled {
		t.Error("enabled did not round-trip")
	}
	if cfg.TimeoutSeconds != 30 || cfg.Limit != 10 || cfg.MaxSearches != 3 {
		t.Errorf("ints did not round-trip: %+v", cfg)
	}
	if cfg.PublicFanoutSoftSeconds != 5 || cfg.PublicFanoutHardSeconds != 30 {
		t.Errorf("fanout ints did not round-trip: %+v", cfg)
	}
	if len(cfg.Order) != 2 || cfg.Order[0] != "duckduckgo" {
		t.Errorf("order did not round-trip: %v", cfg.Order)
	}
	if len(cfg.Exclude) != 1 || cfg.Exclude[0] != "parallel" {
		t.Errorf("exclude did not round-trip: %v", cfg.Exclude)
	}
	if cfg.PerplexityAPIKey != "sk-test" {
		t.Errorf("perplexity key did not round-trip: %q", cfg.PerplexityAPIKey)
	}
	if cfg.OpenRouterAPIKey != "sk-or-test" {
		t.Errorf("openrouter key did not round-trip: %q", cfg.OpenRouterAPIKey)
	}
	// The borrowed-credential resolver must see the keys the UI wrote.
	if cfg.PerplexityKey() != "sk-test" {
		t.Errorf("PerplexityKey() = %q", cfg.PerplexityKey())
	}
	if cfg.OpenRouterKey() != "sk-or-test" {
		t.Errorf("OpenRouterKey() = %q", cfg.OpenRouterKey())
	}
}
