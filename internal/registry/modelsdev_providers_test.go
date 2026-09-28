package registry

import "testing"

func TestGetModelsDevProvidersForModel(t *testing.T) {
	ResetModelsDevCustomProvidersForTest()
	t.Cleanup(ResetModelsDevCustomProvidersForTest)

	payload := []byte(`{
		"alpha": {"id": "alpha", "name": "Alpha AI", "api": "https://api.alpha.test/v1",
			"models": {
				"shared-model": {"id": "shared-model", "reasoning": false, "limit": {"context": 1000, "output": 100}},
				"alpha-only": {"id": "alpha-only", "reasoning": false, "limit": {"context": 2000, "output": 200}}
			}},
		"beta": {"id": "beta", "name": "Beta AI", "api": "https://api.beta.test/v1",
			"models": {"shared-model": {"id": "shared-model", "reasoning": false, "limit": {"context": 4000, "output": 400}}}},
		"gamma": {"id": "gamma", "name": "Gamma AI", "api": "https://api.gamma.test/v1",
			"models": {"shared-model": {"id": "shared-model", "reasoning": false, "limit": {"context": 8000, "output": 800}}}}
	}`)
	if _, err := loadModelsDevCustomProviders(payload, "test"); err != nil {
		t.Fatalf("load: %v", err)
	}

	// A model several providers serve lists them all, sorted by ID.
	shared := GetModelsDevProvidersForModel("shared-model", "")
	if len(shared) != 3 {
		t.Fatalf("shared-model providers = %d, want 3", len(shared))
	}
	got := []string{shared[0].ID, shared[1].ID, shared[2].ID}
	want := []string{"alpha", "beta", "gamma"}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("provider order = %v, want %v", got, want)
		}
	}
	if shared[0].Name != "shared-model" && shared[0].Name == "" {
		t.Fatal("provider name must be populated for display")
	}
	if shared[0].ContextLength != 1000 {
		t.Fatalf("context = %d, want the per-provider value for the model", shared[0].ContextLength)
	}

	// A model one provider serves is unambiguous.
	only := GetModelsDevProvidersForModel("alpha-only", "")
	if len(only) != 1 || only[0].ID != "alpha" {
		t.Fatalf("alpha-only providers = %+v, want just alpha", only)
	}

	// The base URL is a hint that ranks the matching provider first.
	ranked := GetModelsDevProvidersForModel("shared-model", "https://api.beta.test/v1")
	if len(ranked) != 3 {
		t.Fatalf("ranked providers = %d, want 3", len(ranked))
	}
	if ranked[0].ID != "beta" {
		t.Fatalf("base-URL match must rank first, got %q", ranked[0].ID)
	}

	// The hint is matched through the same normalization as the index.
	if first := GetModelsDevProvidersForModel("shared-model", "https://API.Beta.Test/v1/"); first[0].ID != "beta" {
		t.Fatalf("normalized base-URL match must rank first, got %q", first[0].ID)
	}

	if got := GetModelsDevProvidersForModel("no-such-model", ""); got != nil {
		t.Fatalf("unknown model must return nil, got %+v", got)
	}
	if got := GetModelsDevProvidersForModel("", ""); got != nil {
		t.Fatal("empty model must return nil")
	}
}

func TestGetModelsDevProviderModel(t *testing.T) {
	ResetModelsDevCustomProvidersForTest()
	t.Cleanup(ResetModelsDevCustomProvidersForTest)

	payload := []byte(`{
		"alpha": {"id": "alpha", "name": "Alpha AI", "api": "https://api.alpha.test/v1",
			"models": {"m": {"id": "m", "reasoning": true,
				"reasoning_options": [{"type": "effort", "values": ["low", "xhigh"]}],
				"limit": {"context": 111, "output": 11}}}},
		"beta": {"id": "beta", "name": "Beta AI", "api": "https://api.beta.test/v1",
			"models": {"m": {"id": "m", "reasoning": false, "limit": {"context": 222, "output": 22}}}}
	}`)
	if _, err := loadModelsDevCustomProviders(payload, "test"); err != nil {
		t.Fatalf("load: %v", err)
	}

	alpha := GetModelsDevProviderModel("alpha", "m")
	if alpha == nil {
		t.Fatal("pinned provider must resolve its model")
	}
	if alpha.ContextLength != 111 || alpha.MaxCompletionTokens != 11 {
		t.Fatalf("alpha limits = %d/%d", alpha.ContextLength, alpha.MaxCompletionTokens)
	}
	if alpha.Thinking == nil || len(alpha.Thinking.Levels) != 2 || alpha.Thinking.Levels[1] != "xhigh" {
		t.Fatalf("alpha ladder = %+v", alpha.Thinking)
	}

	beta := GetModelsDevProviderModel("beta", "m")
	if beta == nil {
		t.Fatal("second provider must resolve the same model ID")
	}
	if beta.ContextLength != 222 {
		t.Fatalf("beta limits = %d, want 222", beta.ContextLength)
	}
	if beta.Thinking != nil {
		t.Fatalf("non-reasoning provider must not carry a ladder, got %+v", beta.Thinking)
	}

	// A stale pin degrades to "no catalog entry" rather than wrong metadata.
	if got := GetModelsDevProviderModel("alpha", "missing-model"); got != nil {
		t.Fatalf("unknown model must return nil, got %+v", got)
	}
	if got := GetModelsDevProviderModel("no-such-provider", "m"); got != nil {
		t.Fatalf("unknown provider must return nil, got %+v", got)
	}
	if got := GetModelsDevProviderModel("", "m"); got != nil {
		t.Fatal("empty provider must return nil")
	}

	// The returned entry is a clone: mutating it must not corrupt the index.
	alpha.ContextLength = 999999
	again := GetModelsDevProviderModel("alpha", "m")
	if again.ContextLength == 999999 {
		t.Fatal("caller mutated the live index through the returned entry")
	}
}

func TestGetModelsDevProviderModelResolvesAcrossBaseURLs(t *testing.T) {
	ResetModelsDevCustomProvidersForTest()
	t.Cleanup(ResetModelsDevCustomProvidersForTest)

	// A provider reached through two different api URLs still resolves the
	// same model by provider ID, which is what lets a proxy front it.
	payload := []byte(`{
		"dup": {"id": "dup", "name": "Dup", "api": "https://one.test/v1",
			"models": {"shared": {"id": "shared", "reasoning": false, "limit": {"context": 10, "output": 1}}}},
		"dup-plan": {"id": "dup-plan", "name": "Dup Plan", "api": "https://two.test/v1",
			"models": {"shared": {"id": "shared", "reasoning": false, "limit": {"context": 20, "output": 2}}}}
	}`)
	if _, err := loadModelsDevCustomProviders(payload, "test"); err != nil {
		t.Fatalf("load: %v", err)
	}
	first := GetModelsDevProviderModel("dup", "shared")
	second := GetModelsDevProviderModel("dup-plan", "shared")
	if first == nil || second == nil {
		t.Fatal("both providers must resolve")
	}
	if first.ContextLength != 10 || second.ContextLength != 20 {
		t.Fatalf("provider pinning must disambiguate: %d vs %d", first.ContextLength, second.ContextLength)
	}
}
