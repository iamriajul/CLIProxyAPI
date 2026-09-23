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
type modelsdevLiveStore struct {
	mu              sync.RWMutex
	opencode        []*ModelInfo
	zai             []*ModelInfo
	opencodeFetched time.Time
	zaiFetched      time.Time
	lastError       string
	rawJSON         []byte
	revision        uint64
}

var modelsdevCatalogStore = &modelsdevLiveStore{}

// GetModelsDevLive returns a clone of the live models.dev section for
// provider ("opencode" or "zai"), or nil when no live data is loaded.
func GetModelsDevLive(provider string) []*ModelInfo {
	modelsdevCatalogStore.mu.RLock()
	defer modelsdevCatalogStore.mu.RUnlock()
	switch strings.ToLower(strings.TrimSpace(provider)) {
	case "opencode", "opencode-go", "opencode_go":
		return cloneModelInfos(modelsdevCatalogStore.opencode)
	case "zai", "glm", "zhipu":
		return cloneModelInfos(modelsdevCatalogStore.zai)
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
// sections. It returns the provider names whose sections changed
// ("opencode" and/or "zai"). Update contract: only sections whose provider
// key is present AND non-empty are stored — a missing or empty section keeps
// the previous live data, so an upstream glitch can never wipe last-good
// state or fire a spurious refresh. revision advances only on semantic
// change, never on untracked byte churn.
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
	if sections.HasZai && len(sections.Zai) > 0 {
		if modelSectionChanged(modelsdevCatalogStore.zai, sections.Zai) {
			modelsdevCatalogStore.zai = sections.Zai
			changed = append(changed, "zai")
		}
	} else if sections.HasZai {
		log.Warnf("%s: zai-coding-plan section empty, keeping previous live data", source)
	}
	modelsdevCatalogStore.rawJSON = clonedData
	if len(changed) > 0 {
		now := time.Now().UTC()
		for _, provider := range changed {
			switch provider {
			case "opencode":
				modelsdevCatalogStore.opencodeFetched = now
			case "zai":
				modelsdevCatalogStore.zaiFetched = now
			}
		}
		modelsdevCatalogStore.revision++
	}
	return changed, nil
}

// resetModelsDevLiveForTest clears the live overlay. Tests only.
func resetModelsDevLiveForTest() {
	modelsdevCatalogStore.mu.Lock()
	defer modelsdevCatalogStore.mu.Unlock()
	modelsdevCatalogStore.opencode = nil
	modelsdevCatalogStore.zai = nil
	modelsdevCatalogStore.opencodeFetched = time.Time{}
	modelsdevCatalogStore.zaiFetched = time.Time{}
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
func GetModelsDevStatus() ModelsDevStatus {
	modelsdevCatalogStore.mu.RLock()
	liveOpencode := cloneModelInfos(modelsdevCatalogStore.opencode)
	liveZai := cloneModelInfos(modelsdevCatalogStore.zai)
	opencodeFetched := modelsdevCatalogStore.opencodeFetched
	zaiFetched := modelsdevCatalogStore.zaiFetched
	lastError := modelsdevCatalogStore.lastError
	modelsdevCatalogStore.mu.RUnlock()

	describe := func(id string, live []*ModelInfo, fallback []*ModelInfo, fetched time.Time) ModelsDevProviderStatus {
		status := ModelsDevProviderStatus{ID: id, Source: "fallback", Models: len(fallback)}
		if len(live) > 0 {
			status.Source = "live"
			status.Models = len(live)
			fetchedCopy := fetched.UTC()
			if !fetched.IsZero() {
				status.FetchedAt = &fetchedCopy
			}
		}
		if lastError != "" {
			lastErrorCopy := lastError
			status.LastError = &lastErrorCopy
		}
		return status
	}
	return ModelsDevStatus{Providers: []ModelsDevProviderStatus{
		describe("opencode-go", liveOpencode, WithOpencodeBuiltins(cloneModelInfos(getModels().Opencode)), opencodeFetched),
		describe("zai-coding-plan", liveZai, WithZaiBuiltins(cloneModelInfos(getModels().ZAI)), zaiFetched),
	}}
}

// markModelsDevExplicit restores the converter's Explicit flags on
// models.dev-derived entries whose flags were lost in JSON serialization
// (Explicit* is json:"-"). The opencode/zai embedded sections and builtins
// are 100% converter output, so every entry gets ExplicitInputModalities and
// thinking entries get ExplicitThinking — matching the live overlay exactly.
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
