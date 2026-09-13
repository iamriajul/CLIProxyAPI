// Code generated from the oh-my-pi model catalog (extracted 2026-09-13).
// DO NOT EDIT BY HAND — regenerate from the reference dump. It mirrors the
// models.json sections so gateway credentials stay routable when the remote
// catalog carries no matching section (same pattern as the Muse builtins).
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
	models := make([]*ModelInfo, 0, 16)
	models = append(models, &ModelInfo{
		ID:                        "glm-4.5",
		Object:                    "model",
		Created:                   1789257600,
		OwnedBy:                   "zai",
		Type:                      "zai",
		DisplayName:               "GLM-4.5",
		Description:               "GLM-4.5 via Z.AI.",
		ContextLength:             131072,
		MaxCompletionTokens:       98304,
		Thinking:                  &ThinkingSupport{Levels: []string{"minimal", "low", "medium", "high", "xhigh"}},
		SupportedInputModalities:  []string{"text"},
		SupportedOutputModalities: []string{"text"},
	})
	models = append(models, &ModelInfo{
		ID:                        "glm-4.5-air",
		Object:                    "model",
		Created:                   1789257600,
		OwnedBy:                   "zai",
		Type:                      "zai",
		DisplayName:               "GLM-4.5-Air",
		Description:               "GLM-4.5-Air via Z.AI.",
		ContextLength:             131072,
		MaxCompletionTokens:       98304,
		Thinking:                  &ThinkingSupport{Levels: []string{"minimal", "low", "medium", "high", "xhigh"}},
		SupportedInputModalities:  []string{"text"},
		SupportedOutputModalities: []string{"text"},
	})
	models = append(models, &ModelInfo{
		ID:                        "glm-4.5-flash",
		Object:                    "model",
		Created:                   1789257600,
		OwnedBy:                   "zai",
		Type:                      "zai",
		DisplayName:               "GLM-4.5-Flash",
		Description:               "GLM-4.5-Flash via Z.AI.",
		ContextLength:             131072,
		MaxCompletionTokens:       98304,
		Thinking:                  &ThinkingSupport{Levels: []string{"minimal", "low", "medium", "high", "xhigh"}},
		SupportedInputModalities:  []string{"text"},
		SupportedOutputModalities: []string{"text"},
	})
	models = append(models, &ModelInfo{
		ID:                        "glm-4.5v",
		Object:                    "model",
		Created:                   1789257600,
		OwnedBy:                   "zai",
		Type:                      "zai",
		DisplayName:               "GLM-4.5V",
		Description:               "GLM-4.5V via Z.AI.",
		ContextLength:             64000,
		MaxCompletionTokens:       16384,
		Thinking:                  &ThinkingSupport{Levels: []string{"minimal", "low", "medium", "high", "xhigh"}},
		SupportedInputModalities:  []string{"text", "image"},
		SupportedOutputModalities: []string{"text"},
	})
	models = append(models, &ModelInfo{
		ID:                        "glm-4.6",
		Object:                    "model",
		Created:                   1789257600,
		OwnedBy:                   "zai",
		Type:                      "zai",
		DisplayName:               "GLM-4.6",
		Description:               "GLM-4.6 via Z.AI.",
		ContextLength:             204800,
		MaxCompletionTokens:       131072,
		Thinking:                  &ThinkingSupport{Levels: []string{"minimal", "low", "medium", "high", "xhigh"}},
		SupportedInputModalities:  []string{"text"},
		SupportedOutputModalities: []string{"text"},
	})
	models = append(models, &ModelInfo{
		ID:                        "glm-4.6v",
		Object:                    "model",
		Created:                   1789257600,
		OwnedBy:                   "zai",
		Type:                      "zai",
		DisplayName:               "GLM-4.6V",
		Description:               "GLM-4.6V via Z.AI.",
		ContextLength:             128000,
		MaxCompletionTokens:       32768,
		Thinking:                  &ThinkingSupport{Levels: []string{"minimal", "low", "medium", "high", "xhigh"}},
		SupportedInputModalities:  []string{"text", "image"},
		SupportedOutputModalities: []string{"text"},
	})
	models = append(models, &ModelInfo{
		ID:                        "glm-4.7",
		Object:                    "model",
		Created:                   1789257600,
		OwnedBy:                   "zai",
		Type:                      "zai",
		DisplayName:               "GLM-4.7",
		Description:               "GLM-4.7 via Z.AI.",
		ContextLength:             204800,
		MaxCompletionTokens:       131072,
		Thinking:                  &ThinkingSupport{Levels: []string{"minimal", "low", "medium", "high", "xhigh"}},
		SupportedInputModalities:  []string{"text"},
		SupportedOutputModalities: []string{"text"},
	})
	models = append(models, &ModelInfo{
		ID:                        "glm-4.7-flash",
		Object:                    "model",
		Created:                   1789257600,
		OwnedBy:                   "zai",
		Type:                      "zai",
		DisplayName:               "GLM-4.7-Flash",
		Description:               "GLM-4.7-Flash via Z.AI.",
		ContextLength:             200000,
		MaxCompletionTokens:       131072,
		Thinking:                  &ThinkingSupport{Levels: []string{"minimal", "low", "medium", "high", "xhigh"}},
		SupportedInputModalities:  []string{"text"},
		SupportedOutputModalities: []string{"text"},
	})
	models = append(models, &ModelInfo{
		ID:                        "glm-4.7-flashx",
		Object:                    "model",
		Created:                   1789257600,
		OwnedBy:                   "zai",
		Type:                      "zai",
		DisplayName:               "GLM-4.7-FlashX",
		Description:               "GLM-4.7-FlashX via Z.AI.",
		ContextLength:             200000,
		MaxCompletionTokens:       131072,
		Thinking:                  &ThinkingSupport{Levels: []string{"minimal", "low", "medium", "high", "xhigh"}},
		SupportedInputModalities:  []string{"text"},
		SupportedOutputModalities: []string{"text"},
	})
	models = append(models, &ModelInfo{
		ID:                        "glm-5",
		Object:                    "model",
		Created:                   1789257600,
		OwnedBy:                   "zai",
		Type:                      "zai",
		DisplayName:               "GLM-5",
		Description:               "GLM-5 via Z.AI.",
		ContextLength:             204800,
		MaxCompletionTokens:       131072,
		Thinking:                  &ThinkingSupport{Levels: []string{"minimal", "low", "medium", "high", "xhigh"}},
		SupportedInputModalities:  []string{"text"},
		SupportedOutputModalities: []string{"text"},
	})
	models = append(models, &ModelInfo{
		ID:                        "glm-5-turbo",
		Object:                    "model",
		Created:                   1789257600,
		OwnedBy:                   "zai",
		Type:                      "zai",
		DisplayName:               "GLM-5-Turbo",
		Description:               "GLM-5-Turbo via Z.AI.",
		ContextLength:             200000,
		MaxCompletionTokens:       131072,
		Thinking:                  &ThinkingSupport{Levels: []string{"minimal", "low", "medium", "high", "xhigh"}},
		SupportedInputModalities:  []string{"text"},
		SupportedOutputModalities: []string{"text"},
	})
	models = append(models, &ModelInfo{
		ID:                        "glm-5.1",
		Object:                    "model",
		Created:                   1789257600,
		OwnedBy:                   "zai",
		Type:                      "zai",
		DisplayName:               "GLM-5.1",
		Description:               "GLM-5.1 via Z.AI.",
		ContextLength:             200000,
		MaxCompletionTokens:       131072,
		Thinking:                  &ThinkingSupport{Levels: []string{"minimal", "low", "medium", "high", "xhigh"}},
		SupportedInputModalities:  []string{"text"},
		SupportedOutputModalities: []string{"text"},
	})
	models = append(models, &ModelInfo{
		ID:                        "glm-5.2",
		Object:                    "model",
		Created:                   1789257600,
		OwnedBy:                   "zai",
		Type:                      "zai",
		DisplayName:               "GLM-5.2",
		Description:               "GLM-5.2 via Z.AI.",
		ContextLength:             1000000,
		MaxCompletionTokens:       131072,
		Thinking:                  &ThinkingSupport{Levels: []string{"high", "max"}},
		SupportedInputModalities:  []string{"text"},
		SupportedOutputModalities: []string{"text"},
	})
	models = append(models, &ModelInfo{
		ID:                        "glm-5.3",
		Object:                    "model",
		Created:                   1789257600,
		OwnedBy:                   "zai",
		Type:                      "zai",
		DisplayName:               "GLM-5.3",
		Description:               "GLM-5.3 via Z.AI.",
		ContextLength:             1000000,
		MaxCompletionTokens:       131072,
		Thinking:                  &ThinkingSupport{Levels: []string{"low", "high", "max"}},
		SupportedInputModalities:  []string{"text"},
		SupportedOutputModalities: []string{"text"},
	})
	models = append(models, &ModelInfo{
		ID:                        "glm-5.3-flash",
		Object:                    "model",
		Created:                   1789257600,
		OwnedBy:                   "zai",
		Type:                      "zai",
		DisplayName:               "GLM-5.3-Flash",
		Description:               "GLM-5.3-Flash via Z.AI.",
		ContextLength:             1000000,
		MaxCompletionTokens:       131072,
		Thinking:                  &ThinkingSupport{Levels: []string{"low", "high", "max"}},
		SupportedInputModalities:  []string{"text", "image"},
		SupportedOutputModalities: []string{"text"},
	})
	models = append(models, &ModelInfo{
		ID:                        "glm-5v-turbo",
		Object:                    "model",
		Created:                   1789257600,
		OwnedBy:                   "zai",
		Type:                      "zai",
		DisplayName:               "GLM-5V-Turbo",
		Description:               "GLM-5V-Turbo via Z.AI.",
		ContextLength:             200000,
		MaxCompletionTokens:       131072,
		Thinking:                  &ThinkingSupport{Levels: []string{"minimal", "low", "medium", "high", "xhigh"}},
		SupportedInputModalities:  []string{"text", "image"},
		SupportedOutputModalities: []string{"text"},
	})
	return models
}

// WithZaiBuiltins upserts the hard-coded GLM snapshot over catalog entries.
func WithZaiBuiltins(models []*ModelInfo) []*ModelInfo {
	return upsertModelInfos(models, zaiBuiltinModelInfos()...)
}
