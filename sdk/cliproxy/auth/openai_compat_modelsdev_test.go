package auth

import (
	"testing"

	"github.com/router-for-me/CLIProxyAPI/v8/internal/config"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/registry"
	cliproxyexecutor "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/executor"
)

// The request-time capability snapshot must accept exactly the reasoning levels
// the client catalog advertises, otherwise a level published as supported is
// rejected by ValidateConfig when the client actually sends it.
func TestCompatCapabilitiesUseModelsDevLevels(t *testing.T) {
	if err := registry.SeedModelsDevCustomProvidersForTest([]byte(`{
		"acme": {"id": "acme", "name": "Acme AI", "api": "https://api.acme.test/v1",
			"models": {
				"acme-ladder": {
					"id": "acme-ladder", "reasoning": true,
					"reasoning_options": [{"type": "effort", "values": ["low", "xhigh", "max"]}],
					"limit": {"context": 1000, "output": 100}
				},
				"acme-toggle": {
					"id": "acme-toggle", "reasoning": true,
					"reasoning_options": [{"type": "toggle"}],
					"limit": {"context": 2000, "output": 200}
				}
			}}
	}`)); err != nil {
		t.Fatalf("seed catalog: %v", err)
	}
	t.Cleanup(registry.ResetModelsDevCustomProvidersForTest)

	manager := NewManager(nil, nil, nil)
	manager.SetConfig(&config.Config{OpenAICompatibility: []config.OpenAICompatibility{{
		Name:    "acme",
		BaseURL: "https://api.acme.test/v1",
		Models: []config.OpenAICompatibilityModel{
			{Name: "acme-ladder", Alias: "ladder"},
			{Name: "acme-toggle", Alias: "toggle"},
		},
	}}})
	auth := &Auth{
		ID:       "auth-acme",
		Provider: "openai-compatibility:acme",
		Attributes: map[string]string{
			AttributeSource: "config:acme[0]",
			"compat_name":   "acme",
			"provider_key":  "openai-compatibility:acme",
			"base_url":      "https://api.acme.test/v1",
		},
	}
	registerCapabilityTestAuth(t, manager, auth)

	routing := manager.loadAPIKeyModelRouting()
	if routing == nil {
		t.Fatal("no API-key model routing snapshot")
	}

	// A model whose ladder models.dev publishes must use exactly that ladder,
	// not the hardcoded low/medium/high default.
	ladderReq := attachResolvedAPIKeyModelInfo(routing, cliproxyexecutor.Request{}, auth, "ladder", "acme-ladder")
	assertResolvedThinkingLevels(t, ladderReq, "low", "xhigh", "max")

	// A toggle-only model has no verified ladder; it keeps the generic default
	// so reasoning configuration still flows instead of being stripped.
	toggleReq := attachResolvedAPIKeyModelInfo(routing, cliproxyexecutor.Request{}, auth, "toggle", "acme-toggle")
	assertResolvedThinkingLevels(t, toggleReq, "low", "medium", "high")
}

func TestCompatCapabilitiesIgnoreModelsDevForOtherEndpoints(t *testing.T) {
	if err := registry.SeedModelsDevCustomProvidersForTest([]byte(`{
		"acme": {"id": "acme", "name": "Acme AI", "api": "https://api.acme.test/v1",
			"models": {"acme-ladder": {
				"id": "acme-ladder", "reasoning": true,
				"reasoning_options": [{"type": "effort", "values": ["low", "xhigh", "max"]}],
				"limit": {"context": 1000, "output": 100}
			}}}
	}`)); err != nil {
		t.Fatalf("seed catalog: %v", err)
	}
	t.Cleanup(registry.ResetModelsDevCustomProvidersForTest)

	// A different endpoint serving a model with the same name must not inherit
	// the other endpoint's ladder.
	manager := NewManager(nil, nil, nil)
	manager.SetConfig(&config.Config{OpenAICompatibility: []config.OpenAICompatibility{{
		Name:    "private",
		BaseURL: "https://llm.internal.corp/v1",
		Models:  []config.OpenAICompatibilityModel{{Name: "acme-ladder", Alias: "ladder"}},
	}}})
	auth := &Auth{
		ID:       "auth-private",
		Provider: "openai-compatibility:private",
		Attributes: map[string]string{
			AttributeSource: "config:private[0]",
			"compat_name":   "private",
			"provider_key":  "openai-compatibility:private",
			"base_url":      "https://llm.internal.corp/v1",
		},
	}
	registerCapabilityTestAuth(t, manager, auth)

	routing := manager.loadAPIKeyModelRouting()
	req := attachResolvedAPIKeyModelInfo(routing, cliproxyexecutor.Request{}, auth, "ladder", "acme-ladder")
	assertResolvedThinkingLevels(t, req, "low", "medium", "high")
}
