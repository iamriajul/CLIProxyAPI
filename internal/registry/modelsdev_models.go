package registry

import (
	"bytes"
	"fmt"
	"strings"
	"sync"
	"time"

	log "github.com/sirupsen/logrus"
)

// modelsdevLiveStore holds the last successfully converted models.dev
// opencode-go section. The Z.AI section is not tracked here: that lane is
// discovered from Z.AI's own plan-scoped catalog (zai_live_models.go).
type modelsdevLiveStore struct {
	mu              sync.RWMutex
	opencode        []*ModelInfo
	opencodeFetched time.Time
	lastError       string
	rawJSON         []byte
	revision        uint64
}

var modelsdevCatalogStore = &modelsdevLiveStore{}

// GetModelsDevLive returns a clone of the live models.dev opencode-go section,
// or nil when no live data is loaded. The Z.AI lane has no models.dev live
// section: use GetZaiModels / GetZaiLiveModels for it.
func GetModelsDevLive(provider string) []*ModelInfo {
	modelsdevCatalogStore.mu.RLock()
	defer modelsdevCatalogStore.mu.RUnlock()
	switch strings.ToLower(strings.TrimSpace(provider)) {
	case "opencode", "opencode-go", "opencode_go":
		return cloneModelInfos(modelsdevCatalogStore.opencode)
	default:
		return nil
	}
}

// GetModelsDevRevision returns the revision counter of the live overlay.
func GetModelsDevRevision() uint64 {
	modelsdevCatalogStore.mu.RLock()
	defer modelsdevCatalogStore.mu.RUnlock()
	return modelsdevCatalogStore.revision
}

// loadModelsDevLiveFromBytes converts api.json bytes and stores the live
// opencode-go section. It returns the provider names whose sections changed.
// Update contract: only a section whose provider key is present AND non-empty
// is stored — a missing or empty section keeps the previous live data, so an
// upstream glitch can never wipe last-good state or fire a spurious refresh.
// revision advances only on semantic change, never on untracked byte churn.
func loadModelsDevLiveFromBytes(data []byte, source string) ([]string, error) {
	sections, err := ConvertModelsDevCatalog(data)
	if err != nil {
		return nil, fmt.Errorf("%s: %w", source, err)
	}

	clonedData := append([]byte(nil), data...)
	modelsdevCatalogStore.mu.Lock()
	defer modelsdevCatalogStore.mu.Unlock()
	if bytes.Equal(modelsdevCatalogStore.rawJSON, clonedData) {
		return nil, nil
	}
	var changed []string
	if sections.HasOpencode && len(sections.Opencode) > 0 {
		if modelSectionChanged(modelsdevCatalogStore.opencode, sections.Opencode) {
			modelsdevCatalogStore.opencode = sections.Opencode
			changed = append(changed, "opencode")
		}
	} else if sections.HasOpencode {
		log.Warnf("%s: opencode-go section empty, keeping previous live data", source)
	}
	modelsdevCatalogStore.rawJSON = clonedData
	if len(changed) > 0 {
		modelsdevCatalogStore.opencodeFetched = time.Now().UTC()
		modelsdevCatalogStore.revision++
	}
	return changed, nil
}

// resetModelsDevLiveForTest clears the live overlay. Tests only.
func resetModelsDevLiveForTest() {
	modelsdevCatalogStore.mu.Lock()
	defer modelsdevCatalogStore.mu.Unlock()
	modelsdevCatalogStore.opencode = nil
	modelsdevCatalogStore.opencodeFetched = time.Time{}
	modelsdevCatalogStore.lastError = ""
	modelsdevCatalogStore.rawJSON = nil
	modelsdevCatalogStore.revision = 0
}

// setModelsDevLastError records the latest refresh failure (empty clears).
func setModelsDevLastError(msg string) {
	modelsdevCatalogStore.mu.Lock()
	defer modelsdevCatalogStore.mu.Unlock()
	modelsdevCatalogStore.lastError = msg
}

// ModelsDevProviderStatus describes one tracked models.dev section for
// management surfaces (TUI card, CPAMC badge). FetchedAt/LastError are nil
// when never fetched / healthy, so JSON renders null instead of omitting.
type ModelsDevProviderStatus struct {
	ID        string     `json:"id"`
	Source    string     `json:"source"`
	Models    int        `json:"models"`
	FetchedAt *time.Time `json:"fetched_at"`
	LastError *string    `json:"last_error"`
}

// ModelsDevStatus is the management payload for the catalog freshness UI.
type ModelsDevStatus struct {
	Providers []ModelsDevProviderStatus `json:"providers"`
}

// GetModelsDevStatus snapshots per-provider freshness: live when the overlay
// holds the section, fallback otherwise (counts follow the same getters the
// harness reads, so the UI can never disagree with serving state).
//
// Only opencode-go is reported. The Z.AI lane is discovered from the provider
// itself, one entry per plan credential, and so is absent from a models.dev
// freshness payload entirely — as are claude, antigravity, gemini, kimi, meta
// and xai, each of which serves from its own source.
func GetModelsDevStatus() ModelsDevStatus {
	modelsdevCatalogStore.mu.RLock()
	liveOpencode := cloneModelInfos(modelsdevCatalogStore.opencode)
	opencodeFetched := modelsdevCatalogStore.opencodeFetched
	lastError := modelsdevCatalogStore.lastError
	modelsdevCatalogStore.mu.RUnlock()

	status := ModelsDevStatus{
		Providers: []ModelsDevProviderStatus{
			describeCatalogProviderStatus("opencode-go", liveOpencode, WithOpencodeBuiltins(cloneModelInfos(getModels().Opencode)), opencodeFetched),
		},
	}
	if lastError != "" {
		status.Providers[0].LastError = &lastError
	}
	return status
}

// describeCatalogProviderStatus builds one freshness row: live when the source
// holds entries, fallback otherwise, with the fetch time attached only to a
// live row. Shared by the models.dev overlay and the Z.AI live discovery
// status so both render through the same payload and the same TUI card.
func describeCatalogProviderStatus(id string, live, fallback []*ModelInfo, fetchedAt time.Time) ModelsDevProviderStatus {
	status := ModelsDevProviderStatus{ID: id, Source: "fallback", Models: len(fallback)}
	if len(live) > 0 {
		status.Source = "live"
		status.Models = len(live)
		if !fetchedAt.IsZero() {
			fetched := fetchedAt.UTC()
			status.FetchedAt = &fetched
		}
	}
	return status
}

// GetZaiModels returns Z.AI GLM model definitions for the offline snapshot:
// the embedded models.dev zai-coding-plan section plus builtins. Callers that
// hold a credential should use GetZaiModelsForCredential, which prefers that
// credential's discovered lanes.
func GetZaiModels() []*ModelInfo {
	return GetZaiModelsForCredential("", "")
}

// GetZaiModelsForCredential is GetZaiModels for one plan credential, which is
// the shape the Z.AI lane needs: Z.AI's catalog is plan-scoped, so two
// credentials on one deployment may legitimately serve different lanes.
//
// Precedence: the lanes discovered for this credential win, then the embedded
// snapshot plus builtins. A credential with no live result — never discovered,
// currently failing, or refreshing with a key the provider rejected — reads
// the offline catalog, so a discovery fault degrades the lane instead of
// emptying it. An empty credential has no discovery result by construction and
// always reads the offline catalog, which is what the static channel lookups
// and the management surfaces use.
func GetZaiModelsForCredential(authID, apiKey string) []*ModelInfo {
	if keyID := zaiLiveModelsCacheKey(authID, apiKey); keyID != "" {
		if live := GetZaiLiveModels(keyID); len(live) > 0 {
			return live
		}
	}
	return markModelsDevExplicit(WithZaiBuiltins(cloneModelInfos(getModels().ZAI)))
}

// markModelsDevExplicit restores the converter's Explicit flags on
// models.dev-derived entries whose flags were lost in JSON serialization
// (Explicit* is json:"-"). The opencode/zai embedded sections and builtins
// are 100% converter output, so every entry gets ExplicitInputModalities and
// thinking entries get ExplicitThinking — matching what the live sources
// publish, so a lane constrains harnesses identically whichever source it
// came from.
func markModelsDevExplicit(models []*ModelInfo) []*ModelInfo {
	for _, m := range models {
		if m == nil {
			continue
		}
		m.ExplicitInputModalities = true
		if m.Thinking != nil {
			m.ExplicitThinking = true
		}
	}
	return models
}
