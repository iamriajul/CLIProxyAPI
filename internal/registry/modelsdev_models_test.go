package registry

import (
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
