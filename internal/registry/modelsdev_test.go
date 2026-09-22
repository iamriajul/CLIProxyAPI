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
	sections, err := ConvertModelsDevCatalog(loadModelsDevFixture(t))
	if err != nil {
		t.Fatalf("ConvertModelsDevCatalog: %v", err)
	}
	if !sections.HasOpencode || !sections.HasZai {
		t.Fatalf("presence = %v/%v, want true/true", sections.HasOpencode, sections.HasZai)
	}
	opencode, zai := sections.Opencode, sections.Zai
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
	sections, err := ConvertModelsDevCatalog(loadModelsDevFixture(t))
	if err != nil {
		t.Fatalf("ConvertModelsDevCatalog: %v", err)
	}
	for _, m := range append(append([]*ModelInfo{}, sections.Opencode...), sections.Zai...) {
		if m.ID == "zen-only-model" {
			t.Fatal("Zen (opencode) model leaked into Go/plan output")
		}
	}
}

func TestConvertModelsDevCatalogPresence(t *testing.T) {
	if _, err := ConvertModelsDevCatalog([]byte(`{"other": {"models": {}}}`)); err == nil {
		t.Fatal("expected error when neither provider key exists")
	}
	// A missing key reports absent with a nil slice — the store must keep
	// previous data, never wipe it.
	sections, err := ConvertModelsDevCatalog([]byte(`{"zai-coding-plan": {"models": {"glm-4.7": {"id": "glm-4.7", "limit": {"context": 1, "output": 1}}}}}`))
	if err != nil {
		t.Fatalf("single provider: %v", err)
	}
	if sections.HasOpencode || sections.Opencode != nil {
		t.Fatalf("opencode presence = %v/%v, want false/nil", sections.HasOpencode, sections.Opencode)
	}
	if !sections.HasZai || len(sections.Zai) != 1 {
		t.Fatalf("zai presence = %v/%d, want true/1", sections.HasZai, len(sections.Zai))
	}
	// A present-but-empty section converts cleanly; storing or publishing
	// it is the caller's refusal (store keeps previous, CLI fails loudly).
	sections, err = ConvertModelsDevCatalog([]byte(`{"opencode-go": {"models": {}}}`))
	if err != nil {
		t.Fatalf("empty section: %v", err)
	}
	if !sections.HasOpencode || len(sections.Opencode) != 0 {
		t.Fatalf("empty opencode presence = %v/%d, want true/0", sections.HasOpencode, len(sections.Opencode))
	}
	if _, err := ConvertModelsDevCatalog([]byte(`[]`)); err == nil {
		t.Fatal("expected error for non-object payload")
	}
}

func TestConvertModelsDevCatalogDedupsIDs(t *testing.T) {
	sections, err := ConvertModelsDevCatalog([]byte(`{"opencode-go": {"models": {
		"b-key": {"id": "same", "name": "Second", "limit": {"context": 2, "output": 2}},
		"a-key": {"id": "same", "name": "First", "limit": {"context": 1, "output": 1}}
	}}}`))
	if err != nil {
		t.Fatalf("ConvertModelsDevCatalog: %v", err)
	}
	if len(sections.Opencode) != 1 {
		t.Fatalf("got %d models, want 1 deduped", len(sections.Opencode))
	}
	if got := sections.Opencode[0].DisplayName; got != "First" {
		t.Fatalf("dedup winner = %q, want First (sorted-key order)", got)
	}
}

func TestModelsDevReleaseUnixFormats(t *testing.T) {
	if got := modelsDevReleaseUnix("2026-06-13"); got != 1781308800 {
		t.Fatalf("date-only = %d", got)
	}
	if got := modelsDevReleaseUnix("2026-06-13T00:00:00Z"); got != 1781308800 {
		t.Fatalf("rfc3339 = %d", got)
	}
	if got := modelsDevReleaseUnix("not-a-date"); got != 0 {
		t.Fatalf("garbage = %d, want 0", got)
	}
}
