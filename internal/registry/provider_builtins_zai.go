// Code generated from models.dev (zai-coding-plan provider, fetched 2026-09-22T12:57:56Z).
// DO NOT EDIT BY HAND — regenerate with: go run ./cmd/fetch_modelsdev_models
// models.json sections so gateway credentials stay routable when the remote
// catalog carries no matching section (same pattern as the Codex/XAI builtins).
package registry

import "strings"

var zaiOpenAIRouteModels = map[string]bool{
	"glm-5.3-flash": true,
}

// ZaiUsesOpenAIRoute reports whether a Z.AI model rides the OpenAI-completions
// endpoint instead of the default Anthropic endpoint.
func ZaiUsesOpenAIRoute(model string) bool {
	key := strings.ToLower(strings.TrimSpace(model))
	if openParen := strings.LastIndex(key, "("); openParen >= 0 && strings.HasSuffix(key, ")") {
		key = strings.TrimSpace(key[:openParen])
	}
	return zaiOpenAIRouteModels[key]
}

// ZaiBuiltinModelInfos placeholder
func zaiBuiltinModelInfos() []*ModelInfo {
	// modelsdev:generated:begin
	models := make([]*ModelInfo, 0, 7)
	models = append(models, &ModelInfo{
		ID:                        "glm-4.7",
		Object:                    "model",
		Created:                   1766361600,
		OwnedBy:                   "zai",
		Type:                      "zai",
		DisplayName:               "GLM-4.7",
		Description:               "GLM-4.7 via Z.AI.",
		ContextLength:             204800,
		MaxCompletionTokens:       131072,
		InputTokenLimit:           204800,
		OutputTokenLimit:          131072,
		Thinking:                  &ThinkingSupport{Levels: []string{"low", "medium", "high"}},
		SupportedInputModalities:  []string{"text"},
		SupportedOutputModalities: []string{"text"},
		SupportedParameters:       []string{"tool_choice", "temperature"},
	})
	models = append(models, &ModelInfo{
		ID:                        "glm-5-turbo",
		Object:                    "model",
		Created:                   1773619200,
		OwnedBy:                   "zai",
		Type:                      "zai",
		DisplayName:               "GLM-5-Turbo",
		Description:               "GLM-5-Turbo via Z.AI.",
		ContextLength:             200000,
		MaxCompletionTokens:       131072,
		InputTokenLimit:           200000,
		OutputTokenLimit:          131072,
		Thinking:                  &ThinkingSupport{Levels: []string{"low", "medium", "high"}},
		SupportedInputModalities:  []string{"text"},
		SupportedOutputModalities: []string{"text"},
		SupportedParameters:       []string{"tool_choice", "response_format", "temperature"},
	})
	models = append(models, &ModelInfo{
		ID:                        "glm-5.2",
		Object:                    "model",
		Created:                   1781308800,
		OwnedBy:                   "zai",
		Type:                      "zai",
		DisplayName:               "GLM-5.2",
		Description:               "GLM-5.2 via Z.AI.",
		ContextLength:             1000000,
		MaxCompletionTokens:       131072,
		InputTokenLimit:           1000000,
		OutputTokenLimit:          131072,
		Thinking:                  &ThinkingSupport{Levels: []string{"high", "max"}},
		SupportedInputModalities:  []string{"text"},
		SupportedOutputModalities: []string{"text"},
		SupportedParameters:       []string{"tool_choice", "response_format", "temperature"},
	})
	models = append(models, &ModelInfo{
		ID:                        "glm-5.2-highspeed",
		Object:                    "model",
		Created:                   1781308800,
		OwnedBy:                   "zai",
		Type:                      "zai",
		DisplayName:               "GLM-5.2 Highspeed",
		Description:               "GLM-5.2 Highspeed via Z.AI.",
		ContextLength:             1000000,
		MaxCompletionTokens:       131072,
		InputTokenLimit:           1000000,
		OutputTokenLimit:          131072,
		Thinking:                  &ThinkingSupport{Levels: []string{"high", "max"}},
		SupportedInputModalities:  []string{"text"},
		SupportedOutputModalities: []string{"text"},
		SupportedParameters:       []string{"tool_choice", "response_format", "temperature"},
	})
	models = append(models, &ModelInfo{
		ID:                        "glm-5.3",
		Object:                    "model",
		Created:                   1786665600,
		OwnedBy:                   "zai",
		Type:                      "zai",
		DisplayName:               "GLM-5.3",
		Description:               "GLM-5.3 via Z.AI.",
		ContextLength:             1000000,
		MaxCompletionTokens:       131072,
		InputTokenLimit:           1000000,
		OutputTokenLimit:          131072,
		Thinking:                  &ThinkingSupport{Levels: []string{"low", "high", "max"}},
		SupportedInputModalities:  []string{"text"},
		SupportedOutputModalities: []string{"text"},
		SupportedParameters:       []string{"tool_choice", "response_format", "temperature"},
	})
	models = append(models, &ModelInfo{
		ID:                        "glm-5.3-flash",
		Object:                    "model",
		Created:                   1787702400,
		OwnedBy:                   "zai",
		Type:                      "zai",
		DisplayName:               "GLM-5.3-Flash",
		Description:               "GLM-5.3-Flash via Z.AI.",
		ContextLength:             1000000,
		MaxCompletionTokens:       131072,
		InputTokenLimit:           1000000,
		OutputTokenLimit:          131072,
		Thinking:                  &ThinkingSupport{Levels: []string{"low", "high", "max"}},
		SupportedInputModalities:  []string{"text", "image", "video", "pdf"},
		SupportedOutputModalities: []string{"text"},
		SupportedParameters:       []string{"tool_choice", "response_format", "temperature"},
	})
	models = append(models, &ModelInfo{
		ID:                        "glm-5.3-highspeed",
		Object:                    "model",
		Created:                   1786665600,
		OwnedBy:                   "zai",
		Type:                      "zai",
		DisplayName:               "GLM-5.3 Highspeed",
		Description:               "GLM-5.3 Highspeed via Z.AI.",
		ContextLength:             1000000,
		MaxCompletionTokens:       131072,
		InputTokenLimit:           1000000,
		OutputTokenLimit:          131072,
		Thinking:                  &ThinkingSupport{Levels: []string{"low", "high", "max"}},
		SupportedInputModalities:  []string{"text"},
		SupportedOutputModalities: []string{"text"},
		SupportedParameters:       []string{"tool_choice", "response_format", "temperature"},
	})
	// modelsdev:generated:end
	return models
}

// WithZaiBuiltins upserts the hard-coded GLM snapshot over catalog entries.
func WithZaiBuiltins(models []*ModelInfo) []*ModelInfo {
	return upsertModelInfos(models, zaiBuiltinModelInfos()...)
}
