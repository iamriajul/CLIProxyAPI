package registry

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"sort"
	"strings"
	"sync"
	"time"
)

// Z.AI exposes a plan-scoped, per-credential model discovery endpoint whose
// response is the Codex client model catalog format — the same wire shape the
// Codex client catalog reader in internal/client/codex/models already consumes.
// The field names below are therefore the Codex field names, and the semantics
// follow that reader.
//
// Two properties of that format drive the whole converter:
//
//   - A model whose supported_reasoning_levels list is empty reasons on a
//     toggle and has no level ladder. That is not the same as a model that
//     declares no reasoning at all, and the two are distinguished by the
//     explicit capability booleans, never by the length of the level list.
//   - Z.AI answers a bad key with HTTP 200 and an error body
//     ({"code":401,"msg":"token expired or incorrect","success":false}), so
//     error detection has to inspect the payload shape. Treating that as
//     success yields an empty model list, which silently breaks the lane.

// zaiLiveDefaultOutputTokenLimit is deliberately absent: the Codex catalog
// format publishes no per-model output limit. Its truncation_policy is a
// byte-based compaction threshold, not a completion bound, so publishing it as
// max_completion_tokens would understate the lane by an order of magnitude. A
// discovered lane therefore carries no output limit, and clients read "unknown"
// instead of a number Z.AI never declared.

// zaiLiveModelsStore holds the last successfully discovered lanes per plan
// credential. Discovery is plan-scoped rather than global, so this is a
// per-credential map and not the single-section store the models.dev overlay
// uses.
type zaiLiveModelsStore struct {
	mu       sync.RWMutex
	byKeyID  map[string]zaiLiveModelsEntry
	revision uint64
}

type zaiLiveModelsEntry struct {
	models    []*ModelInfo
	fetchedAt time.Time
}

var zaiLiveCatalogStore = &zaiLiveModelsStore{byKeyID: make(map[string]zaiLiveModelsEntry)}

// zaiLiveModelsCacheKey identifies one plan credential by its auth ID plus a
// digest of the key itself, so a re-minted or edited key is never served the
// previous key's lanes. Only a one-way digest is stored: the store never holds
// a credential.
func zaiLiveModelsCacheKey(authID, apiKey string) string {
	key := strings.TrimSpace(apiKey)
	if key == "" {
		return ""
	}
	digest := sha256.Sum256([]byte(key))
	return strings.TrimSpace(authID) + "#" + hex.EncodeToString(digest[:])
}

// GetZaiLiveModelsCacheKey returns the discovery cache key for a Z.AI
// credential, or "" when the credential carries no key. The runtime discovery
// path lives in sdk/cliproxy, which resolves the credential from a
// coreauth.Auth rather than from a config entry, so the key derivation is
// exported for it to share.
func GetZaiLiveModelsCacheKey(authID, apiKey string) string {
	return zaiLiveModelsCacheKey(authID, apiKey)
}

// zaiLiveModelsPayload is the discovery response envelope. The error fields
// decode into the same struct as the success fields, which is the point: the
// two are distinguishable only by inspecting the body.
type zaiLiveModelsPayload struct {
	Models []zaiLiveModelEntry `json:"models"`

	Code    *int   `json:"code"`
	Msg     string `json:"msg"`
	Success *bool  `json:"success"`
}

type zaiLiveModelEntry struct {
	Slug             string `json:"slug"`
	DisplayName      string `json:"display_name"`
	Description      string `json:"description"`
	ContextWindow    int    `json:"context_window"`
	MaxContextWindow int    `json:"max_context_window"`
	// EffectiveContextWindowPercent is the share of context_window a client may
	// actually use (95 on the captured 1 MiB lanes). An absent or out-of-range
	// value means "no scaling".
	EffectiveContextWindowPercent *int                    `json:"effective_context_window_percent"`
	InputModalities               []string                `json:"input_modalities"`
	OutputModalities              []string                `json:"output_modalities"`
	SupportedReasoningLevels      []zaiLiveReasoningLevel `json:"supported_reasoning_levels"`
	SupportsReasoningSummaries    bool                    `json:"supports_reasoning_summaries"`
	SupportsParallelToolCalls     bool                    `json:"supports_parallel_tool_calls"`
	Visibility                    string                  `json:"visibility"`
	Priority                      *int                    `json:"priority"`
}

type zaiLiveReasoningLevel struct {
	Effort string `json:"effort"`
}

// ZaiLiveModelError reports a discovery response Z.AI answered with an error
// envelope. It is returned instead of an HTTP status check, because the status
// is 200 for the credential failures that matter most.
type ZaiLiveModelError struct {
	// Code is Z.AI's own error code, carried as-is.
	Code int
	// Msg is Z.AI's error message.
	Msg string
	// Status is the HTTP status the error body arrived with.
	Status int
}

func (e *ZaiLiveModelError) Error() string {
	if e == nil {
		return "zai live model discovery: failed"
	}
	msg := strings.TrimSpace(e.Msg)
	if msg == "" {
		msg = "upstream reported an error"
	}
	return fmt.Sprintf("zai live model discovery: %s (code %d, status %d)", msg, e.Code, e.Status)
}

// Credential reports whether the failure is the credential's fault rather than
// an upstream fault, so the caller can suppress discovery for a while instead
// of re-asking with a key the provider already rejected.
//
// Z.AI's own codes are matched alongside HTTP semantics because both observed
// shapes are authentication failures: 401/403 are the HTTP-mapped ones, and
// code 1000 with "Authentication Failed" is what the endpoint actually
// returns for a rejected key. Treating 1000 as a transient fault would only
// cost a longer suppression window, but matching the real code keeps the log
// honest about why discovery stopped.
func (e *ZaiLiveModelError) Credential() bool {
	if e == nil {
		return false
	}
	switch e.Code {
	case 401, 403, zaiLiveCodeAuthFailed:
		return true
	default:
		return false
	}
}

// zaiLiveCodeAuthFailed is Z.AI's own code for a rejected credential,
// observed on the discovery endpoint. It is not an HTTP status: Z.AI returns
// it inside a 200 body.
const zaiLiveCodeAuthFailed = 1000

// ErrZaiLiveModelsNoCredential reports a discovery attempt made without a
// usable credential. It is deliberately distinct from a rejected credential: a
// missing key is a configuration gap while a rejected key is a provider
// verdict, and only the latter is worth backing off from.
var ErrZaiLiveModelsNoCredential = fmt.Errorf("zai live model discovery: no api key")

// ConvertZaiLiveModelsCatalog parses a Z.AI discovery payload into registry
// model definitions.
//
// A payload is refused rather than silently accepted in three cases, because
// each one would otherwise leave the lane serving an empty or wrong catalog:
// an error envelope under a 200 status, a body that decodes to no models at
// all (which is what an error body decodes to), and a present-but-empty
// roster.
func ConvertZaiLiveModelsCatalog(data []byte) ([]*ModelInfo, error) {
	var payload zaiLiveModelsPayload
	if err := json.Unmarshal(data, &payload); err != nil {
		return nil, fmt.Errorf("zai live model discovery: decode payload: %w", err)
	}
	if err := zaiLiveModelsEnvelopeError(payload); err != nil {
		return nil, err
	}
	models := zaiLiveModelsFromEntries(payload.Models)
	if len(models) == 0 {
		return nil, fmt.Errorf("zai live model discovery: payload carries no usable models")
	}
	return models, nil
}

// zaiLiveModelsEnvelopeError reports the error envelope Z.AI returns with a
// 200 status. success is checked first because it is the field the provider
// sets explicitly, then a non-2xx code, so neither a good payload that omits
// success nor a good payload that carries a numeric code is misread.
func zaiLiveModelsEnvelopeError(payload zaiLiveModelsPayload) error {
	if payload.Success != nil && !*payload.Success {
		code := 0
		if payload.Code != nil {
			code = *payload.Code
		}
		return &ZaiLiveModelError{Code: code, Msg: payload.Msg, Status: 200}
	}
	if payload.Code != nil && (*payload.Code < 200 || *payload.Code > 299) {
		return &ZaiLiveModelError{Code: *payload.Code, Msg: payload.Msg, Status: 200}
	}
	return nil
}

func zaiLiveModelsFromEntries(entries []zaiLiveModelEntry) []*ModelInfo {
	out := make([]*ModelInfo, 0, len(entries))
	seen := make(map[string]struct{}, len(entries))
	for _, entry := range entries {
		if zaiLiveModelHidden(entry) {
			continue
		}
		info := zaiLiveModelInfo(entry)
		if info == nil {
			continue
		}
		key := strings.ToLower(info.ID)
		if _, dup := seen[key]; dup {
			continue
		}
		seen[key] = struct{}{}
		out = append(out, info)
	}
	sort.SliceStable(out, func(i, j int) bool { return out[i].ID < out[j].ID })
	return out
}

// zaiLiveModelHidden reports whether a catalog entry is explicitly withheld
// from clients. Only the known-hide vocabulary is honored: an unrecognized
// visibility value keeps the lane, because dropping a real plan lane on a
// vocabulary this fork has not seen is the worse failure.
func zaiLiveModelHidden(entry zaiLiveModelEntry) bool {
	switch strings.ToLower(strings.TrimSpace(entry.Visibility)) {
	case "hide", "hidden":
		return true
	default:
		return false
	}
}

// zaiLiveModelInfo converts one catalog entry.
//
// ContextLength carries the effective window: context_window scaled by
// effective_context_window_percent. Z.AI publishes both, and the effective
// figure is the number a client must not exceed — with the captured 1 MiB lane
// at 95 percent, the real ceiling is about 996k, and advertising 1048576 would
// let a client build a prompt the upstream truncates mid-answer. MaxContextLength
// keeps the raw max_context_window so a client catalog built from this entry
// can still report the full window alongside it.
func zaiLiveModelInfo(entry zaiLiveModelEntry) *ModelInfo {
	id := strings.TrimSpace(entry.Slug)
	if id == "" {
		return nil
	}
	displayName := strings.TrimSpace(entry.DisplayName)
	if displayName == "" {
		displayName = id
	}
	// context_window is the maximum the model has; the effective window is what
	// a client may actually use, which the provider publishes separately as a
	// percentage of it. Advertising the maximum would let a client fill the
	// context right up to the point the upstream starts truncating.
	contextLength := zaiLiveEffectiveContextWindow(entry)
	info := &ModelInfo{
		ID:                        id,
		Object:                    "model",
		OwnedBy:                   "zai",
		Type:                      "zai",
		DisplayName:               displayName,
		Description:               strings.TrimSpace(entry.Description),
		ContextLength:             contextLength,
		MaxContextLength:          entry.MaxContextWindow,
		InputTokenLimit:           contextLength,
		SupportedInputModalities:  normalizeModelsDevModalities(entry.InputModalities),
		SupportedOutputModalities: normalizeModelsDevModalities(entry.OutputModalities),
		// The provider catalog is authoritative, so both constraint sets are
		// explicit: a client catalog built from this entry publishes exactly
		// these modalities and exactly this reasoning capability, with no
		// fallback to the embedded snapshot.
		ExplicitInputModalities: true,
	}
	if info.Description == "" {
		info.Description = displayName + " via Z.AI."
	}
	if info.MaxContextLength <= 0 {
		info.MaxContextLength = info.ContextLength
	}
	if info.InputTokenLimit <= 0 {
		info.InputTokenLimit = info.MaxContextLength
	}
	if zaiLiveModelReasons(entry) {
		// The published ladder is exactly what the provider declared, and an
		// empty declaration is preserved as empty rather than back-filled.
		// The struct stays non-nil either way: internal/thinking treats a
		// non-nil Thinking as "this model reasons" and only reads Levels when
		// a level was explicitly requested. Specifically,
		// ValidateConfig's `len(support.Levels) > 0` guard means an empty
		// ladder never reaches the level-membership check, and clampBudget
		// returns the value unchanged when no budget range is declared, so a
		// toggle-only model keeps forwarding reasoning configuration instead
		// of having it stripped — while no level it might reject is ever
		// advertised.
		info.Thinking = &ThinkingSupport{Levels: zaiLiveModelLevels(entry)}
		info.ExplicitThinking = true
	}

	if entry.SupportsParallelToolCalls {
		info.SupportedParameters = append(info.SupportedParameters, "tool_choice")
	}
	return info
}

// zaiLiveEffectiveContextWindow resolves the window a client may use.
// context_window is the ceiling Z.AI enforces; effective_context_window_percent
// is the share of it that survives accounting for the request overhead the
// provider reserves. A percentage of 100 or less scales the window down, and
// an absent, non-positive, or out-of-range value leaves it untouched rather
// than guessing a ratio the provider did not state.
func zaiLiveEffectiveContextWindow(entry zaiLiveModelEntry) int {
	window := entry.ContextWindow
	if window <= 0 {
		window = entry.MaxContextWindow
	}
	if entry.EffectiveContextWindowPercent == nil {
		return window
	}
	percent := *entry.EffectiveContextWindowPercent
	if percent <= 0 || percent > 100 {
		return window
	}
	effective := window * percent / 100
	if effective <= 0 {
		return window
	}
	return effective
}

// zaiLiveModelLevels returns the declared reasoning effort levels, in declared
// order, lowercased and de-duplicated.
func zaiLiveModelLevels(entry zaiLiveModelEntry) []string {
	levels := make([]string, 0, len(entry.SupportedReasoningLevels))
	seen := make(map[string]struct{}, len(entry.SupportedReasoningLevels))
	for _, level := range entry.SupportedReasoningLevels {
		effort := strings.ToLower(strings.TrimSpace(level.Effort))
		if effort == "" {
			continue
		}
		if _, dup := seen[effort]; dup {
			continue
		}
		seen[effort] = struct{}{}
		levels = append(levels, effort)
	}
	return levels
}

// zaiLiveModelReasons reports whether the catalog marks a model as reasoning.
// The level list is not evidence either way: a reasoning model may publish no
// levels, and a non-reasoning model publishes none too. Only the two explicit
// capability booleans decide it. Z.AI states that a model supporting
// reasoning summaries also supports parallel tool calls, so entries predating
// the summaries field still carry the signal in the tool-call flag.
func zaiLiveModelReasons(entry zaiLiveModelEntry) bool {
	return entry.SupportsReasoningSummaries || entry.SupportsParallelToolCalls
}

// GetZaiLiveModels returns the lanes last discovered for one plan credential,
// or nil when nothing has been discovered for it. A credential with no
// discovery result falls through to the embedded catalog plus builtins.
func GetZaiLiveModels(keyID string) []*ModelInfo {
	zaiLiveCatalogStore.mu.RLock()
	defer zaiLiveCatalogStore.mu.RUnlock()
	entry, ok := zaiLiveCatalogStore.byKeyID[keyID]
	if !ok || len(entry.models) == 0 {
		return nil
	}
	return cloneModelInfos(entry.models)
}

// ZaiLiveModelsFetchedAt reports when a credential's lanes were last stored,
// with ok=false when nothing has been discovered for it. The runtime uses it
// to honor the discovery TTL without duplicating the freshness window.
func ZaiLiveModelsFetchedAt(keyID string) (time.Time, bool) {
	zaiLiveCatalogStore.mu.RLock()
	defer zaiLiveCatalogStore.mu.RUnlock()
	entry, ok := zaiLiveCatalogStore.byKeyID[keyID]
	if !ok || len(entry.models) == 0 {
		return time.Time{}, false
	}
	return entry.fetchedAt, true
}

// SetZaiLiveModels stores the lanes discovered for one plan credential and
// reports whether the set changed. The revision advances only on a semantic
// change, never on untracked byte churn, so re-fetching an unchanged roster
// does not re-register every Z.AI credential.
func SetZaiLiveModels(keyID string, models []*ModelInfo) bool {
	if keyID == "" || len(models) == 0 {
		return false
	}
	zaiLiveCatalogStore.mu.Lock()
	defer zaiLiveCatalogStore.mu.Unlock()
	previous, existed := zaiLiveCatalogStore.byKeyID[keyID]
	changed := !existed || modelSectionChanged(previous.models, models)
	zaiLiveCatalogStore.byKeyID[keyID] = zaiLiveModelsEntry{models: cloneModelInfos(models), fetchedAt: time.Now()}
	if changed {
		zaiLiveCatalogStore.revision++
	}
	return changed
}

// ZaiLiveModelsRevision returns the revision counter of the live Z.AI
// discovery store.
func ZaiLiveModelsRevision() uint64 {
	zaiLiveCatalogStore.mu.RLock()
	defer zaiLiveCatalogStore.mu.RUnlock()
	return zaiLiveCatalogStore.revision
}

// ZaiLiveModelsStatus reports discovery freshness for the management surfaces
// that already render one row per catalog source. Only the first live entry is
// reported: the Z.AI coding plan ships one roster for every credential, so
// extra rows would be the same lane list repeated per key, and one key's
// result must not be presented as another's.
func ZaiLiveModelsStatus() ModelsDevStatus {
	zaiLiveCatalogStore.mu.RLock()
	live := cloneModelInfos(zaiLiveCatalogStore.firstEntryModelsLocked())
	fetchedAt := zaiLiveCatalogStore.firstEntryFetchedAtLocked()
	zaiLiveCatalogStore.mu.RUnlock()

	return ModelsDevStatus{
		Providers: []ModelsDevProviderStatus{
			describeCatalogProviderStatus("zai-coding-plan", live, WithZaiBuiltins(cloneModelInfos(getModels().ZAI)), fetchedAt),
		},
	}
}

func (s *zaiLiveModelsStore) firstEntryModelsLocked() []*ModelInfo {
	for _, entry := range s.byKeyID {
		if len(entry.models) > 0 {
			return entry.models
		}
	}
	return nil
}

func (s *zaiLiveModelsStore) firstEntryFetchedAtLocked() time.Time {
	for _, entry := range s.byKeyID {
		if len(entry.models) > 0 {
			return entry.fetchedAt
		}
	}
	return time.Time{}
}

// resetZaiLiveModelsForTest clears the discovery store. Tests only.
func resetZaiLiveModelsForTest() {
	zaiLiveCatalogStore.mu.Lock()
	defer zaiLiveCatalogStore.mu.Unlock()
	zaiLiveCatalogStore.byKeyID = make(map[string]zaiLiveModelsEntry)
	zaiLiveCatalogStore.revision = 0
}
