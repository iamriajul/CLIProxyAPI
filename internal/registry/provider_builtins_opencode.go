// Code generated from the oh-my-pi model catalog (extracted 2026-09-13).
// DO NOT EDIT BY HAND — regenerate from the reference dump. It mirrors the
// models.json sections so gateway credentials stay routable when the remote
// catalog carries no matching section (same pattern as the Muse builtins).
package registry

import "strings"

var opencodeAnthropicRouteModels = map[string]bool{
	"minimax-m2.5":  true,
	"qwen3.8-flash": true,
}

var opencodeResponsesRouteModels = map[string]bool{
	"deepseek-v4-flash":          true,
	"gpt-5.6-luna":               true,
	"grok-4.5":                   true,
	"grok-4.6":                   true,
	"muse-spark-1.2-contributor": true,
	"muse-spark-1.3-contributor": true,
}

// OpencodeUpstreamRoute reports the gateway wire protocol for a model:
// "anthropic" for the Claude-protocol lanes, "responses" for Responses-native
// lanes, "chat" for everything else.
func OpencodeUpstreamRoute(model string) string {
	key := strings.ToLower(strings.TrimSpace(model))
	if openParen := strings.LastIndex(key, "("); openParen >= 0 && strings.HasSuffix(key, ")") {
		key = strings.TrimSpace(key[:openParen])
	}
	if opencodeAnthropicRouteModels[key] {
		return "anthropic"
	}
	if opencodeResponsesRouteModels[key] {
		return "responses"
	}
	return "chat"
}

// OpencodeBuiltinModelInfos placeholder
func opencodeBuiltinModelInfos() []*ModelInfo {
	models := make([]*ModelInfo, 0, 38)
	models = append(models, &ModelInfo{
		ID:                        "deepseek-flash",
		Object:                    "model",
		Created:                   1789292622,
		OwnedBy:                   "opencode",
		Type:                      "opencode",
		DisplayName:               "deepseek-flash",
		Description:               "deepseek-flash via OpenCode Zen Go.",
		SupportedInputModalities:  []string{"text"},
		SupportedOutputModalities: []string{"text"},
	})
	models = append(models, &ModelInfo{
		ID:                        "deepseek-v4-flash",
		Object:                    "model",
		Created:                   1789257600,
		OwnedBy:                   "opencode",
		Type:                      "opencode",
		DisplayName:               "DeepSeek V4 Flash",
		Description:               "DeepSeek V4 Flash via OpenCode Zen Go.",
		ContextLength:             1000000,
		MaxCompletionTokens:       384000,
		Thinking:                  &ThinkingSupport{Levels: []string{"low", "high", "max"}},
		SupportedInputModalities:  []string{"text"},
		SupportedOutputModalities: []string{"text"},
	})
	models = append(models, &ModelInfo{
		ID:                        "deepseek-v4-flash-vision-exp",
		Object:                    "model",
		Created:                   1789257600,
		OwnedBy:                   "opencode",
		Type:                      "opencode",
		DisplayName:               "DeepSeek V4 Flash Vision Exp",
		Description:               "DeepSeek V4 Flash Vision Exp via OpenCode Zen Go.",
		ContextLength:             1000000,
		MaxCompletionTokens:       384000,
		Thinking:                  &ThinkingSupport{Levels: []string{"low", "high", "max"}},
		SupportedInputModalities:  []string{"text", "image"},
		SupportedOutputModalities: []string{"text"},
	})
	models = append(models, &ModelInfo{
		ID:                        "deepseek-v4-pro",
		Object:                    "model",
		Created:                   1789257600,
		OwnedBy:                   "opencode",
		Type:                      "opencode",
		DisplayName:               "DeepSeek V4 Pro (New)",
		Description:               "DeepSeek V4 Pro (New) via OpenCode Zen Go.",
		ContextLength:             1000000,
		MaxCompletionTokens:       384000,
		Thinking:                  &ThinkingSupport{Levels: []string{"low", "high", "max"}},
		SupportedInputModalities:  []string{"text"},
		SupportedOutputModalities: []string{"text"},
	})
	models = append(models, &ModelInfo{
		ID:                        "deepseek-v4.1-flash",
		Object:                    "model",
		Created:                   1789292622,
		OwnedBy:                   "opencode",
		Type:                      "opencode",
		DisplayName:               "deepseek-v4.1-flash",
		Description:               "deepseek-v4.1-flash via OpenCode Zen Go.",
		SupportedInputModalities:  []string{"text"},
		SupportedOutputModalities: []string{"text"},
	})
	models = append(models, &ModelInfo{
		ID:                        "glm-5",
		Object:                    "model",
		Created:                   1789257600,
		OwnedBy:                   "opencode",
		Type:                      "opencode",
		DisplayName:               "GLM-5",
		Description:               "GLM-5 via OpenCode Zen Go.",
		ContextLength:             202752,
		MaxCompletionTokens:       32768,
		Thinking:                  &ThinkingSupport{Levels: []string{"minimal", "low", "medium", "high", "xhigh"}},
		SupportedInputModalities:  []string{"text"},
		SupportedOutputModalities: []string{"text"},
	})
	models = append(models, &ModelInfo{
		ID:                        "glm-5.1",
		Object:                    "model",
		Created:                   1789257600,
		OwnedBy:                   "opencode",
		Type:                      "opencode",
		DisplayName:               "GLM-5.1",
		Description:               "GLM-5.1 via OpenCode Zen Go.",
		ContextLength:             202752,
		MaxCompletionTokens:       32768,
		Thinking:                  &ThinkingSupport{Levels: []string{"minimal", "low", "medium", "high", "xhigh"}},
		SupportedInputModalities:  []string{"text"},
		SupportedOutputModalities: []string{"text"},
	})
	models = append(models, &ModelInfo{
		ID:                        "glm-5.2",
		Object:                    "model",
		Created:                   1789257600,
		OwnedBy:                   "opencode",
		Type:                      "opencode",
		DisplayName:               "GLM-5.2",
		Description:               "GLM-5.2 via OpenCode Zen Go.",
		ContextLength:             1000000,
		MaxCompletionTokens:       131072,
		Thinking:                  &ThinkingSupport{Levels: []string{"minimal", "low", "medium", "high", "max"}},
		SupportedInputModalities:  []string{"text"},
		SupportedOutputModalities: []string{"text"},
	})
	models = append(models, &ModelInfo{
		ID:                        "glm-5.3",
		Object:                    "model",
		Created:                   1789257600,
		OwnedBy:                   "opencode",
		Type:                      "opencode",
		DisplayName:               "GLM-5.3",
		Description:               "GLM-5.3 via OpenCode Zen Go.",
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
		OwnedBy:                   "opencode",
		Type:                      "opencode",
		DisplayName:               "GLM-5.3-Flash (2x usage)",
		Description:               "GLM-5.3-Flash (2x usage) via OpenCode Zen Go.",
		ContextLength:             1000000,
		MaxCompletionTokens:       131072,
		Thinking:                  &ThinkingSupport{Levels: []string{"low", "high", "max"}},
		SupportedInputModalities:  []string{"text", "image"},
		SupportedOutputModalities: []string{"text"},
	})
	models = append(models, &ModelInfo{
		ID:                        "gpt-5.6-luna",
		Object:                    "model",
		Created:                   1789257600,
		OwnedBy:                   "opencode",
		Type:                      "opencode",
		DisplayName:               "GPT-5.6 Luna",
		Description:               "GPT-5.6 Luna via OpenCode Zen Go.",
		ContextLength:             1050000,
		MaxCompletionTokens:       128000,
		Thinking:                  &ThinkingSupport{Levels: []string{"low", "medium", "high", "xhigh", "max"}},
		SupportedInputModalities:  []string{"text", "image"},
		SupportedOutputModalities: []string{"text"},
	})
	models = append(models, &ModelInfo{
		ID:                        "grok-4.5",
		Object:                    "model",
		Created:                   1789257600,
		OwnedBy:                   "opencode",
		Type:                      "opencode",
		DisplayName:               "Grok 4.5",
		Description:               "Grok 4.5 via OpenCode Zen Go.",
		ContextLength:             500000,
		MaxCompletionTokens:       500000,
		Thinking:                  &ThinkingSupport{Levels: []string{"minimal", "low", "medium", "high", "xhigh"}},
		SupportedInputModalities:  []string{"text", "image"},
		SupportedOutputModalities: []string{"text"},
	})
	models = append(models, &ModelInfo{
		ID:                        "grok-4.6",
		Object:                    "model",
		Created:                   1789257600,
		OwnedBy:                   "opencode",
		Type:                      "opencode",
		DisplayName:               "Grok 4.6",
		Description:               "Grok 4.6 via OpenCode Zen Go.",
		ContextLength:             500000,
		MaxCompletionTokens:       500000,
		Thinking:                  &ThinkingSupport{Levels: []string{"minimal", "low", "medium", "high", "xhigh"}},
		SupportedInputModalities:  []string{"text", "image"},
		SupportedOutputModalities: []string{"text"},
	})
	models = append(models, &ModelInfo{
		ID:                        "hy3",
		Object:                    "model",
		Created:                   1789257600,
		OwnedBy:                   "opencode",
		Type:                      "opencode",
		DisplayName:               "Hy3",
		Description:               "Hy3 via OpenCode Zen Go.",
		ContextLength:             256000,
		MaxCompletionTokens:       128000,
		Thinking:                  &ThinkingSupport{Levels: []string{"minimal", "low", "medium", "high", "xhigh"}},
		SupportedInputModalities:  []string{"text"},
		SupportedOutputModalities: []string{"text"},
	})
	models = append(models, &ModelInfo{
		ID:                        "hy3-preview",
		Object:                    "model",
		Created:                   1789292622,
		OwnedBy:                   "opencode",
		Type:                      "opencode",
		DisplayName:               "hy3-preview",
		Description:               "hy3-preview via OpenCode Zen Go.",
		SupportedInputModalities:  []string{"text"},
		SupportedOutputModalities: []string{"text"},
	})
	models = append(models, &ModelInfo{
		ID:                        "hy4-preview",
		Object:                    "model",
		Created:                   1789257600,
		OwnedBy:                   "opencode",
		Type:                      "opencode",
		DisplayName:               "Hy4 preview",
		Description:               "Hy4 preview via OpenCode Zen Go.",
		ContextLength:             1024000,
		MaxCompletionTokens:       64000,
		Thinking:                  &ThinkingSupport{Levels: []string{"minimal", "low", "medium", "high", "xhigh"}},
		SupportedInputModalities:  []string{"text"},
		SupportedOutputModalities: []string{"text"},
	})
	models = append(models, &ModelInfo{
		ID:                        "kimi-k2.5",
		Object:                    "model",
		Created:                   1789257600,
		OwnedBy:                   "opencode",
		Type:                      "opencode",
		DisplayName:               "Kimi K2.5",
		Description:               "Kimi K2.5 via OpenCode Zen Go.",
		ContextLength:             262144,
		MaxCompletionTokens:       65536,
		Thinking:                  &ThinkingSupport{Levels: []string{"minimal", "low", "medium", "high", "xhigh"}},
		SupportedInputModalities:  []string{"text", "image"},
		SupportedOutputModalities: []string{"text"},
	})
	models = append(models, &ModelInfo{
		ID:                        "kimi-k2.6",
		Object:                    "model",
		Created:                   1789257600,
		OwnedBy:                   "opencode",
		Type:                      "opencode",
		DisplayName:               "Kimi K2.6",
		Description:               "Kimi K2.6 via OpenCode Zen Go.",
		ContextLength:             262144,
		MaxCompletionTokens:       65536,
		Thinking:                  &ThinkingSupport{Levels: []string{"minimal", "low", "medium", "high", "xhigh"}},
		SupportedInputModalities:  []string{"text", "image"},
		SupportedOutputModalities: []string{"text"},
	})
	models = append(models, &ModelInfo{
		ID:                        "kimi-k2.7-code",
		Object:                    "model",
		Created:                   1789257600,
		OwnedBy:                   "opencode",
		Type:                      "opencode",
		DisplayName:               "Kimi K2.7 Code",
		Description:               "Kimi K2.7 Code via OpenCode Zen Go.",
		ContextLength:             262144,
		MaxCompletionTokens:       262144,
		Thinking:                  &ThinkingSupport{Levels: []string{"minimal", "low", "medium", "high", "xhigh"}},
		SupportedInputModalities:  []string{"text", "image"},
		SupportedOutputModalities: []string{"text"},
	})
	models = append(models, &ModelInfo{
		ID:                        "kimi-k3",
		Object:                    "model",
		Created:                   1789257600,
		OwnedBy:                   "opencode",
		Type:                      "opencode",
		DisplayName:               "Kimi K3",
		Description:               "Kimi K3 via OpenCode Zen Go.",
		ContextLength:             1048576,
		MaxCompletionTokens:       131072,
		Thinking:                  &ThinkingSupport{Levels: []string{"low", "high", "max"}},
		SupportedInputModalities:  []string{"text", "image"},
		SupportedOutputModalities: []string{"text"},
	})
	models = append(models, &ModelInfo{
		ID:                        "longcat-2.0",
		Object:                    "model",
		Created:                   1789257600,
		OwnedBy:                   "opencode",
		Type:                      "opencode",
		DisplayName:               "LongCat-2.0",
		Description:               "LongCat-2.0 via OpenCode Zen Go.",
		ContextLength:             1000000,
		MaxCompletionTokens:       131072,
		Thinking:                  &ThinkingSupport{Levels: []string{"minimal", "low", "medium", "high", "xhigh"}},
		SupportedInputModalities:  []string{"text"},
		SupportedOutputModalities: []string{"text"},
	})
	models = append(models, &ModelInfo{
		ID:                        "mimo-v2-omni",
		Object:                    "model",
		Created:                   1789257600,
		OwnedBy:                   "opencode",
		Type:                      "opencode",
		DisplayName:               "MiMo-V2-Omni",
		Description:               "MiMo-V2-Omni via OpenCode Zen Go.",
		ContextLength:             262144,
		MaxCompletionTokens:       128000,
		Thinking:                  &ThinkingSupport{Levels: []string{"low", "medium", "high"}},
		SupportedInputModalities:  []string{"text", "image"},
		SupportedOutputModalities: []string{"text"},
	})
	models = append(models, &ModelInfo{
		ID:                        "mimo-v2-pro",
		Object:                    "model",
		Created:                   1789257600,
		OwnedBy:                   "opencode",
		Type:                      "opencode",
		DisplayName:               "MiMo-V2-Pro",
		Description:               "MiMo-V2-Pro via OpenCode Zen Go.",
		ContextLength:             1048576,
		MaxCompletionTokens:       128000,
		Thinking:                  &ThinkingSupport{Levels: []string{"low", "medium", "high"}},
		SupportedInputModalities:  []string{"text"},
		SupportedOutputModalities: []string{"text"},
	})
	models = append(models, &ModelInfo{
		ID:                        "mimo-v2.5",
		Object:                    "model",
		Created:                   1789257600,
		OwnedBy:                   "opencode",
		Type:                      "opencode",
		DisplayName:               "MiMo V2.5",
		Description:               "MiMo V2.5 via OpenCode Zen Go.",
		ContextLength:             1000000,
		MaxCompletionTokens:       128000,
		Thinking:                  &ThinkingSupport{Levels: []string{"low", "medium", "high"}},
		SupportedInputModalities:  []string{"text", "image"},
		SupportedOutputModalities: []string{"text"},
	})
	models = append(models, &ModelInfo{
		ID:                        "mimo-v2.5-pro",
		Object:                    "model",
		Created:                   1789257600,
		OwnedBy:                   "opencode",
		Type:                      "opencode",
		DisplayName:               "MiMo V2.5 Pro",
		Description:               "MiMo V2.5 Pro via OpenCode Zen Go.",
		ContextLength:             1048576,
		MaxCompletionTokens:       128000,
		Thinking:                  &ThinkingSupport{Levels: []string{"low", "medium", "high"}},
		SupportedInputModalities:  []string{"text"},
		SupportedOutputModalities: []string{"text"},
	})
	models = append(models, &ModelInfo{
		ID:                        "minimax-m2.5",
		Object:                    "model",
		Created:                   1789257600,
		OwnedBy:                   "opencode",
		Type:                      "opencode",
		DisplayName:               "MiniMax-M2.5",
		Description:               "MiniMax-M2.5 via OpenCode Zen Go.",
		ContextLength:             204800,
		MaxCompletionTokens:       65536,
		Thinking:                  &ThinkingSupport{Levels: []string{"low", "medium", "high"}},
		SupportedInputModalities:  []string{"text"},
		SupportedOutputModalities: []string{"text"},
	})
	models = append(models, &ModelInfo{
		ID:                        "minimax-m2.7",
		Object:                    "model",
		Created:                   1789257600,
		OwnedBy:                   "opencode",
		Type:                      "opencode",
		DisplayName:               "MiniMax-M2.7",
		Description:               "MiniMax-M2.7 via OpenCode Zen Go.",
		ContextLength:             204800,
		MaxCompletionTokens:       131072,
		Thinking:                  &ThinkingSupport{Levels: []string{"low", "medium", "high"}},
		SupportedInputModalities:  []string{"text"},
		SupportedOutputModalities: []string{"text"},
	})
	models = append(models, &ModelInfo{
		ID:                        "minimax-m3",
		Object:                    "model",
		Created:                   1789257600,
		OwnedBy:                   "opencode",
		Type:                      "opencode",
		DisplayName:               "MiniMax-M3",
		Description:               "MiniMax-M3 via OpenCode Zen Go.",
		ContextLength:             1000000,
		MaxCompletionTokens:       131072,
		Thinking:                  &ThinkingSupport{Levels: []string{"minimal", "low", "medium", "high", "xhigh"}},
		SupportedInputModalities:  []string{"text", "image"},
		SupportedOutputModalities: []string{"text"},
	})
	models = append(models, &ModelInfo{
		ID:                        "muse-spark-1.2-contributor",
		Object:                    "model",
		Created:                   1789257600,
		OwnedBy:                   "opencode",
		Type:                      "opencode",
		DisplayName:               "Muse Spark 1.2 Contributor",
		Description:               "Muse Spark 1.2 Contributor via OpenCode Zen Go.",
		ContextLength:             1048576,
		MaxCompletionTokens:       131072,
		Thinking:                  &ThinkingSupport{Levels: []string{"minimal", "low", "medium", "high", "xhigh"}},
		SupportedInputModalities:  []string{"text", "image"},
		SupportedOutputModalities: []string{"text"},
	})
	models = append(models, &ModelInfo{
		ID:                        "muse-spark-1.3-contributor",
		Object:                    "model",
		Created:                   1789257600,
		OwnedBy:                   "opencode",
		Type:                      "opencode",
		DisplayName:               "Muse Spark 1.3 Contributor",
		Description:               "Muse Spark 1.3 Contributor via OpenCode Zen Go.",
		ContextLength:             1048576,
		MaxCompletionTokens:       131072,
		Thinking:                  &ThinkingSupport{Levels: []string{"minimal", "low", "medium", "high", "xhigh"}},
		SupportedInputModalities:  []string{"text", "image"},
		SupportedOutputModalities: []string{"text"},
	})
	models = append(models, &ModelInfo{
		ID:                        "omen-alpha",
		Object:                    "model",
		Created:                   1789292622,
		OwnedBy:                   "opencode",
		Type:                      "opencode",
		DisplayName:               "omen-alpha",
		Description:               "omen-alpha via OpenCode Zen Go.",
		SupportedInputModalities:  []string{"text"},
		SupportedOutputModalities: []string{"text"},
	})
	models = append(models, &ModelInfo{
		ID:                        "ox-alpha-free",
		Object:                    "model",
		Created:                   1789257600,
		OwnedBy:                   "opencode",
		Type:                      "opencode",
		DisplayName:               "Ox Alpha Free (Unlimited)",
		Description:               "Ox Alpha Free (Unlimited) via OpenCode Zen Go.",
		ContextLength:             1000000,
		MaxCompletionTokens:       131072,
		Thinking:                  &ThinkingSupport{Levels: []string{"low", "high", "max"}},
		SupportedInputModalities:  []string{"text", "image"},
		SupportedOutputModalities: []string{"text"},
	})
	models = append(models, &ModelInfo{
		ID:                        "qwen3.5-plus",
		Object:                    "model",
		Created:                   1789257600,
		OwnedBy:                   "opencode",
		Type:                      "opencode",
		DisplayName:               "Qwen3.5 Plus",
		Description:               "Qwen3.5 Plus via OpenCode Zen Go.",
		ContextLength:             262144,
		MaxCompletionTokens:       65536,
		Thinking:                  &ThinkingSupport{Levels: []string{"minimal", "low", "medium", "high"}},
		SupportedInputModalities:  []string{"text", "image"},
		SupportedOutputModalities: []string{"text"},
	})
	models = append(models, &ModelInfo{
		ID:                        "qwen3.6-plus",
		Object:                    "model",
		Created:                   1789257600,
		OwnedBy:                   "opencode",
		Type:                      "opencode",
		DisplayName:               "Qwen3.6 Plus",
		Description:               "Qwen3.6 Plus via OpenCode Zen Go.",
		ContextLength:             1000000,
		MaxCompletionTokens:       65536,
		Thinking:                  &ThinkingSupport{Levels: []string{"minimal", "low", "medium", "high"}},
		SupportedInputModalities:  []string{"text", "image"},
		SupportedOutputModalities: []string{"text"},
	})
	models = append(models, &ModelInfo{
		ID:                        "qwen3.7-max",
		Object:                    "model",
		Created:                   1789257600,
		OwnedBy:                   "opencode",
		Type:                      "opencode",
		DisplayName:               "Qwen3.7 Max",
		Description:               "Qwen3.7 Max via OpenCode Zen Go.",
		ContextLength:             1000000,
		MaxCompletionTokens:       65536,
		Thinking:                  &ThinkingSupport{Levels: []string{"minimal", "low", "medium", "high"}},
		SupportedInputModalities:  []string{"text"},
		SupportedOutputModalities: []string{"text"},
	})
	models = append(models, &ModelInfo{
		ID:                        "qwen3.7-plus",
		Object:                    "model",
		Created:                   1789257600,
		OwnedBy:                   "opencode",
		Type:                      "opencode",
		DisplayName:               "Qwen3.7 Plus",
		Description:               "Qwen3.7 Plus via OpenCode Zen Go.",
		ContextLength:             1000000,
		MaxCompletionTokens:       65536,
		Thinking:                  &ThinkingSupport{Levels: []string{"minimal", "low", "medium", "high"}},
		SupportedInputModalities:  []string{"text", "image"},
		SupportedOutputModalities: []string{"text"},
	})
	models = append(models, &ModelInfo{
		ID:                        "qwen3.8-flash",
		Object:                    "model",
		Created:                   1789257600,
		OwnedBy:                   "opencode",
		Type:                      "opencode",
		DisplayName:               "Qwen3.8 Flash",
		Description:               "Qwen3.8 Flash via OpenCode Zen Go.",
		ContextLength:             1000000,
		MaxCompletionTokens:       131072,
		Thinking:                  &ThinkingSupport{Levels: []string{"minimal", "low", "medium", "high", "xhigh"}},
		SupportedInputModalities:  []string{"text", "image"},
		SupportedOutputModalities: []string{"text"},
	})
	models = append(models, &ModelInfo{
		ID:                        "qwen3.8-max",
		Object:                    "model",
		Created:                   1789257600,
		OwnedBy:                   "opencode",
		Type:                      "opencode",
		DisplayName:               "Qwen3.8 Max",
		Description:               "Qwen3.8 Max via OpenCode Zen Go.",
		ContextLength:             1000000,
		MaxCompletionTokens:       131072,
		Thinking:                  &ThinkingSupport{Levels: []string{"minimal", "low", "medium", "high"}},
		SupportedInputModalities:  []string{"text", "image"},
		SupportedOutputModalities: []string{"text"},
	})
	return models
}

// WithOpencodeBuiltins upserts the hard-coded Zen Go lane snapshot over catalog entries.
func WithOpencodeBuiltins(models []*ModelInfo) []*ModelInfo {
	return upsertModelInfos(models, opencodeBuiltinModelInfos()...)
}
