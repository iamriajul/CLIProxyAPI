package registry

import "testing"

func TestGetStaticModelDefinitionsByChannelSupportsGeminiInteractions(t *testing.T) {
	models := GetStaticModelDefinitionsByChannel("gemini-interactions")
	if len(models) == 0 {
		t.Fatal("GetStaticModelDefinitionsByChannel(gemini-interactions) returned no models")
	}
}

func TestGetMuseModelsIncludesSparkFamily(t *testing.T) {
	for _, channel := range []string{"muse", "muse-code", "muse_code"} {
		models := GetStaticModelDefinitionsByChannel(channel)
		if len(models) < 5 {
			t.Fatalf("GetStaticModelDefinitionsByChannel(%q) = %d models, want >= 5", channel, len(models))
		}
		ids := make(map[string]bool, len(models))
		for _, m := range models {
			if m != nil {
				ids[m.ID] = true
			}
		}
		for _, want := range []string{"muse-spark-1.1", "muse-spark-1.2", "muse-spark-1.2-contributor", "muse-spark-1.3", "muse-spark-1.3-contributor"} {
			if !ids[want] {
				t.Fatalf("channel %q missing model %q (got %v)", channel, want, ids)
			}
		}
	}
	if got := LookupStaticModelInfo("muse-spark-1.3"); got == nil || got.ID != "muse-spark-1.3" {
		t.Fatalf("LookupStaticModelInfo(muse-spark-1.3) = %+v, want muse-spark-1.3", got)
	}
}

func TestModelOverrideHeadersFromEmbeddedModels(t *testing.T) {
	const wantUA = "codex-tui/0.154.0 (Mac OS 26.5.2; arm64) iTerm.app/3.6.11 (codex-tui; 0.154.0)"
	got := ModelOverrideHeaders("gpt-5.6-luna")
	if got == nil {
		t.Fatal("ModelOverrideHeaders(gpt-5.6-luna) = nil, want headers")
	}
	if got["user-agent"] != wantUA {
		t.Fatalf("user-agent = %q, want %q", got["user-agent"], wantUA)
	}
	if got := ModelOverrideHeaders("gpt-5.4"); got != nil {
		t.Fatalf("ModelOverrideHeaders(gpt-5.4) = %#v, want nil", got)
	}
}

func TestGeminiVertexModelsUseFlashLiteReleaseID(t *testing.T) {
	const releaseID = "gemini-3.1-flash-lite"
	const previewID = releaseID + "-preview"

	for _, model := range GetGeminiVertexModels() {
		if model == nil {
			continue
		}
		if model.ID == previewID {
			t.Fatalf("Vertex model ID = %q, want release ID %q", model.ID, releaseID)
		}
		if model.ID == releaseID {
			return
		}
	}

	t.Fatalf("Vertex models do not contain %q", releaseID)
}

func TestWithXAIBuiltinsIncludesImage20(t *testing.T) {
	models := WithXAIBuiltins(nil)
	for _, model := range models {
		if model != nil && model.ID == xaiBuiltinImage20ModelID {
			if model.Created != 1786060800 {
				t.Fatalf("created = %d, want 1786060800 (2026-08-07)", model.Created)
			}
			return
		}
	}
	t.Fatalf("expected xAI builtin model %s", xaiBuiltinImage20ModelID)
}

func TestWithXAIBuiltinsIncludesVideo15GAAndPreviewAlias(t *testing.T) {
	models := WithXAIBuiltins(nil)
	foundGA := false
	foundPreviewAlias := false

	for _, model := range models {
		if model == nil {
			continue
		}
		if model.ID == xaiBuiltinVideo15ModelID {
			foundGA = true
		}
		if model.ID == xaiBuiltinVideo15PreviewID {
			foundPreviewAlias = true
		}
	}

	if !foundGA {
		t.Fatalf("expected xAI builtin model %s", xaiBuiltinVideo15ModelID)
	}
	if !foundPreviewAlias {
		t.Fatalf("expected xAI builtin compatibility alias %s", xaiBuiltinVideo15PreviewID)
	}
}

func TestAntigravityWebSearchModelForRequiresRequestedModelCapability(t *testing.T) {
	registryRef := GetGlobalRegistry()
	registryRef.RegisterClient("test-antigravity-websearch-route", "antigravity", []*ModelInfo{
		{ID: "gemini-route-test"},
		{ID: "gemini-web-search-test", SupportsWebSearch: true},
	})
	registryRef.RegisterClient("test-gemini-websearch-route", "gemini", []*ModelInfo{
		{ID: "gemini-cross-provider-route"},
		{ID: "gemini-cross-provider-search", SupportsWebSearch: true},
	})
	t.Cleanup(func() {
		registryRef.UnregisterClient("test-antigravity-websearch-route")
		registryRef.UnregisterClient("test-gemini-websearch-route")
	})

	if got := AntigravityWebSearchModelFor("gemini-route-test"); got != "" {
		t.Fatalf("route model without web search support should not get fallback model, got %q", got)
	}
	if got := AntigravityWebSearchModelFor("gemini-route-test(high)"); got != "" {
		t.Fatalf("suffix route model without web search support should not get fallback model, got %q", got)
	}
	if got := AntigravityWebSearchModelFor("gemini-web-search-test"); got != "gemini-web-search-test" {
		t.Fatalf("AntigravityWebSearchModelFor capable model = %q, want itself", got)
	}
	if got := AntigravityWebSearchModelFor("gemini-cross-provider-route"); got != "" {
		t.Fatalf("cross-provider model should not get Antigravity web search model, got %q", got)
	}
	if got := AntigravityWebSearchModelFor("unknown-model"); got != "" {
		t.Fatalf("unknown model should not get Antigravity web search model, got %q", got)
	}
}

func TestWithCodexBuiltinsIncludesImage25Models(t *testing.T) {
	models := WithCodexBuiltins(nil)
	expectedModels := map[string]string{
		"gpt-image-2.5-flare":    "GPT Image 2.5 Flare",
		"gpt-image-2.5-sunburst": "GPT Image 2.5 Sunburst",
		"gpt-image-2.5":          "GPT Image 2.5",
	}

	found := make(map[string]*ModelInfo)
	for _, model := range models {
		if model != nil {
			if _, ok := expectedModels[model.ID]; ok {
				found[model.ID] = model
			}
		}
	}

	for id, wantDisplayName := range expectedModels {
		model, ok := found[id]
		if !ok {
			t.Fatalf("expected builtin model %s in WithCodexBuiltins", id)
		}
		if model.DisplayName != wantDisplayName {
			t.Errorf("model %s DisplayName = %q, want %q", id, model.DisplayName, wantDisplayName)
		}
		if model.Object != "model" {
			t.Errorf("model %s Object = %q, want model", id, model.Object)
		}
		if model.OwnedBy != "openai" {
			t.Errorf("model %s OwnedBy = %q, want openai", id, model.OwnedBy)
		}
		if model.Type != "openai" {
			t.Errorf("model %s Type = %q, want openai", id, model.Type)
		}
		if model.Version != id {
			t.Errorf("model %s Version = %q, want %q", id, model.Version, id)
		}
		if model.Created != 1704067200 {
			t.Errorf("model %s Created = %d, want 1704067200", id, model.Created)
		}
	}
}

func TestGetMuseModelsFallsBackWhenCatalogSectionEmpty(t *testing.T) {
	// A remote catalog refresh without a muse section replaces the embedded
	// one wholesale; Muse credentials must stay routable via builtins.
	modelsCatalogStore.mu.Lock()
	previous := modelsCatalogStore.data
	modelsCatalogStore.data = &staticModelsJSON{}
	modelsCatalogStore.mu.Unlock()
	t.Cleanup(func() {
		modelsCatalogStore.mu.Lock()
		modelsCatalogStore.data = previous
		modelsCatalogStore.mu.Unlock()
	})

	models := GetMuseModels()
	if len(models) != len(museBuiltinModelIDs()) {
		t.Fatalf("GetMuseModels() with empty catalog = %d models, want %d builtins", len(models), len(museBuiltinModelIDs()))
	}
	for _, want := range museBuiltinModelIDs() {
		if got := LookupStaticModelInfo(want); got == nil || got.ID != want {
			t.Fatalf("LookupStaticModelInfo(%q) with empty catalog = %+v, want %q", want, got, want)
		}
	}
}

func TestGetMuseModelsMergesPartialCatalogSection(t *testing.T) {
	// A partial remote section must not drop the remaining builtins.
	modelsCatalogStore.mu.Lock()
	previous := modelsCatalogStore.data
	modelsCatalogStore.data = &staticModelsJSON{
		Muse: []*ModelInfo{{ID: "muse-spark-9", DisplayName: "Future Spark", Type: "muse"}},
	}
	modelsCatalogStore.mu.Unlock()
	t.Cleanup(func() {
		modelsCatalogStore.mu.Lock()
		modelsCatalogStore.data = previous
		modelsCatalogStore.mu.Unlock()
	})

	models := GetMuseModels()
	if len(models) != len(museBuiltinModelIDs())+1 {
		t.Fatalf("GetMuseModels() with partial catalog = %d models, want %d", len(models), len(museBuiltinModelIDs())+1)
	}
	ids := make(map[string]bool, len(models))
	for _, m := range models {
		ids[m.ID] = true
	}
	for _, want := range append(append([]string(nil), museBuiltinModelIDs()...), "muse-spark-9") {
		if !ids[want] {
			t.Fatalf("merged models missing %q (got %v)", want, ids)
		}
	}
}

func TestGetOpencodeModelsCoverGatewayLanes(t *testing.T) {
	if got := len(GetOpencodeModels()); got < 37 {
		t.Fatalf("GetOpencodeModels() = %d, want >= 37 live Zen lanes", got)
	}
	for channel, want := range map[string]string{
		"opencode": "glm-5.2", "opencode-go": "glm-5.2",
	} {
		found := false
		for _, m := range GetStaticModelDefinitionsByChannel(channel) {
			if m != nil && m.ID == want {
				found = true
				break
			}
		}
		if !found {
			t.Fatalf("channel %q missing model %q", channel, want)
		}
	}
	if got := LookupStaticModelInfo("glm-5.2"); got == nil {
		t.Fatalf("LookupStaticModelInfo(glm-5.2) = nil")
	}
	// Anthropic-route lanes keep working when the catalog section is wiped.
	if got := OpencodeUpstreamRoute("minimax-m2.5"); got != "anthropic" {
		t.Fatalf("minimax route = %q", got)
	}
}
