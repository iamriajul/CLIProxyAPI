package registry

import (
	"os"
	"testing"
)

func loadModelsDevFixture(t *testing.T) []byte {
	t.Helper()
	data, err := os.ReadFile("modelsdev_testdata_api.json")
	if err != nil {
		t.Fatalf("read fixture: %v", err)
	}
	return data
}

func TestConvertModelsDevCatalogGolden(t *testing.T) {
	opencode, zai, err := ConvertModelsDevCatalog(loadModelsDevFixture(t))
	if err != nil {
		t.Fatalf("ConvertModelsDevCatalog: %v", err)
	}
	if len(opencode) != 2 || len(zai) != 2 {
		t.Fatalf("got %d opencode / %d zai, want 2 / 2", len(opencode), len(zai))
	}
	byID := func(models []*ModelInfo) map[string]*ModelInfo {
		out := make(map[string]*ModelInfo, len(models))
		for _, m := range models {
			out[m.ID] = m
		}
		return out
	}
	oc := byID(opencode)
	za := byID(zai)

	glm52 := oc["glm-5.2"]
	if glm52 == nil {
		t.Fatal("missing glm-5.2")
	}
	if glm52.ContextLength != 1000000 || glm52.MaxCompletionTokens != 131072 {
		t.Fatalf("glm-5.2 windows = %d/%d", glm52.ContextLength, glm52.MaxCompletionTokens)
	}
	if glm52.InputTokenLimit != 1000000 || glm52.OutputTokenLimit != 131072 {
		t.Fatalf("glm-5.2 token limits = %d/%d", glm52.InputTokenLimit, glm52.OutputTokenLimit)
	}
	if got := lvl(glm52); len(got) != 2 || got[0] != "high" || got[1] != "max" {
		t.Fatalf("glm-5.2 levels = %v", got)
	}
	if !glm52.ExplicitThinking || !glm52.ExplicitInputModalities {
		t.Fatal("glm-5.2 explicit flags not set")
	}
	if got := glm52.SupportedParameters; len(got) != 3 || got[0] != "tool_choice" || got[1] != "response_format" || got[2] != "temperature" {
		t.Fatalf("glm-5.2 params = %v", got)
	}
	if glm52.Type != "opencode" || glm52.OwnedBy != "opencode" || glm52.Object != "model" {
		t.Fatalf("glm-5.2 identity = %+v", glm52)
	}
	if glm52.Created != 1781308800 { // 2026-06-13 UTC
		t.Fatalf("glm-5.2 created = %d", glm52.Created)
	}

	luna := oc["gpt-5.6-luna"]
	if luna == nil {
		t.Fatal("missing gpt-5.6-luna")
	}
	if luna.ContextLength != 1050000 || luna.InputTokenLimit != 922000 || luna.MaxCompletionTokens != 128000 {
		t.Fatalf("luna windows = %d/%d/%d", luna.ContextLength, luna.InputTokenLimit, luna.MaxCompletionTokens)
	}
	if got := luna.SupportedParameters; len(got) != 2 || got[0] != "tool_choice" || got[1] != "response_format" {
		t.Fatalf("luna params (temperature:false must drop temperature) = %v", got)
	}
	if got := lvl(luna); len(got) != 6 || got[0] != "none" {
		t.Fatalf("luna levels = %v", got)
	}

	flash := za["glm-5.3-flash"]
	if flash == nil {
		t.Fatal("missing glm-5.3-flash")
	}
	if flash.Type != "zai" || flash.ContextLength != 1000000 || flash.MaxCompletionTokens != 131072 {
		t.Fatalf("flash = %+v", flash)
	}
	if got := flash.SupportedInputModalities; len(got) != 4 || got[3] != "pdf" {
		t.Fatalf("flash input modalities = %v", got)
	}

	glm47 := za["glm-4.7"]
	if glm47 == nil {
		t.Fatal("missing glm-4.7")
	}
	if got := lvl(glm47); len(got) != 3 || got[0] != "low" || got[1] != "medium" || got[2] != "high" {
		t.Fatalf("glm-4.7 toggle-only levels (default) = %v", got)
	}
	if got := glm47.SupportedParameters; len(got) != 2 || got[0] != "tool_choice" || got[1] != "temperature" {
		t.Fatalf("glm-4.7 params (null structured_output must drop response_format) = %v", got)
	}
}

func lvl(m *ModelInfo) []string {
	if m == nil || m.Thinking == nil {
		return nil
	}
	return m.Thinking.Levels
}

func TestConvertModelsDevCatalogIgnoresZen(t *testing.T) {
	opencode, zai, err := ConvertModelsDevCatalog(loadModelsDevFixture(t))
	if err != nil {
		t.Fatalf("ConvertModelsDevCatalog: %v", err)
	}
	for _, m := range append(append([]*ModelInfo{}, opencode...), zai...) {
		if m.ID == "zen-only-model" {
			t.Fatal("Zen (opencode) model leaked into Go/plan output")
		}
	}
}

func TestConvertModelsDevCatalogMissingProviders(t *testing.T) {
	if _, _, err := ConvertModelsDevCatalog([]byte(`{"other": {"models": {}}}`)); err == nil {
		t.Fatal("expected error when neither provider key exists")
	}
	// One provider present is fine; the other comes back empty.
	oc, zai, err := ConvertModelsDevCatalog([]byte(`{"opencode-go": {"models": {}}}`))
	if err != nil {
		t.Fatalf("single provider: %v", err)
	}
	if len(oc) != 0 || len(zai) != 0 {
		t.Fatalf("got %d/%d, want 0/0", len(oc), len(zai))
	}
	if _, _, err := ConvertModelsDevCatalog([]byte(`[]`)); err == nil {
		t.Fatal("expected error for non-object payload")
	}
}
