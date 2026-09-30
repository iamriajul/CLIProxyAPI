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
	if len(changed) != 1 || changed[0] != "opencode" {
		t.Fatalf("changed = %v, want [opencode] (the Z.AI section is no longer a live overlay)", changed)
	}

	got := GetOpencodeModels()
	if len(got) != 2 {
		t.Fatalf("GetOpencodeModels with live = %d, want 2 (live wins, no builtin merge)", len(got))
	}
	if got[0].ID != "glm-5.2" || got[1].ID != "gpt-5.6-luna" {
		t.Fatalf("live IDs = [%s %s]", got[0].ID, got[1].ID)
	}
	// A models.dev payload carrying a zai-coding-plan section must not move the
	// Z.AI lane: that lane is discovered from Z.AI's own plan-scoped catalog,
	// so a third-party section cannot be allowed to overwrite it.
	if got := idsOf(GetZaiModels()); len(got) < 7 {
		t.Fatalf("GetZaiModels with a live models.dev payload = %v, want the offline lanes", got)
	}
	if got := GetModelsDevLive("zai"); got != nil {
		t.Fatalf("models.dev live store returned %d Z.AI lanes; the Z.AI lane has no models.dev live section", len(got))
	}

	// Same bytes again: no change reported.
	changed, err = loadModelsDevLiveFromBytes(data, "test")
	if err != nil {
		t.Fatalf("reload live: %v", err)
	}

	if len(changed) != 0 {
		t.Fatalf("reload changed = %v, want none", changed)
	}

	// Reset restores the offline fallback for every lane, including Z.AI,
	// which has no models.dev live section to restore.
	resetModelsDevLiveForTest()
	if got := len(GetZaiModels()); got < 7 {
		t.Fatalf("GetZaiModels after reset = %d, want the offline snapshot", got)
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
	zaiOffline := idsOf(GetZaiModels())
	if len(zaiOffline) < 7 {
		t.Fatalf("fallback zai = %v, want the offline snapshot lanes", zaiOffline)
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
	// The Z.AI lane ignores models.dev entirely: its offline lanes are
	// unchanged by a live payload, and only a credential-scoped discovery
	// result can move it.
	if got := idsOf(GetZaiModels()); !equalModelsDevStrings(got, zaiOffline) {
		t.Fatalf("live models.dev payload moved the Z.AI lane to %v, want the unchanged %v", got, zaiOffline)
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

	// A payload carrying only opencode-go must report no change when the
	// opencode content is identical.
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
	if got := GetOpencodeModels(); len(got) != 2 {
		t.Fatalf("opencode after partial load = %d, want 2 preserved", len(got))
	}
	if got := GetModelsDevRevision(); got != rev {
		t.Fatalf("revision = %d, want %d (no semantic change)", got, rev)
	}

	// A present-but-empty tracked section keeps previous data instead of
	// wiping it. That contract is unchanged, but it now applies to the only
	// section the overlay tracks.
	changed, err = loadModelsDevLiveFromBytes([]byte(`{"opencode-go": {"models": {}}}`), "test")
	if err != nil {
		t.Fatalf("load empty section: %v", err)
	}
	if len(changed) != 0 {
		t.Fatalf("empty section changed = %v, want none", changed)
	}
	if got := GetOpencodeModels(); len(got) != 2 {
		t.Fatalf("opencode after empty section = %d, want 2 preserved", len(got))
	}
	if got := GetModelsDevRevision(); got != rev {
		t.Fatalf("revision = %d, want %d after an empty section", got, rev)
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

func TestGetModelsDevStatus(t *testing.T) {
	resetModelsDevLiveForTest()
	t.Cleanup(resetModelsDevLiveForTest)

	// Cold boot: the tracked section is on fallback, with no fetch metadata.
	// Only opencode-go is reported. The Z.AI lane is discovered from Z.AI
	// itself and is not part of a models.dev freshness payload.
	status := GetModelsDevStatus()
	if len(status.Providers) != 1 {
		t.Fatalf("providers = %d, want 1 (opencode-go only)", len(status.Providers))
	}
	row := status.Providers[0]
	if row.ID != "opencode-go" {
		t.Fatalf("provider id = %q, want opencode-go", row.ID)
	}
	if row.Source != "fallback" {
		t.Fatalf("source = %q, want fallback", row.Source)
	}
	if row.FetchedAt != nil || row.LastError != nil {
		t.Fatalf("metadata not nil on cold boot")
	}
	if row.Models < 39 {
		t.Fatalf("fallback count = %d, want >= 39", row.Models)
	}

	// Live load flips source and stamps fetch time.
	data, err := os.ReadFile("modelsdev_testdata_api.json")
	if err != nil {
		t.Fatalf("read fixture: %v", err)
	}
	if _, err := loadModelsDevLiveFromBytes(data, "test"); err != nil {
		t.Fatalf("load live: %v", err)
	}
	status = GetModelsDevStatus()
	row = status.Providers[0]
	if row.Source != "live" || row.FetchedAt == nil || row.Models != 2 {
		t.Fatalf("live row = %#v, want a live row with 2 models and a fetch time", row)
	}

	// A recorded failure surfaces without changing source.
	setModelsDevLastError("boom")
	row = GetModelsDevStatus().Providers[0]
	if row.LastError == nil || *row.LastError != "boom" {
		t.Fatalf("last_error missing")
	}
	if row.Source != "live" {
		t.Fatalf("source = %q, want live (error must not flip source)", row.Source)
	}
}
