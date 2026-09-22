package registry

import (
	"encoding/json"
	"os"
	"testing"
)

func TestModelsDevLiveOverlayPrecedence(t *testing.T) {
	resetModelsDevLiveForTest()
	t.Cleanup(resetModelsDevLiveForTest)

	before := GetOpencodeModels()
	if len(before) == 0 {
		t.Fatal("expected catalog/builtin fallback before live load")
	}

	data, err := os.ReadFile("modelsdev_testdata_api.json")
	if err != nil {
		t.Fatalf("read fixture: %v", err)
	}
	changed, err := loadModelsDevLiveFromBytes(data, "test")
	if err != nil {
		t.Fatalf("load live: %v", err)
	}
	if len(changed) != 2 {
		t.Fatalf("changed = %v, want [opencode zai]", changed)
	}

	got := GetOpencodeModels()
	if len(got) != 2 {
		t.Fatalf("GetOpencodeModels with live = %d, want 2 (live wins, no builtin merge)", len(got))
	}
	if got[0].ID != "glm-5.2" || got[1].ID != "gpt-5.6-luna" {
		t.Fatalf("live IDs = [%s %s]", got[0].ID, got[1].ID)
	}
	gotZai := GetZaiModels()
	if len(gotZai) != 2 {
		t.Fatalf("GetZaiModels with live = %d, want 2", len(gotZai))
	}

	// Same bytes again: no change reported.
	changed, err = loadModelsDevLiveFromBytes(data, "test")
	if err != nil {
		t.Fatalf("reload live: %v", err)
	}
	if len(changed) != 0 {
		t.Fatalf("reload changed = %v, want none", changed)
	}

	// Reset restores fallback.
	resetModelsDevLiveForTest()
	if got := len(GetZaiModels()); got < 7 {
		t.Fatalf("GetZaiModels after reset = %d, want fallback", got)
	}
}

func TestModelsDevLiveRejectsBadPayload(t *testing.T) {
	resetModelsDevLiveForTest()
	t.Cleanup(resetModelsDevLiveForTest)
	if _, err := loadModelsDevLiveFromBytes([]byte(`{"nope": {}}`), "test"); err == nil {
		t.Fatal("expected error for payload without tracked providers")
	}
	if len(GetModelsDevLive("opencode")) != 0 {
		t.Fatal("failed load must not populate live store")
	}
}

func TestModelsDevLiveBeatsFallback(t *testing.T) {
	resetModelsDevLiveForTest()
	t.Cleanup(resetModelsDevLiveForTest)

	if got := len(GetOpencodeModels()); got < 39 {
		t.Fatalf("fallback opencode = %d, want >= 39", got)
	}
	if got := len(GetZaiModels()); got != 7 {
		t.Fatalf("fallback zai = %d, want 7", got)
	}

	data, err := os.ReadFile("modelsdev_testdata_api.json")
	if err != nil {
		t.Fatalf("read fixture: %v", err)
	}
	if _, err := loadModelsDevLiveFromBytes(data, "test"); err != nil {
		t.Fatalf("load live: %v", err)
	}
	if got := GetOpencodeModels(); len(got) != 2 || got[0].ID != "glm-5.2" {
		t.Fatalf("live opencode = %v, want 2 fixture models", idsOf(got))
	}
	if got := GetZaiModels(); len(got) != 2 {
		t.Fatalf("live zai = %v, want 2 fixture models", idsOf(got))
	}

	resetModelsDevLiveForTest()
	if got := len(GetOpencodeModels()); got < 39 {
		t.Fatalf("restored fallback opencode = %d, want >= 39", got)
	}
}

func idsOf(models []*ModelInfo) []string {
	out := make([]string, 0, len(models))
	for _, m := range models {
		if m != nil {
			out = append(out, m.ID)
		}
	}
	return out
}

func TestModelsDevLiveKeepsMissingSection(t *testing.T) {
	resetModelsDevLiveForTest()
	t.Cleanup(resetModelsDevLiveForTest)

	data, err := os.ReadFile("modelsdev_testdata_api.json")
	if err != nil {
		t.Fatalf("read fixture: %v", err)
	}
	if _, err := loadModelsDevLiveFromBytes(data, "test"); err != nil {
		t.Fatalf("load live: %v", err)
	}
	rev := GetModelsDevRevision()
	if rev == 0 {
		t.Fatal("revision must advance on first load")
	}

	// Payload carrying only opencode-go must preserve the zai section and
	// report no change when opencode content is identical.
	partial := []byte(`{"opencode-go": {"models": {
		"glm-5.2": {"id": "glm-5.2", "name": "GLM-5.2", "reasoning": true,
			"reasoning_options": [{"type": "effort", "values": ["high", "max"]}],
			"tool_call": true, "structured_output": true, "temperature": true,
			"release_date": "2026-06-13",
			"modalities": {"input": ["text"], "output": ["text"]},
			"limit": {"context": 1000000, "output": 131072}},
		"gpt-5.6-luna": {"id": "gpt-5.6-luna", "name": "GPT-5.6 Luna", "reasoning": true,
			"reasoning_options": [{"type": "effort", "values": ["none", "low", "medium", "high", "xhigh", "max"]}],
			"tool_call": true, "structured_output": true, "temperature": false,
			"release_date": "2026-07-09",
			"modalities": {"input": ["text", "image", "pdf"], "output": ["text"]},
			"limit": {"context": 1050000, "input": 922000, "output": 128000}}
	}}}`)
	changed, err := loadModelsDevLiveFromBytes(partial, "test")
	if err != nil {
		t.Fatalf("load partial: %v", err)
	}
	if len(changed) != 0 {
		t.Fatalf("partial identical changed = %v, want none", changed)
	}
	if got := GetZaiModels(); len(got) != 2 {
		t.Fatalf("zai after partial load = %d, want 2 preserved", len(got))
	}
	if got := GetModelsDevRevision(); got != rev {
		t.Fatalf("revision = %d, want %d (no semantic change)", got, rev)
	}

	// A present-but-empty section keeps previous data instead of wiping it.
	changed, err = loadModelsDevLiveFromBytes([]byte(`{"zai-coding-plan": {"models": {}}}`), "test")
	if err != nil {
		t.Fatalf("load empty section: %v", err)
	}
	if len(changed) != 0 {
		t.Fatalf("empty section changed = %v, want none", changed)
	}
	if got := GetZaiModels(); len(got) != 2 {
		t.Fatalf("zai after empty section = %d, want 2 preserved", len(got))
	}
	if got := GetOpencodeModels(); len(got) != 2 {
		t.Fatalf("opencode after empty section = %d, want 2 preserved", len(got))
	}
}

func TestModelsDevFallbackExplicitParity(t *testing.T) {
	resetModelsDevLiveForTest()
	t.Cleanup(resetModelsDevLiveForTest)

	// Fallback entries must carry the converter's Explicit flags so harness
	// constraints match the live overlay for identical model IDs.
	for _, m := range append(GetOpencodeModels(), GetZaiModels()...) {
		if m == nil {
			continue
		}
		if !m.ExplicitInputModalities {
			t.Fatalf("%s: ExplicitInputModalities not set on fallback", m.ID)
		}
		if want := m.Thinking != nil; m.ExplicitThinking != want {
			t.Fatalf("%s: ExplicitThinking = %v, want %v", m.ID, m.ExplicitThinking, want)
		}
	}
}

// TestModelsDevSnapshotBuiltinConsistency pins models.json sections against
// the generated builtins field-by-field (via JSON, which excludes the
// json:"-" Explicit flags): both derive from the same converter output, so
// any render drift between rewriteModelsJSON and renderBuiltinModels fails
// loudly here instead of diverging silently in production.
func TestModelsDevSnapshotBuiltinConsistency(t *testing.T) {
	data := getModels()
	check := func(section string, catalog, builtins []*ModelInfo) {
		t.Helper()
		byID := func(models []*ModelInfo) map[string]*ModelInfo {
			out := make(map[string]*ModelInfo, len(models))
			for _, m := range models {
				if m != nil {
					out[m.ID] = m
				}
			}
			return out
		}
		catalogByID, builtinsByID := byID(catalog), byID(builtins)
		if len(catalogByID) != len(builtinsByID) {
			t.Fatalf("%s: catalog has %d models, builtins have %d", section, len(catalogByID), len(builtinsByID))
		}
		for id, c := range catalogByID {
			b := builtinsByID[id]
			if b == nil {
				t.Fatalf("%s: model %q in catalog but missing from builtins", section, id)
			}
			cJSON, err := json.Marshal(c)
			if err != nil {
				t.Fatalf("%s: marshal catalog %q: %v", section, id, err)
			}
			bJSON, err := json.Marshal(b)
			if err != nil {
				t.Fatalf("%s: marshal builtin %q: %v", section, id, err)
			}
			if string(cJSON) != string(bJSON) {
				t.Fatalf("%s: model %q diverges:\n catalog: %s\nbuiltin: %s", section, id, cJSON, bJSON)
			}
		}
	}
	check("opencode", data.Opencode, opencodeBuiltinModelInfos())
	check("zai", data.ZAI, zaiBuiltinModelInfos())
}

func TestModelsDevLiveCallerMutationIsolated(t *testing.T) {
	resetModelsDevLiveForTest()
	t.Cleanup(resetModelsDevLiveForTest)

	data, err := os.ReadFile("modelsdev_testdata_api.json")
	if err != nil {
		t.Fatalf("read fixture: %v", err)
	}
	if _, err := loadModelsDevLiveFromBytes(data, "test"); err != nil {
		t.Fatalf("load live: %v", err)
	}
	got := GetOpencodeModels()
	if len(got) == 0 {
		t.Fatal("expected live models")
	}
	got[0].ID = "mutated"
	got[0].SupportedInputModalities[0] = "mutated"
	got[0].Thinking.Levels[0] = "mutated"
	fresh := GetOpencodeModels()
	if fresh[0].ID == "mutated" || fresh[0].SupportedInputModalities[0] == "mutated" || fresh[0].Thinking.Levels[0] == "mutated" {
		t.Fatal("caller mutation leaked into the live store")
	}
}

func TestModelsDevLiveIgnoresKeyOrder(t *testing.T) {
	resetModelsDevLiveForTest()
	t.Cleanup(resetModelsDevLiveForTest)

	data, err := os.ReadFile("modelsdev_testdata_api.json")
	if err != nil {
		t.Fatalf("read fixture: %v", err)
	}
	if _, err := loadModelsDevLiveFromBytes(data, "test"); err != nil {
		t.Fatalf("load live: %v", err)
	}
	// Same document with top-level provider keys reordered: bytes differ
	// but converted sections are identical, so no change is reported.
	var doc map[string]json.RawMessage
	if err := json.Unmarshal(data, &doc); err != nil {
		t.Fatalf("decode fixture: %v", err)
	}
	reordered := []byte(`{"zai-coding-plan": ` + string(doc["zai-coding-plan"]) +
		`, "opencode-go": ` + string(doc["opencode-go"]) +
		`, "opencode": ` + string(doc["opencode"]) + `}`)
	changed, err := loadModelsDevLiveFromBytes(reordered, "test")
	if err != nil {
		t.Fatalf("load reordered: %v", err)
	}
	if len(changed) != 0 {
		t.Fatalf("reordered keys changed = %v, want none", changed)
	}
}
