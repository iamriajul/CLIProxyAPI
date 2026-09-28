package cliproxy

import (
	"testing"

	"github.com/router-for-me/CLIProxyAPI/v8/internal/config"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/registry"
)

// seedModelsDevCustomProviderCatalog installs a synthetic models.dev catalog
// for a custom provider endpoint. Tests only.
func seedModelsDevCustomProviderCatalog(t *testing.T, payload string) {
	t.Helper()
	if err := registry.SeedModelsDevCustomProvidersForTest([]byte(payload)); err != nil {
		t.Fatalf("seed catalog: %v", err)
	}
	t.Cleanup(registry.ResetModelsDevCustomProvidersForTest)
}

func findModelByID(models []*ModelInfo, id string) *ModelInfo {
	for _, model := range models {
		if model != nil && model.ID == id {
			return model
		}
	}
	return nil
}

func TestBuildOpenAICompatibilityConfigModels_UsesCatalogLimitsAndLevels(t *testing.T) {
	seedModelsDevCustomProviderCatalog(t, `{
		"acme": {"id": "acme", "name": "Acme AI", "api": "https://api.acme.test/v1",
			"models": {
				"acme-reasoner": {
					"id": "acme-reasoner", "name": "Acme Reasoner", "reasoning": true,
					"reasoning_options": [{"type": "effort", "values": ["low", "xhigh", "max"]}],
					"tool_call": true, "structured_output": true, "temperature": false,
					"modalities": {"input": ["text", "image"], "output": ["text"]},
					"limit": {"context": 262144, "output": 32768}
				},
				"acme-toggle": {
					"id": "acme-toggle", "name": "Acme Toggle", "reasoning": true,
					"reasoning_options": [{"type": "toggle"}],
					"tool_call": true, "temperature": true,
					"limit": {"context": 128000, "output": 16384}
				}
			}}
	}`)

	compat := &config.OpenAICompatibility{
		Name:    "acme",
		BaseURL: "https://api.acme.test/v1",
		Models: []config.OpenAICompatibilityModel{
			// Alias differs from the upstream name; the catalog is keyed by the
			// upstream name.
			{Name: "acme-reasoner", Alias: "acme-fast"},
			{Name: "acme-toggle", Alias: "acme-toggle-alias"},
		},
	}
	models := buildOpenAICompatibilityConfigModels(compat)

	reasoner := findModelByID(models, "acme-fast")
	if reasoner == nil {
		t.Fatal("missing acme-fast")
	}
	if reasoner.ContextLength != 262144 {
		t.Fatalf("ContextLength = %d, want 262144 from the catalog", reasoner.ContextLength)
	}
	if reasoner.MaxCompletionTokens != 32768 {
		t.Fatalf("MaxCompletionTokens = %d, want 32768 from the catalog", reasoner.MaxCompletionTokens)
	}
	if got := reasoner.SupportedParameters; len(got) != 2 || got[0] != "tool_choice" || got[1] != "response_format" {
		t.Fatalf("SupportedParameters = %v, want tool_choice,response_format", got)
	}
	if got := joinModalities(reasoner.SupportedInputModalities); got != "text,image" {
		t.Fatalf("SupportedInputModalities = %q, want text,image from the catalog", got)
	}
	if got := joinModalities(reasoner.SupportedOutputModalities); got != "text" {
		t.Fatalf("SupportedOutputModalities = %q, want text from the catalog", got)
	}
	if !reasoner.ExplicitInputModalities {
		t.Fatal("catalog modalities must be published to clients (ExplicitInputModalities)")
	}
	if reasoner.Thinking == nil {
		t.Fatal("Thinking must be carried from the catalog")
	}
	if got := reasoner.Thinking.Levels; len(got) != 3 || got[0] != "low" || got[1] != "xhigh" || got[2] != "max" {
		t.Fatalf("levels = %v, want the catalog ladder low,xhigh,max", got)
	}
	if !reasoner.ExplicitThinking {
		t.Fatal("a catalog-backed ladder must be published to clients (ExplicitThinking)")
	}

	toggle := findModelByID(models, "acme-toggle-alias")
	if toggle == nil {
		t.Fatal("missing acme-toggle-alias")
	}
	if toggle.ContextLength != 128000 {
		t.Fatalf("toggle model ContextLength = %d, want 128000", toggle.ContextLength)
	}
	if toggle.ExplicitThinking {
		t.Fatal("a toggle-only reasoning model has no ladder; clients must not be shown one")
	}
	if toggle.Thinking == nil {
		t.Fatal("thinking support must remain non-nil so the model is still recognized as reasoning-capable")
	}
}

func TestBuildOpenAICompatibilityConfigModels_ConfigOverridesCatalog(t *testing.T) {
	seedModelsDevCustomProviderCatalog(t, `{
		"acme": {"id": "acme", "name": "Acme AI", "api": "https://api.acme.test/v1",
			"models": {"acme-model": {
				"id": "acme-model", "reasoning": true,
				"reasoning_options": [{"type": "effort", "values": ["low", "high"]}],
				"tool_call": true, "temperature": true,
				"limit": {"context": 262144, "output": 32768}
			}}}
	}`)

	compat := &config.OpenAICompatibility{
		Name:    "acme",
		BaseURL: "https://api.acme.test/v1",
		Models: []config.OpenAICompatibilityModel{{
			Name:             "acme-model",
			Alias:            "acme-model",
			MaxContextLength: 1048576,
			Thinking:         &registry.ThinkingSupport{Levels: []string{"minimal", "high"}},
			InputModalities:  []string{"text"},
		}},
	}
	models := buildOpenAICompatibilityConfigModels(compat)
	if len(models) != 1 {
		t.Fatalf("model count = %d, want 1", len(models))
	}
	model := models[0]
	if model.MaxContextLength != 1048576 || model.ContextLength != 1048576 {
		t.Fatalf("configured max-context-length must win: ContextLength=%d MaxContextLength=%d",
			model.ContextLength, model.MaxContextLength)
	}
	if model.Thinking == nil {
		t.Fatal("Thinking must come from the configuration")
	}
	if got := model.Thinking.Levels; len(got) != 2 || got[0] != "minimal" || got[1] != "high" {
		t.Fatalf("levels = %v, want the configured minimal,high", got)
	}
	if !model.ExplicitThinking || !model.ExplicitInputModalities {
		t.Fatal("explicit configuration must be marked explicit")
	}
	if got := joinModalities(model.SupportedInputModalities); got != "text" {
		t.Fatalf("catalog modalities must not override configured ones: %q", got)
	}
}

func TestBuildOpenAICompatibilityConfigModels_UnknownEndpointStaysUnopinionated(t *testing.T) {
	seedModelsDevCustomProviderCatalog(t, `{
		"acme": {"id": "acme", "name": "Acme AI", "api": "https://api.acme.test/v1",
			"models": {"acme-model": {"id": "acme-model", "reasoning": true,
				"reasoning_options": [{"type": "effort", "values": ["low", "high"]}],
				"limit": {"context": 1000, "output": 100}}}}
	}`)

	// A model the endpoint serves but the catalog does not list: no metadata
	// may be borrowed from a sibling model.
	compat := &config.OpenAICompatibility{
		Name:    "acme",
		BaseURL: "https://api.acme.test/v1",
		Models:  []config.OpenAICompatibilityModel{{Name: "acme-private-preview", Alias: "preview"}},
	}
	models := buildOpenAICompatibilityConfigModels(compat)
	if len(models) != 1 {
		t.Fatalf("model count = %d, want 1", len(models))
	}
	if model := models[0]; model.ContextLength != 0 || len(model.SupportedParameters) != 0 || model.ExplicitThinking {
		t.Fatalf("unlisted model must not inherit sibling metadata: %+v", model)
	}
}

func TestBuildOpenAICompatibilityConfigModels_UnknownEndpointPublishesNoLevels(t *testing.T) {
	seedModelsDevCustomProviderCatalog(t, `{
		"acme": {"id": "acme", "name": "Acme AI", "api": "https://api.acme.test/v1",
			"models": {"acme-model": {"id": "acme-model", "reasoning": false, "limit": {"context": 1, "output": 1}}}}
	}`)

	// A private deployment unknown to models.dev must keep the previous
	// behavior: no fabricated limits, and — critically — no reasoning ladder
	// advertised to clients.
	compat := &config.OpenAICompatibility{
		Name:    "private",
		BaseURL: "https://llm.internal.corp/v1",
		Models:  []config.OpenAICompatibilityModel{{Name: "house-model", Alias: "house"}},
	}
	models := buildOpenAICompatibilityConfigModels(compat)
	if len(models) != 1 {
		t.Fatalf("model count = %d, want 1", len(models))
	}
	model := models[0]
	if model.ContextLength != 0 || model.MaxCompletionTokens != 0 || len(model.SupportedParameters) != 0 {
		t.Fatalf("unknown endpoint must not gain catalog limits: %+v", model)
	}
	if model.ExplicitThinking {
		t.Fatal("unknown endpoint must not advertise a reasoning ladder")
	}
	// The generic ladder is not a verified answer, so it must not be
	// published. Thinking stays non-nil so the thinking pipeline keeps
	// forwarding reasoning configuration for this model.
	if model.Thinking == nil {
		t.Fatal("Thinking must stay non-nil so the pipeline keeps handling reasoning config")
	}
	if len(model.Thinking.Levels) != 0 {
		t.Fatalf("unknown endpoint must publish no levels, got %v", model.Thinking.Levels)
	}
}

func TestBuildOpenAICompatibilityConfigModels_ImageModelKeepsConfiguredFields(t *testing.T) {
	seedModelsDevCustomProviderCatalog(t, `{
		"acme": {"id": "acme", "name": "Acme AI", "api": "https://api.acme.test/v1",
			"models": {"acme-image": {"id": "acme-image", "reasoning": false,
				"limit": {"context": 4096, "output": 1024}}}}
	}`)

	// An image model must still carry whatever the operator configured: the
	// models.dev work only suppresses the generic reasoning-ladder default.
	compat := &config.OpenAICompatibility{
		Name:    "acme",
		BaseURL: "https://api.acme.test/v1",
		Models: []config.OpenAICompatibilityModel{{
			Name:             "acme-image",
			Alias:            "img",
			Image:            true,
			Thinking:         &registry.ThinkingSupport{Levels: []string{"high"}},
			InputModalities:  []string{"text"},
			OutputModalities: []string{"image"},
		}},
	}
	models := buildOpenAICompatibilityConfigModels(compat)
	if len(models) != 1 {
		t.Fatalf("model count = %d, want 1", len(models))
	}
	model := models[0]
	if model.Type != registry.OpenAIImageModelType {
		t.Fatalf("Type = %q, want %q", model.Type, registry.OpenAIImageModelType)
	}
	if model.Thinking == nil || len(model.Thinking.Levels) != 1 || model.Thinking.Levels[0] != "high" {
		t.Fatalf("configured thinking must survive for an image model, got %+v", model.Thinking)
	}
	if !model.ExplicitThinking {
		t.Fatal("configured thinking must stay explicit")
	}
	if got := joinModalities(model.SupportedOutputModalities); got != "image" {
		t.Fatalf("output modalities = %q, want image", got)
	}
}

func TestBuildOpenAICompatibilityConfigModels_KeepsPipelineThinkingCapability(t *testing.T) {
	seedModelsDevCustomProviderCatalog(t, `{
		"acme": {"id": "acme", "name": "Acme AI", "api": "https://api.acme.test/v1",
			"models": {"acme-toggle": {"id": "acme-toggle", "reasoning": true,
				"reasoning_options": [{"type": "toggle"}],
				"limit": {"context": 128000, "output": 16384}}}}
	}`)

	// A toggle-only model publishes no levels, but the thinking pipeline keys
	// request handling off a non-nil Thinking: a nil value would make the
	// pipeline strip reasoning configuration before it reaches the upstream.
	compat := &config.OpenAICompatibility{
		Name:    "acme",
		BaseURL: "https://api.acme.test/v1",
		Models:  []config.OpenAICompatibilityModel{{Name: "acme-toggle", Alias: "toggle"}},
	}
	models := buildOpenAICompatibilityConfigModels(compat)
	if len(models) != 1 {
		t.Fatalf("model count = %d, want 1", len(models))
	}
	model := models[0]
	if model.Thinking == nil {
		t.Fatal("Thinking must stay non-nil so the thinking pipeline keeps handling reasoning config")
	}
	if model.ExplicitThinking {
		t.Fatal("a toggle-only model must not publish a ladder to clients")
	}
}

func TestBuildOpenAICompatibilityConfigModels_PinnedProviderSelectsCatalogEntry(t *testing.T) {
	// The same model ID published by two providers with different limits. The
	// base URL points at a third, unrelated endpoint, so only the pin can
	// resolve the intended entry.
	seedModelsDevCustomProviderCatalog(t, `{
		"alpha": {"id": "alpha", "name": "Alpha AI", "api": "https://api.alpha.test/v1",
			"models": {"shared": {"id": "shared", "reasoning": true,
				"reasoning_options": [{"type": "effort", "values": ["low", "xhigh"]}],
				"tool_call": true, "temperature": true,
				"limit": {"context": 111000, "output": 11000}}}},
		"beta": {"id": "beta", "name": "Beta AI", "api": "https://api.beta.test/v1",
			"models": {"shared": {"id": "shared", "reasoning": true,
				"reasoning_options": [{"type": "effort", "values": ["medium"]}],
				"structured_output": true, "temperature": false,
				"limit": {"context": 222000, "output": 22000}}}},
		"proxy-target": {"id": "proxy-target", "name": "Proxy", "api": "https://api.gamma.test/v1",
			"models": {"other": {"id": "other", "reasoning": false, "limit": {"context": 1, "output": 1}}}}
	}`)

	newCompat := func(pin string) *config.OpenAICompatibility {
		return &config.OpenAICompatibility{
			Name:    "my-proxy",
			BaseURL: "https://proxy.internal.corp/v1",
			Models: []config.OpenAICompatibilityModel{
				{Name: "shared", Alias: "shared-alias", ModelsDevProvider: pin},
			},
		}
	}

	alpha := findModelByID(buildOpenAICompatibilityConfigModels(newCompat("alpha")), "shared-alias")
	if alpha == nil {
		t.Fatal("missing shared-alias")
	}
	if alpha.ContextLength != 111000 || alpha.MaxCompletionTokens != 11000 {
		t.Fatalf("alpha limits = %d/%d, want 111000/11000", alpha.ContextLength, alpha.MaxCompletionTokens)
	}
	if got := alpha.Thinking.Levels; len(got) != 2 || got[1] != "xhigh" {
		t.Fatalf("alpha levels = %v, want the pinned low,xhigh", got)
	}
	if got := alpha.SupportedParameters; len(got) != 2 || got[0] != "tool_choice" || got[1] != "temperature" {
		t.Fatalf("alpha params = %v, want tool_choice,temperature", got)
	}

	beta := findModelByID(buildOpenAICompatibilityConfigModels(newCompat("beta")), "shared-alias")
	if beta == nil {
		t.Fatal("missing shared-alias")
	}
	if beta.ContextLength != 222000 || beta.MaxCompletionTokens != 22000 {
		t.Fatalf("beta limits = %d/%d, want 222000/22000", beta.ContextLength, beta.MaxCompletionTokens)
	}
	if got := beta.Thinking.Levels; len(got) != 1 || got[0] != "medium" {
		t.Fatalf("beta levels = %v, want the pinned medium", got)
	}
	if got := beta.SupportedParameters; len(got) != 1 || got[0] != "response_format" {
		t.Fatalf("beta params = %v, want response_format only", got)
	}

	// An unpinned model behind a proxy has no resolvable entry, so it stays
	// unopinionated rather than borrowing another provider's limits.
	unpinned := findModelByID(buildOpenAICompatibilityConfigModels(newCompat("")), "shared-alias")
	if unpinned == nil {
		t.Fatal("missing shared-alias")
	}
	if unpinned.ContextLength != 0 || len(unpinned.SupportedParameters) != 0 {
		t.Fatalf("unpinned model behind a proxy must gain no metadata: %+v", unpinned)
	}
	if unpinned.ExplicitThinking {
		t.Fatal("unpinned model behind a proxy must publish no ladder")
	}

	// A stale pin degrades to no metadata instead of wrong metadata.
	stale := findModelByID(buildOpenAICompatibilityConfigModels(newCompat("no-such-provider")), "shared-alias")
	if stale == nil {
		t.Fatal("missing shared-alias")
	}
	if stale.ContextLength != 0 || len(stale.SupportedParameters) != 0 || stale.ExplicitThinking {
		t.Fatalf("stale pin must degrade to no metadata: %+v", stale)
	}
}
