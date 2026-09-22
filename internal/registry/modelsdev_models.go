package registry

import (
	"bytes"
	"fmt"
	"strings"
	"sync"

	log "github.com/sirupsen/logrus"
)

// modelsdevLiveStore holds the last successfully converted models.dev
// sections. It is an overlay: GetOpencodeModels/GetZaiModels prefer it and
// fall back to the embedded catalog plus builtins when it is empty.
type modelsdevLiveStore struct {
	mu       sync.RWMutex
	opencode []*ModelInfo
	zai      []*ModelInfo
	rawJSON  []byte
	revision uint64
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
	modelsdevCatalogStore.rawJSON = nil
	modelsdevCatalogStore.revision = 0
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
