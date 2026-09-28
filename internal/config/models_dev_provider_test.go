package config

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"gopkg.in/yaml.v3"
)

func TestOpenAICompatibilityModelsDevProviderDecoding(t *testing.T) {
	const yamlConfig = `
openai-compatibility:
  - name: test-provider
    models:
      - name: acme-reasoner
        alias: fast
        models-dev-provider: alpha
      - name: acme-plain
        alias: plain
`
	var cfg Config
	if errDecode := yaml.Unmarshal([]byte(yamlConfig), &cfg); errDecode != nil {
		t.Fatalf("yaml decode error: %v", errDecode)
	}
	if len(cfg.OpenAICompatibility) != 1 {
		t.Fatalf("OpenAICompatibility len = %d, want 1", len(cfg.OpenAICompatibility))
	}
	models := cfg.OpenAICompatibility[0].Models
	if len(models) != 2 {
		t.Fatalf("models len = %d, want 2", len(models))
	}
	if models[0].ModelsDevProvider != "alpha" {
		t.Fatalf("models[0].ModelsDevProvider = %q, want alpha", models[0].ModelsDevProvider)
	}
	if models[1].ModelsDevProvider != "" {
		t.Fatalf("models[1].ModelsDevProvider = %q, want empty when omitted", models[1].ModelsDevProvider)
	}

	const jsonConfig = `{"openai-compatibility":[{
		"name":"test-provider",
		"models":[
			{"name":"acme-reasoner","alias":"fast","models-dev-provider":"alpha"},
			{"name":"acme-plain","alias":"plain"}
		]
	}]}`
	var jsonCfg Config
	if errDecode := json.Unmarshal([]byte(jsonConfig), &jsonCfg); errDecode != nil {
		t.Fatalf("json decode error: %v", errDecode)
	}
	if got := jsonCfg.OpenAICompatibility[0].Models[0].ModelsDevProvider; got != "alpha" {
		t.Fatalf("json models[0].ModelsDevProvider = %q, want alpha", got)
	}
	if got := jsonCfg.OpenAICompatibility[0].Models[1].ModelsDevProvider; got != "" {
		t.Fatalf("json models[1].ModelsDevProvider = %q, want empty when omitted", got)
	}
}

func TestOpenAICompatibilityModelsDevProviderSurvivesSave(t *testing.T) {
	// The management save path re-marshals the whole config and merges it into
	// the on-disk tree, so a field the writer does not emit would silently
	// disappear from the operator's configuration.
	configPath := filepath.Join(t.TempDir(), "config.yaml")
	initialYAML := `openai-compatibility:
  - name: acme
    base-url: "https://api.acme.test/v1"
    api-key-entries:
      - api-key: "sk-test"
    models:
      - name: acme-reasoner
        alias: fast
        models-dev-provider: alpha
      - name: acme-plain
        alias: plain
`
	if errWrite := os.WriteFile(configPath, []byte(initialYAML), 0o600); errWrite != nil {
		t.Fatalf("os.WriteFile() error = %v", errWrite)
	}
	cfg, errLoad := LoadConfig(configPath)
	if errLoad != nil {
		t.Fatalf("LoadConfig() error = %v", errLoad)
	}
	if errSave := SaveConfigPreserveComments(configPath, cfg); errSave != nil {
		t.Fatalf("SaveConfigPreserveComments() error = %v", errSave)
	}
	savedBytes, errRead := os.ReadFile(configPath)
	if errRead != nil {
		t.Fatalf("os.ReadFile() error = %v", errRead)
	}
	saved := string(savedBytes)
	if !strings.Contains(saved, "models-dev-provider: alpha") {
		t.Fatalf("models-dev-provider lost on save:\n%s", saved)
	}
	// A model without a pin must not gain one, and the provider must survive.
	if strings.Count(saved, "models-dev-provider") != 1 {
		t.Fatalf("models-dev-provider must appear exactly once:\n%s", saved)
	}
	for _, want := range []string{"name: acme", "base-url:", "alias: fast", "alias: plain"} {
		if !strings.Contains(saved, want) {
			t.Fatalf("save dropped %q:\n%s", want, saved)
		}
	}

	reloaded, errLoad := LoadConfig(configPath)
	if errLoad != nil {
		t.Fatalf("reload error = %v", errLoad)
	}
	models := reloaded.OpenAICompatibility[0].Models
	if got := models[0].ModelsDevProvider; got != "alpha" {
		t.Fatalf("reloaded models[0].ModelsDevProvider = %q, want alpha", got)
	}
	if got := models[1].ModelsDevProvider; got != "" {
		t.Fatalf("reloaded models[1].ModelsDevProvider = %q, want empty", got)
	}
}
