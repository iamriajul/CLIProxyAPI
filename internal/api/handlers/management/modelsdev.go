package management

import (
	"net/http"

	"github.com/gin-gonic/gin"

	"github.com/router-for-me/CLIProxyAPI/v8/internal/registry"
)

// GetModelsDevStatus reports catalog freshness for the models.dev
// opencode-go section, the one provider whose models genuinely come from the
// models.dev catalogue.
//
// Z.AI is deliberately absent. Its lanes are discovered from Z.AI's own
// plan-scoped catalog, so it has no models.dev dependency to report on, and
// listing it here would put a row in a models.dev freshness surface for a
// provider that does not use one. It is also the only lane on a different
// axis — per-credential rather than one catalogue section — so its count
// could not be compared with a single overlay row. Like claude, antigravity,
// gemini, kimi, meta and xai, Z.AI serves from its own source and is not
// listed here.
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
