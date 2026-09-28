package registry

import "testing"

func TestModelsDevBaseKeyFoldsEquivalentEndpoints(t *testing.T) {
	tests := []struct {
		name string
		a, b string
	}{
		{"trailing slash", "https://openrouter.ai/api/v1", "https://openrouter.ai/api/v1/"},
		{"v1 suffix vs not", "https://openrouter.ai/api/v1", "https://openrouter.ai/api"},
		{"host case", "https://OpenRouter.AI/api/v1", "https://openrouter.ai/api/v1"},
		{"http vs https", "http://openrouter.ai/api/v1", "https://openrouter.ai/api/v1"},
		{"userinfo", "https://user:pass@openrouter.ai/api/v1", "https://openrouter.ai/api/v1"},
		{"surrounding space", "  https://openrouter.ai/api/v1  ", "https://openrouter.ai/api/v1"},
		{"explicit port", "https://selfhost.test:8443/api/v1", "https://selfhost.test:8443/api/v1"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got, want := modelsDevBaseKey(tc.a), modelsDevBaseKey(tc.b); got != want {
				t.Fatalf("keys differ: %q vs %q", got, want)
			}
			if modelsDevBaseKey(tc.a) == "" {
				t.Fatal("key must not be empty")
			}
		})
	}
}

func TestModelsDevBaseKeyKeepsDistinctPathsApart(t *testing.T) {
	payg := modelsDevBaseKey("https://api.z.ai/api/paas/v4")
	plan := modelsDevBaseKey("https://api.z.ai/api/coding/paas/v4")
	if payg == plan {
		t.Fatalf("distinct paths collapsed to %q", payg)
	}
	// A path segment is case-sensitive; only host case is folded.
	if modelsDevBaseKey("https://h.io/Api/v1") == modelsDevBaseKey("https://h.io/api/v1") {
		t.Fatal("path case must not be folded")
	}
	for _, bad := range []string{"", "   ", "not a url", "://missing-scheme"} {
		if got := modelsDevBaseKey(bad); got != "" {
			t.Fatalf("base key for %q = %q, want empty", bad, got)
		}
	}
}

func TestLoadModelsDevCustomProvidersIndexesByBaseURL(t *testing.T) {
	ResetModelsDevCustomProvidersForTest()
	t.Cleanup(ResetModelsDevCustomProvidersForTest)

	payload := []byte(`{
		"acme": {
			"id": "acme", "name": "Acme AI", "api": "https://api.acme.test/v1",
			"models": {
				"acme-reasoner": {
					"id": "acme-reasoner", "name": "Acme Reasoner", "reasoning": true,
					"reasoning_options": [{"type": "effort", "values": ["low", "high", "max"]}],
					"tool_call": true, "structured_output": true, "temperature": false,
					"modalities": {"input": ["text", "image"], "output": ["text"]},
					"limit": {"context": 262144, "output": 32768}
				},
				"acme-plain": {
					"id": "acme-plain", "name": "Acme Plain", "reasoning": false,
					"tool_call": false, "structured_output": true, "temperature": true,
					"modalities": {"input": ["text"], "output": ["text"]},
					"limit": {"context": 8192, "output": 2048}
				}
			}
		},
		"globex": {
			"id": "globex", "name": "Globex", "api": "https://api.globex.test/v1",
			"models": {"globex-base": {"id": "globex-base", "reasoning": false, "limit": {"context": 4096, "output": 1024}}}
		},
		"sdk-only": {
			"id": "sdk-only", "name": "SDK Only", "env": ["SDK_ONLY_KEY"],
			"models": {"sdk-base": {"id": "sdk-base", "reasoning": false, "limit": {"context": 1, "output": 1}}}
		}
	}`)
	if _, err := loadModelsDevCustomProviders(payload, "test"); err != nil {
		t.Fatalf("loadModelsDevCustomProviders: %v", err)
	}

	entries := GetModelsDevByBaseURL("https://api.acme.test/v1")
	if len(entries) != 2 {
		t.Fatalf("acme entries = %d, want 2", len(entries))
	}
	byID := make(map[string]*ModelInfo, len(entries))
	for _, entry := range entries {
		byID[entry.ID] = entry
	}
	reasoner := byID["acme-reasoner"]
	if reasoner == nil {
		t.Fatal("missing acme-reasoner")
	}
	if reasoner.ContextLength != 262144 || reasoner.MaxCompletionTokens != 32768 {
		t.Fatalf("reasoner limits = %d/%d", reasoner.ContextLength, reasoner.MaxCompletionTokens)
	}
	if got := reasoner.SupportedParameters; len(got) != 2 || got[0] != "tool_choice" || got[1] != "response_format" {
		t.Fatalf("reasoner params = %v", got)
	}
	if got := reasoner.SupportedInputModalities; len(got) != 2 || got[1] != "image" {
		t.Fatalf("reasoner input modalities = %v", got)
	}
	if got := lvl(reasoner); len(got) != 3 || got[0] != "low" || got[2] != "max" {
		t.Fatalf("reasoner levels = %v", got)
	}
	if plain := byID["acme-plain"]; plain == nil || plain.Thinking != nil {
		t.Fatalf("non-reasoning model must carry no thinking support: %+v", plain)
	}

	// The lookup normalizes like the index does.
	if len(GetModelsDevByBaseURL("https://API.Acme.Test/v1/")) != 2 {
		t.Fatal("normalized lookup must resolve the same entry")
	}
	if len(GetModelsDevByBaseURL("https://api.globex.test/v1")) != 1 {
		t.Fatal("second provider not indexed")
	}
	// A provider with no api field cannot be reached by base URL.
	if got := GetModelsDevByBaseURL("https://sdk-only.test/v1"); got != nil {
		t.Fatalf("sdk-only provider must not be indexed, got %d entries", len(got))
	}
	if got := GetModelsDevByBaseURL("https://unknown.test/v1"); got != nil {
		t.Fatalf("unknown endpoint must return nil, got %d entries", len(got))
	}
}

func TestLoadModelsDevCustomProvidersDropsInventedLevels(t *testing.T) {

	ResetModelsDevCustomProvidersForTest()
	t.Cleanup(ResetModelsDevCustomProvidersForTest)

	// reasoning_options with no effort values means a reasoning toggle, not a
	// level ladder. The index must not publish an invented low/medium/high.
	payload := []byte(`{"toggle-co": {
		"id": "toggle-co", "name": "Toggle Co", "api": "https://api.toggle.test/v1",
		"models": {"toggle-model": {
			"id": "toggle-model", "reasoning": true,
			"reasoning_options": [{"type": "toggle"}],
			"limit": {"context": 128000, "output": 16384}
		}}
	}}`)
	if _, err := loadModelsDevCustomProviders(payload, "test"); err != nil {
		t.Fatalf("loadModelsDevCustomProviders: %v", err)
	}
	entries := GetModelsDevByBaseURL("https://api.toggle.test/v1")
	if len(entries) != 1 {
		t.Fatalf("entries = %d, want 1", len(entries))
	}
	if entries[0].Thinking != nil {
		t.Fatalf("toggle-only model must not advertise levels, got %v", entries[0].Thinking.Levels)
	}
	if entries[0].ContextLength != 128000 {
		t.Fatalf("limits must still be carried, got %d", entries[0].ContextLength)
	}
}

func TestLoadModelsDevCustomProvidersDropsConflictingIDs(t *testing.T) {
	ResetModelsDevCustomProvidersForTest()
	t.Cleanup(ResetModelsDevCustomProvidersForTest)

	// Two routes on one base URL claiming the same model ID with different
	// windows. Publishing either would be a guess, so the model is dropped.
	payload := []byte(`{
		"route-a": {"id": "route-a", "name": "A", "api": "https://api.dupe.test/v1",
			"models": {"shared": {"id": "shared", "reasoning": false, "limit": {"context": 1000, "output": 100}}}},
		"route-b": {"id": "route-b", "name": "B", "api": "https://api.dupe.test/v1",
			"models": {"shared": {"id": "shared", "reasoning": false, "limit": {"context": 2000, "output": 200}}}}
	}`)
	if _, err := loadModelsDevCustomProviders(payload, "test"); err != nil {
		t.Fatalf("loadModelsDevCustomProviders: %v", err)
	}
	if got := GetModelsDevByBaseURL("https://api.dupe.test/v1"); len(got) != 0 {
		t.Fatalf("conflicting model must be dropped, got %d entries", len(got))
	}
}

func TestLoadModelsDevCustomProvidersKeepsAgreeingDuplicateRoutes(t *testing.T) {
	ResetModelsDevCustomProvidersForTest()
	t.Cleanup(ResetModelsDevCustomProvidersForTest)

	// Vendors publish a pay-per-token and a coding-plan route on one host, so
	// most duplicated models are the same model listed twice. Those must
	// collapse to one entry, not be discarded as if ambiguous.
	payload := []byte(`{
		"vendor": {"id": "vendor", "name": "Vendor", "api": "https://api.vendor.test/v1",
			"models": {
				"shared": {"id": "shared", "name": "Shared", "reasoning": true,
					"reasoning_options": [{"type": "effort", "values": ["low", "high"]}],
					"tool_call": true, "temperature": true,
					"modalities": {"input": ["text"], "output": ["text"]},
					"limit": {"context": 200000, "output": 32000}},
				"only-payg": {"id": "only-payg", "reasoning": false, "limit": {"context": 8000, "output": 1000}}
			}},
		"vendor-coding-plan": {"id": "vendor-coding-plan", "name": "Vendor Plan", "api": "https://api.vendor.test/v1",
			"models": {
				"shared": {"id": "shared", "name": "Shared (plan copy)", "reasoning": true,
					"reasoning_options": [{"type": "effort", "values": ["low", "high"]}],
					"tool_call": true, "temperature": true,
					"modalities": {"input": ["text"], "output": ["text"]},
					"limit": {"context": 200000, "output": 32000}}
			}}
	}`)
	if _, err := loadModelsDevCustomProviders(payload, "test"); err != nil {
		t.Fatalf("loadModelsDevCustomProviders: %v", err)
	}
	entries := GetModelsDevByBaseURL("https://api.vendor.test/v1")
	if len(entries) != 2 {
		t.Fatalf("entries = %d, want 2 (shared collapsed, only-payg kept)", len(entries))
	}
	byID := make(map[string]*ModelInfo, len(entries))
	for _, entry := range entries {
		if _, dup := byID[entry.ID]; dup {
			t.Fatalf("duplicate entry survived for %s", entry.ID)
		}
		byID[entry.ID] = entry
	}
	shared := byID["shared"]
	if shared == nil {
		t.Fatal("agreeing duplicate must be kept, not dropped")
	}
	if shared.ContextLength != 200000 || shared.MaxCompletionTokens != 32000 {
		t.Fatalf("shared limits = %d/%d", shared.ContextLength, shared.MaxCompletionTokens)
	}
	if shared.Thinking == nil || len(shared.Thinking.Levels) != 2 {
		t.Fatalf("shared ladder = %+v, want the agreed low,high", shared.Thinking)
	}
	if byID["only-payg"] == nil {
		t.Fatal("single-route model must be kept")
	}
}

func TestLoadModelsDevCustomProvidersKeepsOrderInsensitiveAgreement(t *testing.T) {
	ResetModelsDevCustomProvidersForTest()
	t.Cleanup(ResetModelsDevCustomProvidersForTest)

	// The same capabilities listed in a different order are agreement, not
	// disagreement.
	payload := []byte(`{
		"a": {"id": "a", "name": "A", "api": "https://api.order.test/v1",
			"models": {"m": {"id": "m", "reasoning": false, "tool_call": true, "temperature": true,
				"modalities": {"input": ["text", "image"], "output": ["text"]},
				"limit": {"context": 1000, "output": 100}}}},
		"b": {"id": "b", "name": "B", "api": "https://api.order.test/v1",
			"models": {"m": {"id": "m", "reasoning": false, "tool_call": true, "temperature": true,
				"modalities": {"input": ["image", "text"], "output": ["text"]},
				"limit": {"context": 1000, "output": 100}}}}
	}`)
	if _, err := loadModelsDevCustomProviders(payload, "test"); err != nil {
		t.Fatalf("loadModelsDevCustomProviders: %v", err)
	}
	if got := GetModelsDevByBaseURL("https://api.order.test/v1"); len(got) != 1 {
		t.Fatalf("entries = %d, want 1 (order must not read as a conflict)", len(got))
	}
}

func TestLoadModelsDevCustomProvidersKeepsPreviousOnBadPayload(t *testing.T) {
	ResetModelsDevCustomProvidersForTest()
	t.Cleanup(ResetModelsDevCustomProvidersForTest)

	good := []byte(`{"keep": {"id": "keep", "name": "Keep", "api": "https://api.keep.test/v1",
		"models": {"keep-model": {"id": "keep-model", "reasoning": false, "limit": {"context": 1, "output": 1}}}}}`)
	if _, err := loadModelsDevCustomProviders(good, "test"); err != nil {
		t.Fatalf("seed: %v", err)
	}
	if _, err := loadModelsDevCustomProviders([]byte(`{ not json`), "test"); err == nil {
		t.Fatal("expected an error for a malformed payload")
	}
	if len(GetModelsDevByBaseURL("https://api.keep.test/v1")) != 1 {
		t.Fatal("a rejected payload must not wipe the previous index")
	}
}

func TestGetModelsDevByBaseURLClonesEntries(t *testing.T) {
	ResetModelsDevCustomProvidersForTest()
	t.Cleanup(ResetModelsDevCustomProvidersForTest)

	payload := []byte(`{"clone-co": {"id": "clone-co", "name": "Clone Co", "api": "https://api.clone.test/v1",
		"models": {"clone-model": {"id": "clone-model", "reasoning": false, "limit": {"context": 10, "output": 2}}}}}`)
	if _, err := loadModelsDevCustomProviders(payload, "test"); err != nil {
		t.Fatalf("load: %v", err)
	}
	first := GetModelsDevByBaseURL("https://api.clone.test/v1")
	first[0].ContextLength = 999999
	first[0].SupportedParameters = append(first[0].SupportedParameters, "mutated")
	second := GetModelsDevByBaseURL("https://api.clone.test/v1")
	if second[0].ContextLength == 999999 {
		t.Fatal("caller mutated the live index through the returned slice")
	}
	if len(second[0].SupportedParameters) != 0 {
		t.Fatalf("parameter slice aliased the live index: %v", second[0].SupportedParameters)
	}
}

func TestLoadModelsDevCustomProvidersStripsLevelsWhenIDDiffersFromKey(t *testing.T) {
	ResetModelsDevCustomProvidersForTest()
	t.Cleanup(ResetModelsDevCustomProvidersForTest)

	// The map key and the id field disagree. The converter dedups on the id
	// and the strip must still resolve the payload, otherwise a toggle-only
	// model would publish an invented low/medium/high ladder.
	payload := []byte(`{"mismatch-co": {
		"id": "mismatch-co", "name": "Mismatch Co", "api": "https://api.mismatch.test/v1",
		"models": {"legacy-key": {
			"id": "canonical-id", "name": "Canonical", "reasoning": true,
			"reasoning_options": [{"type": "toggle"}],
			"limit": {"context": 64000, "output": 8192}
		}}
	}}`)
	if _, err := loadModelsDevCustomProviders(payload, "test"); err != nil {
		t.Fatalf("loadModelsDevCustomProviders: %v", err)
	}
	entries := GetModelsDevByBaseURL("https://api.mismatch.test/v1")
	if len(entries) != 1 {
		t.Fatalf("entries = %d, want 1", len(entries))
	}
	if entries[0].ID != "canonical-id" {
		t.Fatalf("ID = %q, want canonical-id", entries[0].ID)
	}
	if entries[0].Thinking != nil {
		t.Fatalf("toggle-only model must not advertise levels, got %v", entries[0].Thinking.Levels)
	}
}

func TestLoadModelsDevCustomProvidersReportsChangedBaseURLs(t *testing.T) {
	ResetModelsDevCustomProvidersForTest()
	t.Cleanup(ResetModelsDevCustomProvidersForTest)

	first := []byte(`{
		"acme": {"id": "acme", "name": "Acme", "api": "https://api.acme.test/v1",
			"models": {"acme-model": {"id": "acme-model", "reasoning": false, "limit": {"context": 100, "output": 10}}}},
		"globex": {"id": "globex", "name": "Globex", "api": "https://api.globex.test/v1",
			"models": {"globex-model": {"id": "globex-model", "reasoning": false, "limit": {"context": 200, "output": 20}}}}
	}`)
	changed, err := loadModelsDevCustomProviders(first, "test")
	if err != nil {
		t.Fatalf("seed: %v", err)
	}
	if len(changed) != 2 {
		t.Fatalf("first load changed = %v, want both base URLs", changed)
	}

	// Only one endpoint's limits move.
	second := []byte(`{
		"acme": {"id": "acme", "name": "Acme", "api": "https://api.acme.test/v1",
			"models": {"acme-model": {"id": "acme-model", "reasoning": false, "limit": {"context": 999, "output": 99}}}},
		"globex": {"id": "globex", "name": "Globex", "api": "https://api.globex.test/v1",
			"models": {"globex-model": {"id": "globex-model", "reasoning": false, "limit": {"context": 200, "output": 20}}}}
	}`)
	changed, err = loadModelsDevCustomProviders(second, "test")
	if err != nil {
		t.Fatalf("update: %v", err)
	}
	if len(changed) != 1 || changed[0] != "api.acme.test" {
		t.Fatalf("changed = %v, want only the acme base URL", changed)
	}

	// An unchanged payload reports nothing, so no needless re-registration.
	changed, err = loadModelsDevCustomProviders(second, "test")
	if err != nil {
		t.Fatalf("repeat: %v", err)
	}
	if len(changed) != 0 {
		t.Fatalf("unchanged payload reported %v", changed)
	}

	// A dropped endpoint is reported too, so its provider stops advertising
	// catalog-derived metadata.
	changed, err = loadModelsDevCustomProviders([]byte(`{
		"acme": {"id": "acme", "name": "Acme", "api": "https://api.acme.test/v1",
			"models": {"acme-model": {"id": "acme-model", "reasoning": false, "limit": {"context": 999, "output": 99}}}}
	}`), "test")
	if err != nil {
		t.Fatalf("drop: %v", err)
	}
	if len(changed) != 1 || changed[0] != "api.globex.test" {
		t.Fatalf("changed = %v, want the dropped globex base URL", changed)
	}
}
