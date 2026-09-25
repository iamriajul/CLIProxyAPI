package api

import (
	"strings"

	"github.com/gin-gonic/gin"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/registry"
)

// litellmDiscoverySourceFormat identifies LiteLLM-compatible discovery responses
// in logs and plugin interceptors.
const litellmDiscoverySourceFormat = "litellm"

// litellmImageModelIDs lists generation-only image model base IDs (without
// provider prefix). They share the IDs used by the image handlers.
var litellmImageModelIDs = map[string]struct{}{
	"gpt-image-1.5":              {},
	"gpt-image-2":                {},
	"gpt-image-2.5-flare":        {},
	"gpt-image-2.5-sunburst":     {},
	"gpt-image-2.5":              {},
	"grok-imagine-image":         {},
	"grok-imagine-image-quality": {},
	"grok-imagine-image-2.0":     {},
}

// litellmVideoModelIDs lists generation-only video model base IDs (without
// provider prefix).
var litellmVideoModelIDs = map[string]struct{}{
	"grok-imagine-video":             {},
	"grok-imagine-video-1.5":         {},
	"grok-imagine-video-1.5-preview": {},
}

// handleLiteLLMModelInfo serves LiteLLM-compatible rich model discovery.
// It backs GET /model/info, GET /v1/model/info and GET /v2/model/info.
// An optional litellm_model_id query parameter narrows the result to one model.
func (s *Server) handleLiteLLMModelInfo(c *gin.Context) {
	entries := litellmDiscoveryEntries(registry.GetGlobalRegistry().GetAvailableModelInfos())
	entries = filterLiteLLMDiscoveryEntries(entries, c.Query("litellm_model_id"))
	s.writeModelListResponse(c, litellmDiscoverySourceFormat, gin.H{"data": entries})
}

// handleLiteLLMModelGroupInfo serves GET /model_group/info with the same rich
// entries as the model info endpoints. Rich clients probe this route first.
func (s *Server) handleLiteLLMModelGroupInfo(c *gin.Context) {
	entries := litellmDiscoveryEntries(registry.GetGlobalRegistry().GetAvailableModelInfos())
	entries = filterLiteLLMDiscoveryEntries(entries, c.Query("litellm_model_id"))
	s.writeModelListResponse(c, litellmDiscoverySourceFormat, gin.H{"data": entries})
}

// litellmDiscoveryEntries converts available registry models into LiteLLM rich
// discovery entries. The shape mirrors LiteLLM's /model/info and
// /model_group/info responses: rich clients (Oh My Pi, OpenCode) probe
// /model_group/info, /v2/model/info, /model/info and /v1/model/info in order
// and read model_group/model_name, litellm_params and model_info.
func litellmDiscoveryEntries(infos []*registry.ModelInfo) []map[string]any {
	entries := make([]map[string]any, 0, len(infos))
	for _, info := range infos {
		if info == nil {
			continue
		}
		id := strings.TrimSpace(info.ID)
		if id == "" {
			continue
		}
		provider := litellmProviderForModel(info)
		mode := litellmModeForModel(info)
		modelInfo := map[string]any{
			"id":   id,
			"mode": mode,
		}
		if maxInput := litellmMaxInputTokens(info); maxInput > 0 {
			modelInfo["max_input_tokens"] = maxInput
		}
		if maxOutput := litellmMaxOutputTokens(info); maxOutput > 0 {
			modelInfo["max_output_tokens"] = maxOutput
			modelInfo["max_tokens"] = maxOutput
		}
		if vision, ok := litellmVisionSupport(info); ok {
			modelInfo["supports_vision"] = vision
		}
		modelInfo["supports_reasoning"] = info.Thinking != nil
		modelInfo["supports_function_calling"] = mode == "chat"
		if params := litellmSupportedOpenAIParams(info, mode); len(params) > 0 {
			modelInfo["supported_openai_params"] = params
		}
		entry := map[string]any{
			"model_name":  id,
			"model_group": id,
			"providers":   []string{provider},
			"litellm_params": map[string]any{
				"model":               id,
				"custom_llm_provider": provider,
			},
			"model_info": modelInfo,
		}
		// Flat mirrors for group-oriented clients that read top-level fields.
		for key, value := range modelInfo {
			if key == "id" {
				continue
			}
			entry[key] = value
		}
		entries = append(entries, entry)
	}
	return entries
}

// filterLiteLLMDiscoveryEntries narrows discovery entries to one deployment,
// matching LiteLLM's litellm_model_id query filter.
func filterLiteLLMDiscoveryEntries(entries []map[string]any, modelID string) []map[string]any {
	modelID = strings.TrimSpace(modelID)
	if modelID == "" {
		return entries
	}
	filtered := make([]map[string]any, 0, len(entries))
	for _, entry := range entries {
		if entry == nil {
			continue
		}
		if entry["model_name"] == modelID || entry["model_group"] == modelID {
			filtered = append(filtered, entry)
			continue
		}
		if modelInfo, ok := entry["model_info"].(map[string]any); ok && modelInfo["id"] == modelID {
			filtered = append(filtered, entry)
		}
	}
	return filtered
}

// litellmMaxInputTokens resolves the advertised context window. An explicit
// max-context-length configuration override wins over catalog metadata.
func litellmMaxInputTokens(info *registry.ModelInfo) int {
	if info.MaxContextLength > 0 {
		return info.MaxContextLength
	}
	if info.ContextLength > 0 {
		return info.ContextLength
	}
	return info.InputTokenLimit
}

// litellmMaxOutputTokens resolves the advertised output limit.
func litellmMaxOutputTokens(info *registry.ModelInfo) int {
	if info.MaxCompletionTokens > 0 {
		return info.MaxCompletionTokens
	}
	return info.OutputTokenLimit
}

// litellmVisionSupport reports image-input support. It returns ok=false when
// the model declares no input modalities so clients fall back to their own
// reference data instead of trusting an asserted text-only claim.
func litellmVisionSupport(info *registry.ModelInfo) (vision, ok bool) {
	if len(info.SupportedInputModalities) == 0 {
		return false, false
	}
	for _, modality := range info.SupportedInputModalities {
		if strings.EqualFold(strings.TrimSpace(modality), "image") {
			return true, true
		}
	}
	return false, true
}

// litellmSupportedOpenAIParams passes through catalog-declared OpenAI
// parameters and advertises reasoning_effort for thinking-capable chat models:
// CPA accepts reasoning_effort on the OpenAI ingress and translates it to the
// provider-native thinking configuration.
func litellmSupportedOpenAIParams(info *registry.ModelInfo, mode string) []string {
	params := append([]string(nil), info.SupportedParameters...)
	if mode != "chat" || info.Thinking == nil {
		return params
	}
	for _, param := range params {
		if param == "reasoning_effort" {
			return params
		}
	}
	return append(params, "reasoning_effort")
}

// litellmModeForModel maps a registry model to a LiteLLM mode. Chat is the
// default; generation-only and embedding models report their task mode so rich
// clients exclude them from the chat picker instead of offering a model the
// chat endpoint cannot serve.
func litellmModeForModel(info *registry.ModelInfo) string {
	if info.Type == registry.OpenAIImageModelType {
		return "image_generation"
	}
	base := strings.ToLower(strings.TrimSpace(info.ID))
	if idx := strings.LastIndex(base, "/"); idx >= 0 {
		base = base[idx+1:]
	}
	if _, ok := litellmImageModelIDs[base]; ok {
		return "image_generation"
	}
	if _, ok := litellmVideoModelIDs[base]; ok {
		return "video_generation"
	}
	if strings.Contains(base, "embed") {
		return "embedding"
	}
	return "chat"
}

// litellmProviderForModel maps a registry model type to a LiteLLM provider
// name. Only native OpenAI lanes report "openai" so rich clients route them to
// the Responses API; every translated lane reports its own provider and stays
// on chat completions.
func litellmProviderForModel(info *registry.ModelInfo) string {
	switch strings.ToLower(strings.TrimSpace(info.Type)) {
	case "openai", "openai-image":
		return "openai"
	case "claude":
		return "anthropic"
	case "gemini":
		return "gemini"
	case "xai":
		return "xai"
	case "kimi":
		return "moonshot"
	case "openai-compatibility", "":
		return "openai_compatible"
	default:
		return strings.ToLower(strings.TrimSpace(info.Type))
	}
}
