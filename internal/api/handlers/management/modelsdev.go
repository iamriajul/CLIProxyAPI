package management

import (
	"net/http"

	"github.com/gin-gonic/gin"

	"github.com/router-for-me/CLIProxyAPI/v8/internal/registry"
)

// GetModelsDevStatus reports catalog freshness for both live model sources:
// the models.dev opencode-go section and the Z.AI plan catalog discovered from
// Z.AI itself. Each row is live overlay or embedded fallback, with model
// counts, last fetch time, and the latest refresh error.
//
// The Z.AI row carries no last_error: discovery is per credential, so a
// single "latest error" would describe one plan key while the row's count
// describes the last successful one. The fallback source is the honest signal
// there — it already reads as "no live lanes for this deployment".
func (h *Handler) GetModelsDevStatus(c *gin.Context) {
	status := registry.GetModelsDevStatus()
	status.Providers = append(status.Providers, registry.ZaiLiveModelsStatus().Providers...)
	c.JSON(http.StatusOK, status)
}

// RefreshModelsDev triggers one models.dev fetch outside the 3-hour ticker
// and returns the changed provider names (empty when already current).
func (h *Handler) RefreshModelsDev(c *gin.Context) {
	changed, err := registry.TriggerModelsDevRefresh(c.Request.Context())
	if err != nil {
		c.JSON(http.StatusBadGateway, gin.H{"error": err.Error(), "changed": []string{}})
		return
	}
	if changed == nil {
		changed = []string{}
	}
	c.JSON(http.StatusOK, gin.H{"changed": changed})
}
