// Package registry model source for models.dev.
//
// models.dev (https://models.dev/api.json) is the realtime source of truth
// for the OpenCode Zen Go gateway lanes and the Z.AI GLM Coding Plan lanes.
// Provider IDs are exact: "opencode-go" is the Go gateway
// (https://opencode.ai/zen/go/v1) and must never be confused with "opencode"
// (OpenCode Zen, https://opencode.ai/zen/v1); "zai-coding-plan" is the flat
// coding plan (https://api.z.ai/api/coding/paas/v4), not the "zai" or
// "zhipu" pay-per-token lanes.
package registry

import (
	"encoding/json"
	"fmt"
	"sort"
	"strings"
	"time"
)

// ModelsDevProviderIDs lists the exact models.dev provider IDs this fork tracks.
func ModelsDevProviderIDs() []string {
	return []string{"opencode-go", "zai-coding-plan"}
}

// modelsDevCatalog is the top-level shape of https://models.dev/api.json:
// a map from provider ID to provider payload.
type modelsDevCatalog map[string]modelsDevProvider

type modelsDevProvider struct {
	Models map[string]modelsDevModel `json:"models"`
}

type modelsDevModel struct {
	ID               string                  `json:"id"`
	Name             string                  `json:"name"`
	Reasoning        bool                    `json:"reasoning"`
	ReasoningOptions []modelsDevReasonOption `json:"reasoning_options"`
	ToolCall         bool                    `json:"tool_call"`
	StructuredOutput *bool                   `json:"structured_output"`
	Temperature      bool                    `json:"temperature"`
	ReleaseDate      string                  `json:"release_date"`
	Modalities       modelsDevModalities     `json:"modalities"`
	Limit            modelsDevLimit          `json:"limit"`
}

type modelsDevReasonOption struct {
	Type   string   `json:"type"`
	Values []string `json:"values"`
}

type modelsDevModalities struct {
	Input  []string `json:"input"`
	Output []string `json:"output"`
}

type modelsDevLimit struct {
	Context int `json:"context"`
	Input   int `json:"input"`
	Output  int `json:"output"`
}

// ModelsDevSections is the converted output for the tracked models.dev
// providers. HasOpencode/HasZai record key presence: a missing key leaves
// the slice nil, which the overlay store treats as "no data" (keep previous)
// rather than "empty" (wipe). A present-but-empty section converts to an
// empty non-nil slice; callers must refuse to store or publish it, because
// an upstream glitch must never wipe last-good data.
type ModelsDevSections struct {
	Opencode    []*ModelInfo
	Zai         []*ModelInfo
	HasOpencode bool
	HasZai      bool
}

// ConvertModelsDevCatalog parses api.json bytes and returns ModelInfo slices
// for the opencode-go and zai-coding-plan providers. A missing provider key
// sets its Has flag false; a payload carrying neither key is an error.
func ConvertModelsDevCatalog(data []byte) (ModelsDevSections, error) {
	var out ModelsDevSections
	var catalog modelsDevCatalog
	if err := json.Unmarshal(data, &catalog); err != nil {
		return out, fmt.Errorf("decode models.dev catalog: %w", err)
	}
	ocProvider, ocOK := catalog["opencode-go"]
	zaiProvider, zaiOK := catalog["zai-coding-plan"]
	if !ocOK && !zaiOK {
		return out, fmt.Errorf("models.dev payload carries neither opencode-go nor zai-coding-plan")
	}
	if ocOK {
		out.HasOpencode = true
		out.Opencode = convertModelsDevProvider("opencode", "OpenCode Zen Go", ocProvider.Models)
	}
	if zaiOK {
		out.HasZai = true
		out.Zai = convertModelsDevProvider("zai", "Z.AI", zaiProvider.Models)
	}
	return out, nil
}

func convertModelsDevProvider(ownedBy, via string, models map[string]modelsDevModel) []*ModelInfo {
	// Sorted keys keep output deterministic and duplicate resolution
	// first-wins by (key, id) order instead of random map order.
	keys := make([]string, 0, len(models))
	for key := range models {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	seen := make(map[string]struct{}, len(models))
	out := make([]*ModelInfo, 0, len(models))
	for _, key := range keys {
		raw := models[key]
		id := strings.TrimSpace(raw.ID)
		if id == "" {
			id = strings.TrimSpace(key)
		}
		if id == "" {
			continue
		}
		if _, dup := seen[id]; dup {
			continue
		}
		seen[id] = struct{}{}
		displayName := strings.TrimSpace(raw.Name)
		if displayName == "" {
			displayName = id
		}
		info := &ModelInfo{
			ID:                        id,
			Object:                    "model",
			Created:                   modelsDevReleaseUnix(raw.ReleaseDate),
			OwnedBy:                   ownedBy,
			Type:                      ownedBy,
			DisplayName:               displayName,
			Description:               displayName + " via " + via + ".",
			ContextLength:             raw.Limit.Context,
			MaxCompletionTokens:       raw.Limit.Output,
			InputTokenLimit:           raw.Limit.Input,
			OutputTokenLimit:          raw.Limit.Output,
			SupportedInputModalities:  normalizeModelsDevModalities(raw.Modalities.Input),
			SupportedOutputModalities: normalizeModelsDevModalities(raw.Modalities.Output),
			SupportedParameters:       modelsDevParameters(raw),
			ExplicitInputModalities:   true,
		}
		if info.InputTokenLimit <= 0 {
			info.InputTokenLimit = info.ContextLength
		}
		if thinking := modelsDevThinking(raw); thinking != nil {
			info.Thinking = thinking
			info.ExplicitThinking = true
		}
		out = append(out, info)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].ID < out[j].ID })
	return out
}

func modelsDevReleaseUnix(releaseDate string) int64 {
	releaseDate = strings.TrimSpace(releaseDate)
	if releaseDate == "" {
		return 0
	}
	if parsed, err := time.Parse(time.RFC3339, releaseDate); err == nil {
		return parsed.Unix()
	}
	if parsed, err := time.Parse("2006-01-02", releaseDate); err == nil {
		return parsed.Unix()
	}
	return 0
}
func normalizeModelsDevModalities(raw []string) []string {
	var out []string
	seen := make(map[string]struct{}, len(raw))
	for _, item := range raw {
		modality := strings.ToLower(strings.TrimSpace(item))
		if modality == "" {
			continue
		}
		if _, exists := seen[modality]; exists {
			continue
		}
		seen[modality] = struct{}{}
		out = append(out, modality)
	}
	if len(out) == 0 {
		return []string{"text"}
	}
	return out
}

// modelsDevThinking maps reasoning_options effort values to ThinkingSupport
// levels. Mapping contract (deliberate, mirrors the checked-in snapshots):
// only "effort" options contribute levels; toggle-only, budget-only, or
// option-less reasoning models fall back to the default low/medium/high
// ladder; non-reasoning models get nil. A "none" value flows through as a
// level — downstream NormalizeThinkingSupport sets ZeroAllowed for it.
func modelsDevThinking(raw modelsDevModel) *ThinkingSupport {
	if !raw.Reasoning {
		return nil
	}
	var levels []string
	seen := make(map[string]struct{})
	for _, option := range raw.ReasoningOptions {
		if !strings.EqualFold(strings.TrimSpace(option.Type), "effort") {
			continue
		}
		for _, value := range option.Values {
			level := strings.ToLower(strings.TrimSpace(value))
			if level == "" {
				continue
			}
			if _, exists := seen[level]; exists {
				continue
			}
			seen[level] = struct{}{}
			levels = append(levels, level)
		}
	}
	if len(levels) == 0 {
		levels = []string{"low", "medium", "high"}
	}
	return &ThinkingSupport{Levels: levels}
}

func modelsDevParameters(raw modelsDevModel) []string {
	var params []string
	if raw.ToolCall {
		params = append(params, "tool_choice")
	}
	if raw.StructuredOutput != nil && *raw.StructuredOutput {
		params = append(params, "response_format")
	}
	if raw.Temperature {
		params = append(params, "temperature")
	}
	return params
}
