package config

import (
	"strings"
	"testing"

	"gopkg.in/yaml.v3"
)

// Regression: the web-search section is a root-level section (SDKConfig field),
// so v8 validation and migration must both accept it. It was missing from
// v8AllowedRoots, which failed the example-config test and made migration
// comment out a user's live web-search configuration.
func TestV8WebSearchRootSectionIsValid(t *testing.T) {
	raw := []byte("server:\n  port: 8317\nweb-search:\n  enabled: true\n  limit: 5\n")
	if err := ValidateV8Config(raw); err != nil {
		t.Fatalf("root web-search section must validate: %v", err)
	}
	cfg, err := ParseConfigBytes(raw)
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if !cfg.SDKConfig.WebSearch.Enabled {
		t.Fatal("web-search.enabled did not reach runtime config")
	}
	if cfg.SDKConfig.WebSearch.Limit != 5 {
		t.Fatalf("web-search.limit = %d, want 5", cfg.SDKConfig.WebSearch.Limit)
	}
}

// Migration must preserve a user's web-search block rather than commenting it
// out as an unknown section.
func TestV8MigrationPreservesWebSearch(t *testing.T) {
	raw := []byte("port: 8317\nweb-search:\n  enabled: true\n  limit: 5\n")
	out, changed, err := NormalizeConfigLayout(raw, true)
	if err != nil {
		t.Fatalf("migrate: %v", err)
	}
	if !changed {
		t.Fatal("expected migration to add config-version")
	}
	migrated := string(out)
	if strings.Contains(migrated, "# web-search:") {
		t.Fatalf("migration commented out web-search, losing user config:\n%s", migrated)
	}
	if !strings.Contains(migrated, "enabled: true") {
		t.Fatalf("web-search settings lost during migration:\n%s", migrated)
	}
	if err := ValidateV8Config(out); err != nil {
		t.Fatalf("migrated file must validate: %v", err)
	}
}

func TestWebSearchAloneIsNotV8Layout(t *testing.T) {
	raw := "port: 8317\nweb-search:\n  enabled: true\n"
	var doc yaml.Node
	if err := yaml.Unmarshal([]byte(raw), &doc); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if IsV8ConfigLayout(doc.Content[0]) {
		t.Fatal("a config whose only v8-shaped root is web-search is still legacy layout")
	}
}
