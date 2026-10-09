package registry

import (
	"testing"
)

// The periodic models.dev refresh must keep running unless --local-model pins
// the embedded catalogs. Upstream's catalog-sources switchboard removed the
// cmd/server helper that used to own this policy, so the gate below is what
// Service.Run consults before starting the ticker.
func TestModelsDevRefreshEnabledHonorsLocalMode(t *testing.T) {
	defer SetLocalModelCatalogs(false)

	SetLocalModelCatalogs(false)
	if !ModelsDevRefreshEnabled() {
		t.Fatal("ModelsDevRefreshEnabled() = false without --local-model, ticker would never start")
	}

	SetLocalModelCatalogs(true)
	if ModelsDevRefreshEnabled() {
		t.Fatal("ModelsDevRefreshEnabled() = true under --local-model, ticker would fetch despite pinned catalogs")
	}
}
