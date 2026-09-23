package management

import (
	"net/http"

	"github.com/gin-gonic/gin"

	"github.com/router-for-me/CLIProxyAPI/v7/internal/registry"
)

// GetModelsDevStatus reports models.dev catalog freshness per tracked
// provider (opencode-go, zai-coding-plan): live overlay or embedded
// fallback, model counts, last fetch time, and the latest refresh error.
func (h *Handler) GetModelsDevStatus(c *gin.Context) {
	c.JSON(http.StatusOK, registry.GetModelsDevStatus())
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
