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
	"bytes"
	"encoding/json"
	"fmt"
	"net/url"
	"sort"
	"strings"
	"sync"
	"time"

	log "github.com/sirupsen/logrus"
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

	// Name is the provider's human-readable label, used to build the
	// "… via <provider>" description on converted models.
	Name string `json:"name"`

	// API is the provider's OpenAI-compatible base URL. It is the join key
	// against a configured custom provider's base-url, so the field is
	// decoded even though the opencode/zai sections never read it.
	API string `json:"api"`
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
		out.Opencode, _ = convertModelsDevProvider("opencode", "OpenCode Zen Go", ocProvider.Models)
	}
	if zaiOK {
		out.HasZai = true
		out.Zai, _ = convertModelsDevProvider("zai", "Z.AI", zaiProvider.Models)
	}
	return out, nil
}

// convertModelsDevProvider converts one models.dev provider's models. Each
// returned entry is paired with the raw payload it came from, so callers that
// need fields not represented on ModelInfo do not have to reverse-lookup by ID
// (which breaks whenever a model omits its id field and inherits the map key).
func convertModelsDevProvider(ownedBy, via string, models map[string]modelsDevModel) ([]*ModelInfo, map[string]modelsDevModel) {
	// Sorted keys keep output deterministic and duplicate resolution
	// first-wins by (key, id) order instead of random map order.
	keys := make([]string, 0, len(models))
	for key := range models {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	seen := make(map[string]struct{}, len(models))
	raws := make(map[string]modelsDevModel, len(models))
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
		raws[id] = raw
		out = append(out, info)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].ID < out[j].ID })
	return out, raws
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

// modelsDevReportsEffortLevels reports whether models.dev publishes discrete
// effort levels for a model. Toggle-only and budget-only reasoning models
// return false: they reason, but they have no level ladder, so advertising
// one to a client would be a fabricated capability.
func modelsDevReportsEffortLevels(raw modelsDevModel) bool {
	for _, option := range raw.ReasoningOptions {
		if !strings.EqualFold(strings.TrimSpace(option.Type), "effort") {
			continue
		}
		for _, value := range option.Values {
			if strings.TrimSpace(value) != "" {
				return true
			}
		}
	}
	return false
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

// modelsDevCapabilities holds the live models.dev capability metadata for
// OpenAI-compatible providers, keyed by normalized base URL. It is the lookup
// that lets a configured custom provider (base URL + API key) inherit the
// limits, modalities, parameters and reasoning levels models.dev publishes
// for that endpoint, instead of advertising a guessed low/medium/high
// reasoning ladder to clients.
type modelsDevCapabilities struct {
	mu      sync.RWMutex
	byBase  map[string][]*ModelInfo
	rawJSON []byte

	// byModel maps a lowercased model ID to the models.dev provider IDs that
	// publish it, sorted. It backs the provider picker: a model served by
	// exactly one provider is unambiguous and needs no choice, while a model
	// many providers serve must let the operator say which catalog entry is
	// theirs. Only providers with an api base URL are included, because the
	// others are not reachable as a custom provider endpoint.
	byModel map[string][]string

	// byProvider holds the same capability entries keyed by models.dev
	// provider ID, so a configuration that pins a provider can resolve the
	// model even when that provider's base URL differs from the configured
	// one (a proxy in front of it, for instance).
	byProvider map[string]map[string]*ModelInfo

	// providerAPI maps a models.dev provider ID to its published base URL.
	providerAPI map[string]string
}

var modelsDevCustomProviders = &modelsDevCapabilities{}

// modelsDevBaseKey normalizes a models.dev api base URL and a configured
// provider base URL to the same lookup key. Normalization is deliberately
// conservative: scheme, host case, userinfo, a trailing "/v1" segment and
// trailing slashes are all folded, because operators routinely write the same
// endpoint as "https://openrouter.ai/api/v1" and "https://openrouter.ai/api/v1/".
// Path, query and fragment differences are left intact: two different paths on
// one host are different APIs and must not share capability metadata.
func modelsDevBaseKey(raw string) string {
	value := strings.TrimSpace(raw)
	if value == "" {
		return ""
	}
	parsed, err := url.Parse(value)
	if err != nil {
		return ""
	}
	host := strings.ToLower(parsed.Hostname())
	if host == "" {
		return ""
	}
	port := parsed.Port()
	// Path segments are case-sensitive; only the well-known trailing API
	// version segment is folded away.
	path := strings.TrimSuffix(strings.TrimRight(parsed.EscapedPath(), "/"), "/v1")
	if port != "" {
		host = host + ":" + port
	}
	return host + path
}

// ModelsDevBaseURLKey normalizes an OpenAI-compatible base URL to the key used
// by the models.dev custom-provider index. It is exported so the registration
// refresh can tell whether a changed catalog entry applies to a configured
// provider.
func ModelsDevBaseURLKey(baseURL string) string {
	return modelsDevBaseKey(baseURL)
}

// ModelsDevCustomProviderPrefix marks a models.dev refresh change as belonging
// to a custom (OpenAI-compatible) provider identified by base URL, rather than
// to one of the fixed provider IDs. The registration callback uses it to
// re-register the matching openai-compatibility providers.
const ModelsDevCustomProviderPrefix = "openai-compat-base:"

// loadModelsDevCustomProviders indexes the whole models.dev catalog by base
// URL and reports the base URLs whose capabilities changed. Unlike the
// opencode/zai sections this keeps every provider, because a custom provider is
// matched by base URL rather than by a fixed provider ID. Providers without an
// api field (SDK-only and cloud-provider entries such as anthropic, groq or
// azure) are not reachable by base URL and are skipped.
//
// A single conflicting model ID is dropped rather than allowed to shadow its
// siblings: some providers reuse a slug across distinct endpoints (several
// vendors expose both a pay-per-token and a coding-plan route). Within one
// base URL the models.dev map is authoritative, so its metadata is kept.
func loadModelsDevCustomProviders(data []byte, source string) ([]string, error) {
	var catalog modelsDevCatalog
	if err := json.Unmarshal(data, &catalog); err != nil {
		return nil, fmt.Errorf("%s: decode models.dev catalog: %w", source, err)
	}
	byBase := make(map[string][]*ModelInfo, len(catalog))
	byModel := make(map[string][]string, len(catalog))
	byProvider := make(map[string]map[string]*ModelInfo, len(catalog))
	providerAPI := make(map[string]string, len(catalog))
	for _, providerID := range sortedModelsDevProviderIDs(catalog) {
		provider := catalog[providerID]
		key := modelsDevBaseKey(provider.API)
		if key == "" {
			continue
		}
		via := strings.TrimSpace(provider.Name)
		models, raws := convertModelsDevProvider(providerID, via, provider.Models)
		// convertModelsDevProvider invents a low/medium/high ladder for
		// toggle-only reasoning models, which is the right default for the
		// curated opencode/zai sections but a wrong parameter to publish for
		// a custom endpoint. Drop the invented levels here so the index
		// carries only ladders models.dev actually declares.
		for _, model := range models {
			if model.Thinking == nil {
				continue
			}
			if !modelsDevReportsEffortLevels(raws[model.ID]) {
				model.Thinking = nil
			}
		}
		if len(models) == 0 {
			continue
		}
		// Several vendors expose a pay-per-token and a coding-plan route on one
		// host, so the same model ID can arrive twice. Tag each entry with the
		// route it came from; capability resolution below decides whether that
		// duplication is harmless or a genuine conflict.
		for _, model := range models {
			model.OwnedBy = providerID
			byModel[strings.ToLower(model.ID)] = append(byModel[strings.ToLower(model.ID)], providerID)
			entry := byProvider[providerID]
			if entry == nil {
				entry = make(map[string]*ModelInfo, len(models))
				byProvider[providerID] = entry
			}
			modelKey := strings.ToLower(model.ID)
			if existing, ok := entry[modelKey]; !ok || modelsDevCapabilitiesAgree([]*ModelInfo{existing, model}) {
				entry[modelKey] = model
			}
		}
		providerAPI[providerID] = strings.TrimSpace(provider.API)
		byBase[key] = append(byBase[key], models...)
	}
	for modelID := range byModel {
		sort.Strings(byModel[modelID])
		byModel[modelID] = compactSortedStrings(byModel[modelID])
	}
	for key, models := range byBase {
		byBase[key] = resolveModelsDevEntries(models)
	}

	modelsDevCustomProviders.mu.Lock()
	defer modelsDevCustomProviders.mu.Unlock()
	if bytes.Equal(modelsDevCustomProviders.rawJSON, data) {
		return nil, nil
	}
	// Report the base URLs whose capabilities actually moved, so the refresh
	// callback can re-register the affected custom providers. Without this the
	// index would update in place while the registered models clients see kept
	// serving the previous metadata until the process restarted.
	changed := make([]string, 0, len(byBase))
	for key, models := range byBase {
		if modelSectionChanged(modelsDevCustomProviders.byBase[key], models) {
			changed = append(changed, key)
		}
	}
	for key := range modelsDevCustomProviders.byBase {
		if _, ok := byBase[key]; !ok {
			changed = append(changed, key)
		}
	}
	sort.Strings(changed)
	modelsDevCustomProviders.byBase = byBase
	modelsDevCustomProviders.byModel = byModel
	modelsDevCustomProviders.byProvider = byProvider
	modelsDevCustomProviders.providerAPI = providerAPI
	modelsDevCustomProviders.rawJSON = append([]byte(nil), data...)
	return changed, nil
}

// GetModelsDevByBaseURL returns a clone of the models.dev capability metadata
// published for an OpenAI-compatible endpoint. It returns nil when the base
// URL is unknown, so callers must treat "no catalog entry" as "no opinion" and
// keep whatever the configuration already declares.
func GetModelsDevByBaseURL(baseURL string) []*ModelInfo {
	key := modelsDevBaseKey(baseURL)
	if key == "" {
		return nil
	}
	modelsDevCustomProviders.mu.RLock()
	defer modelsDevCustomProviders.mu.RUnlock()
	return cloneModelInfos(modelsDevCustomProviders.byBase[key])
}

// GetModelsDevBaseURLs lists the base URLs the live index currently covers.
// Management surfaces use it to report coverage; it is not client metadata.
func GetModelsDevBaseURLs() []string {
	modelsDevCustomProviders.mu.RLock()
	defer modelsDevCustomProviders.mu.RUnlock()
	out := make([]string, 0, len(modelsDevCustomProviders.byBase))
	for key := range modelsDevCustomProviders.byBase {
		out = append(out, key)
	}
	sort.Strings(out)
	return out
}

// ModelsDevProviderInfo describes one models.dev provider that can back a
// custom provider, as offered by the management provider picker.
type ModelsDevProviderInfo struct {
	// ID is the exact models.dev provider ID to store in configuration.
	ID string `json:"id"`
	// Name is the human-readable label.
	Name string `json:"name"`
	// API is the OpenAI-compatible base URL models.dev publishes for it.
	API string `json:"api"`
	// ContextLength is the context window of the model this entry describes,
	// shown in the picker as a hint.
	ContextLength int `json:"context_length,omitempty"`
}

// GetModelsDevProvidersForModel returns the models.dev providers publishing a
// model, ordered with the provider reachable at baseURL first when given. The
// result is empty when no provider publishes the model, which callers treat
// as "no catalog entry" rather than as an error.
//
// A single-element result is unambiguous: the caller may apply it without
// asking the operator to choose.
func GetModelsDevProvidersForModel(model, baseURL string) []ModelsDevProviderInfo {
	modelKey := strings.ToLower(strings.TrimSpace(model))
	if modelKey == "" {
		return nil
	}
	modelsDevCustomProviders.mu.RLock()
	providerIDs := append([]string(nil), modelsDevCustomProviders.byModel[modelKey]...)
	byProvider := modelsDevCustomProviders.byProvider
	providerAPI := modelsDevCustomProviders.providerAPI
	modelsDevCustomProviders.mu.RUnlock()
	if len(providerIDs) == 0 {
		return nil
	}
	baseKey := modelsDevBaseKey(baseURL)
	out := make([]ModelsDevProviderInfo, 0, len(providerIDs))
	for _, id := range providerIDs {
		info := ModelsDevProviderInfo{ID: id, API: providerAPI[id]}
		if model := byProvider[id][modelKey]; model != nil {
			info.Name = model.DisplayName
			info.ContextLength = model.ContextLength
		}
		out = append(out, info)
	}
	// The provider the operator is actually pointed at comes first, so the
	// pre-selected option is almost always the right one.
	sort.SliceStable(out, func(i, j int) bool {
		iMatch := baseKey != "" && modelsDevBaseKey(out[i].API) == baseKey
		jMatch := baseKey != "" && modelsDevBaseKey(out[j].API) == baseKey
		if iMatch != jMatch {
			return iMatch
		}
		return out[i].ID < out[j].ID
	})
	return out
}

// GetModelsDevProviderModel returns the capability entry a pinned models.dev
// provider publishes for a model. It returns nil when the provider does not
// publish it, so a stale configuration pin degrades to "no catalog entry"
// rather than to wrong metadata.
func GetModelsDevProviderModel(providerID, model string) *ModelInfo {
	providerKey := strings.ToLower(strings.TrimSpace(providerID))
	modelKey := strings.ToLower(strings.TrimSpace(model))
	if providerKey == "" || modelKey == "" {
		return nil
	}
	modelsDevCustomProviders.mu.RLock()
	defer modelsDevCustomProviders.mu.RUnlock()
	entry := modelsDevCustomProviders.byProvider[providerKey][modelKey]
	if entry == nil {
		return nil
	}
	return cloneModelInfo(entry)
}

// resolveModelsDevEntries collapses a model ID that arrived from more than one
// models.dev route on the same endpoint.
//
// Vendors commonly publish a pay-per-token and a coding-plan route on one
// host, so the same model ID can appear twice. The two cases are not the same:
//
//   - Agreeing entries are pure duplication. One copy is kept, because both
//     routes describe the same model with the same capabilities.
//   - Disagreeing entries mean the routes genuinely disagree about a limit the
//     client will act on. Picking one arbitrarily would advertise a context
//     window the upstream may reject, so the model is dropped and the caller
//     falls back to its own configuration.
//
// Comparison is on capability only. Presentation fields (display name,
// description, release date) are ignored, so a cosmetic difference between two
// routes does not discard a model.
func resolveModelsDevEntries(models []*ModelInfo) []*ModelInfo {
	byID := make(map[string][]*ModelInfo, len(models))
	order := make([]string, 0, len(models))
	for _, model := range models {
		key := strings.ToLower(model.ID)
		if _, seen := byID[key]; !seen {
			order = append(order, key)
		}
		byID[key] = append(byID[key], model)
	}
	out := make([]*ModelInfo, 0, len(models))
	for _, key := range order {
		group := byID[key]
		if len(group) == 1 || modelsDevCapabilitiesAgree(group) {
			out = append(out, group[0])
			continue
		}
		log.Warnf("models.dev: %s is published with conflicting capabilities on one endpoint (routes %s); omitting it rather than publishing a guess",
			group[0].ID, modelsDevRouteNames(group))
	}
	return out
}

// modelsDevCapabilitiesAgree reports whether every entry describes the same
// capabilities. Only the fields a client acts on are compared.
func modelsDevCapabilitiesAgree(group []*ModelInfo) bool {
	if len(group) < 2 {
		return true
	}
	first := group[0]
	for _, other := range group[1:] {
		if other.ContextLength != first.ContextLength ||
			other.MaxCompletionTokens != first.MaxCompletionTokens ||
			other.InputTokenLimit != first.InputTokenLimit ||
			other.OutputTokenLimit != first.OutputTokenLimit {
			return false
		}
		if !equalModelsDevStrings(other.SupportedInputModalities, first.SupportedInputModalities) ||
			!equalModelsDevStrings(other.SupportedOutputModalities, first.SupportedOutputModalities) ||
			!equalModelsDevStrings(other.SupportedParameters, first.SupportedParameters) {
			return false
		}
		if !equalModelsDevThinking(other.Thinking, first.Thinking) {
			return false
		}
	}
	return true
}

func equalModelsDevThinking(a, b *ThinkingSupport) bool {
	if a == nil || b == nil {
		return a == nil && b == nil
	}
	return a.Min == b.Min && a.Max == b.Max && a.ZeroAllowed == b.ZeroAllowed &&
		a.DynamicAllowed == b.DynamicAllowed && equalModelsDevStrings(a.Levels, b.Levels)
}

// equalModelsDevStrings compares order-insensitively: two routes may list the
// same capabilities in a different order without disagreeing.
func equalModelsDevStrings(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	seen := make(map[string]int, len(a))
	for _, value := range a {
		seen[strings.ToLower(strings.TrimSpace(value))]++
	}
	for _, value := range b {
		key := strings.ToLower(strings.TrimSpace(value))
		if seen[key] == 0 {
			return false
		}
		seen[key]--
	}
	return true
}

func modelsDevRouteNames(group []*ModelInfo) string {
	names := make([]string, 0, len(group))
	for _, model := range group {
		if name := strings.TrimSpace(model.OwnedBy); name != "" {
			names = append(names, name)
		}
	}
	sort.Strings(names)
	return strings.Join(names, ", ")
}

// compactSortedStrings removes adjacent duplicates from a sorted slice.
func compactSortedStrings(values []string) []string {
	if len(values) < 2 {
		return values
	}
	out := values[:1]
	for _, value := range values[1:] {
		if value != out[len(out)-1] {
			out = append(out, value)
		}
	}
	return out
}

func sortedModelsDevProviderIDs(catalog modelsDevCatalog) []string {
	ids := make([]string, 0, len(catalog))
	for id := range catalog {
		ids = append(ids, id)
	}
	sort.Strings(ids)
	return ids
}

// SeedModelsDevCustomProvidersForTest installs a models.dev catalog payload
// into the custom-provider index without performing a network fetch. Tests
// only; the runtime path is the models.dev refresh ticker.
func SeedModelsDevCustomProvidersForTest(data []byte) error {
	_, err := loadModelsDevCustomProviders(data, "test seed")
	return err
}

// ResetModelsDevCustomProvidersForTest clears the custom-provider index.
// Tests only.
func ResetModelsDevCustomProvidersForTest() {
	modelsDevCustomProviders.mu.Lock()
	defer modelsDevCustomProviders.mu.Unlock()
	modelsDevCustomProviders.byBase = nil
	modelsDevCustomProviders.byModel = nil
	modelsDevCustomProviders.byProvider = nil
	modelsDevCustomProviders.providerAPI = nil
	modelsDevCustomProviders.rawJSON = nil
}
