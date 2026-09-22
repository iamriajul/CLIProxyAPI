package registry

import (
	"bytes"
	"fmt"
	"strings"
	"sync"
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
// ("opencode" and/or "zai").
func loadModelsDevLiveFromBytes(data []byte, source string) ([]string, error) {
	opencode, zai, err := ConvertModelsDevCatalog(data)
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
	if modelSectionChanged(modelsdevCatalogStore.opencode, opencode) {
		changed = append(changed, "opencode")
	}
	if modelSectionChanged(modelsdevCatalogStore.zai, zai) {
		changed = append(changed, "zai")
	}
	modelsdevCatalogStore.opencode = opencode
	modelsdevCatalogStore.zai = zai
	modelsdevCatalogStore.rawJSON = clonedData
	modelsdevCatalogStore.revision++
	return changed, nil
}

// resetModelsDevLiveForTest clears the live overlay. Tests only.
func resetModelsDevLiveForTest() {
	modelsdevCatalogStore.mu.Lock()
	defer modelsdevCatalogStore.mu.Unlock()
	modelsdevCatalogStore.opencode = nil
	modelsdevCatalogStore.zai = nil
	modelsdevCatalogStore.rawJSON = nil
}
