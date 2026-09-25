package api

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/router-for-me/CLIProxyAPI/v7/internal/registry"
)

func registerLiteLLMDiscoveryTestModels(t *testing.T) {
	t.Helper()
	modelRegistry := registry.GetGlobalRegistry()
	clientID := "test-litellm-discovery"
	modelRegistry.RegisterClient(clientID, "claude", []*registry.ModelInfo{
		{
			ID: "litellm-claude-model", Type: "claude", OwnedBy: "anthropic",
			ContextLength: 200000, MaxCompletionTokens: 64000,
			Thinking:                  &registry.ThinkingSupport{Min: 1024, Max: 128000, ZeroAllowed: true},
			SupportedInputModalities:  []string{"text", "image"},
			SupportedOutputModalities: []string{"text"},
		},
		{
			ID: "litellm-text-model", Type: "gemini", OwnedBy: "google",
			InputTokenLimit: 1048576, OutputTokenLimit: 65536,
			Thinking:                  &registry.ThinkingSupport{Min: 128, Max: 32768, DynamicAllowed: true},
			SupportedInputModalities:  []string{"text"},
			SupportedOutputModalities: []string{"text"},
		},
		{
			ID: "litellm-override-model", Type: "kimi", OwnedBy: "moonshot",
			ContextLength: 131072, MaxContextLength: 50000, MaxCompletionTokens: 32768,
			SupportedInputModalities: []string{"text"},
		},
		{
			ID: "litellm-bare-model", Type: "openai-compatibility", OwnedBy: "deepseek",
		},
	})
	modelRegistry.RegisterClient(clientID+"-openai", "openai", []*registry.ModelInfo{
		{
			ID: "litellm-gpt-model", Type: "openai", OwnedBy: "openai",
			ContextLength: 272000, MaxCompletionTokens: 128000,
			SupportedParameters:       []string{"tools"},
			Thinking:                  &registry.ThinkingSupport{Levels: []string{"low", "medium", "high"}},
			SupportedInputModalities:  []string{"text", "image"},
			SupportedOutputModalities: []string{"text"},
		},
		{ID: "gpt-image-2", Type: "openai", OwnedBy: "openai"},
		{ID: "grok-imagine-video", Type: "xai", OwnedBy: "xai"},
		{ID: "text-embedding-3-small", Type: "openai", OwnedBy: "openai", ContextLength: 8192},
	})
	t.Cleanup(func() {
		modelRegistry.UnregisterClient(clientID)
		modelRegistry.UnregisterClient(clientID + "-openai")
	})
}

type litellmTestEntry struct {
	ModelName string `json:"model_name"`
	ModelInfo struct {
		ID                      string   `json:"id"`
		Mode                    string   `json:"mode"`
		MaxInputTokens          int      `json:"max_input_tokens"`
		MaxOutputTokens         int      `json:"max_output_tokens"`
		MaxTokens               int      `json:"max_tokens"`
		SupportsVision          *bool    `json:"supports_vision"`
		SupportsReasoning       bool     `json:"supports_reasoning"`
		SupportsFunctionCalling bool     `json:"supports_function_calling"`
		SupportedOpenAIParams   []string `json:"supported_openai_params"`
	} `json:"model_info"`
	LiteLLMParams struct {
		Model             string `json:"model"`
		CustomLLMProvider string `json:"custom_llm_provider"`
	} `json:"litellm_params"`
	Providers []string `json:"providers"`
}

func fetchLiteLLMDiscoveryEntries(t *testing.T, server *Server, path string) map[string]litellmTestEntry {
	t.Helper()
	req := httptest.NewRequest(http.MethodGet, path, nil)
	req.Header.Set("Authorization", "Bearer test-key")
	recorder := httptest.NewRecorder()
	server.engine.ServeHTTP(recorder, req)
	if recorder.Code != http.StatusOK {
		t.Fatalf("%s status = %d, body = %s", path, recorder.Code, recorder.Body.String())
	}
	var response struct {
		Data []litellmTestEntry `json:"data"`
	}
	if err := json.Unmarshal(recorder.Body.Bytes(), &response); err != nil {
		t.Fatalf("%s decode: %v; body=%s", path, err, recorder.Body.String())
	}
	entries := make(map[string]litellmTestEntry, len(response.Data))
	for _, entry := range response.Data {
		entries[entry.ModelName] = entry
	}
	return entries
}

func TestLiteLLMDiscoveryEndpoints(t *testing.T) {
	registerLiteLLMDiscoveryTestModels(t)
	server := newTestServer(t)

	for _, path := range []string{"/model_group/info", "/v2/model/info", "/model/info", "/v1/model/info"} {
		t.Run(path, func(t *testing.T) {
			entries := fetchLiteLLMDiscoveryEntries(t, server, path)

			claude, ok := entries["litellm-claude-model"]
			if !ok {
				t.Fatal("missing litellm-claude-model entry")
			}
			if claude.ModelInfo.Mode != "chat" {
				t.Fatalf("claude mode = %q, want chat", claude.ModelInfo.Mode)
			}
			if claude.ModelInfo.MaxInputTokens != 200000 || claude.ModelInfo.MaxOutputTokens != 64000 || claude.ModelInfo.MaxTokens != 64000 {
				t.Fatalf("claude limits = %+v", claude.ModelInfo)
			}
			if claude.ModelInfo.SupportsVision == nil || !*claude.ModelInfo.SupportsVision {
				t.Fatalf("claude supports_vision = %+v, want true", claude.ModelInfo.SupportsVision)
			}
			if !claude.ModelInfo.SupportsReasoning {
				t.Fatal("claude supports_reasoning = false, want true")
			}
			if !claude.ModelInfo.SupportsFunctionCalling {
				t.Fatal("claude supports_function_calling = false, want true")
			}
			if len(claude.ModelInfo.SupportedOpenAIParams) != 1 || claude.ModelInfo.SupportedOpenAIParams[0] != "reasoning_effort" {
				t.Fatalf("claude supported_openai_params = %v", claude.ModelInfo.SupportedOpenAIParams)
			}
			if len(claude.Providers) != 1 || claude.Providers[0] != "anthropic" || claude.LiteLLMParams.CustomLLMProvider != "anthropic" {
				t.Fatalf("claude providers = %v params = %+v", claude.Providers, claude.LiteLLMParams)
			}

			text, ok := entries["litellm-text-model"]
			if !ok {
				t.Fatal("missing litellm-text-model entry")
			}
			if text.ModelInfo.MaxInputTokens != 1048576 || text.ModelInfo.MaxOutputTokens != 65536 {
				t.Fatalf("gemini limits = %+v", text.ModelInfo)
			}
			if text.ModelInfo.SupportsVision == nil || *text.ModelInfo.SupportsVision {
				t.Fatalf("gemini supports_vision = %+v, want false", text.ModelInfo.SupportsVision)
			}

			override, ok := entries["litellm-override-model"]
			if !ok {
				t.Fatal("missing litellm-override-model entry")
			}
			if override.ModelInfo.MaxInputTokens != 50000 {
				t.Fatalf("override max_input_tokens = %d, want 50000", override.ModelInfo.MaxInputTokens)
			}
			if override.ModelInfo.SupportsReasoning {
				t.Fatal("override supports_reasoning = true, want false")
			}
			if len(override.ModelInfo.SupportedOpenAIParams) != 0 {
				t.Fatalf("override supported_openai_params = %v, want empty", override.ModelInfo.SupportedOpenAIParams)
			}

			bare, ok := entries["litellm-bare-model"]
			if !ok {
				t.Fatal("missing litellm-bare-model entry")
			}
			if bare.ModelInfo.SupportsVision != nil {
				t.Fatalf("bare supports_vision = %+v, want omitted", bare.ModelInfo.SupportsVision)
			}
			if len(bare.Providers) != 1 || bare.Providers[0] != "openai_compatible" {
				t.Fatalf("bare providers = %v", bare.Providers)
			}

			gpt, ok := entries["litellm-gpt-model"]
			if !ok {
				t.Fatal("missing litellm-gpt-model entry")
			}
			if len(gpt.Providers) != 1 || gpt.Providers[0] != "openai" {
				t.Fatalf("gpt providers = %v", gpt.Providers)
			}
			if len(gpt.ModelInfo.SupportedOpenAIParams) != 2 || gpt.ModelInfo.SupportedOpenAIParams[0] != "tools" || gpt.ModelInfo.SupportedOpenAIParams[1] != "reasoning_effort" {
				t.Fatalf("gpt supported_openai_params = %v", gpt.ModelInfo.SupportedOpenAIParams)
			}

			image, ok := entries["gpt-image-2"]
			if !ok {
				t.Fatal("missing gpt-image-2 entry")
			}
			if image.ModelInfo.Mode != "image_generation" || image.ModelInfo.SupportsFunctionCalling {
				t.Fatalf("image entry = %+v", image.ModelInfo)
			}

			video, ok := entries["grok-imagine-video"]
			if !ok {
				t.Fatal("missing grok-imagine-video entry")
			}
			if video.ModelInfo.Mode != "video_generation" {
				t.Fatalf("video mode = %q", video.ModelInfo.Mode)
			}

			embedding, ok := entries["text-embedding-3-small"]
			if !ok {
				t.Fatal("missing text-embedding-3-small entry")
			}
			if embedding.ModelInfo.Mode != "embedding" {
				t.Fatalf("embedding mode = %q", embedding.ModelInfo.Mode)
			}
		})
	}
}

func TestLiteLLMDiscoveryFiltersByModelID(t *testing.T) {
	registerLiteLLMDiscoveryTestModels(t)
	server := newTestServer(t)

	entries := fetchLiteLLMDiscoveryEntries(t, server, "/model/info?litellm_model_id=litellm-gpt-model")
	if len(entries) != 1 {
		t.Fatalf("filtered entries = %d, want 1", len(entries))
	}
	if _, ok := entries["litellm-gpt-model"]; !ok {
		t.Fatalf("filtered entries = %v", entries)
	}
}

func TestLiteLLMDiscoveryRequiresAuth(t *testing.T) {
	registerLiteLLMDiscoveryTestModels(t)
	server := newTestServer(t)

	for _, path := range []string{"/model_group/info", "/v2/model/info", "/model/info", "/v1/model/info"} {
		recorder := httptest.NewRecorder()
		server.engine.ServeHTTP(recorder, httptest.NewRequest(http.MethodGet, path, nil))
		if recorder.Code != http.StatusUnauthorized {
			t.Fatalf("%s status = %d, want %d", path, recorder.Code, http.StatusUnauthorized)
		}
	}
}
