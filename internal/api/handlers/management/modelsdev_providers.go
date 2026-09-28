package management

import (
	"net/http"
	"strings"

	"github.com/gin-gonic/gin"

	"github.com/router-for-me/CLIProxyAPI/v8/internal/registry"
)

// GetModelsDevProviders lists the models.dev providers publishing a model, so a
// management client can offer a provider picker for a custom provider entry.
//
// It accepts model as a query parameter and base_url as an optional hint used
// only for ordering: the provider reachable at that endpoint is returned first,
// because it is almost always the one the operator is actually pointed at.
//
// An empty list is a normal answer meaning "no provider publishes this model",
// not an error, so a client can leave the field unset instead of forcing a
// choice. A single entry is unambiguous and may be applied directly.
func (h *Handler) GetModelsDevProviders(c *gin.Context) {
	model := strings.TrimSpace(c.Query("model"))
	if model == "" {
		c.JSON(http.StatusBadRequest, gin.H{"error": "model is required"})
		return
	}
	baseURL := strings.TrimSpace(c.Query("base_url"))
	providers := registry.GetModelsDevProvidersForModel(model, baseURL)
	if providers == nil {
		providers = []registry.ModelsDevProviderInfo{}
	}
	c.JSON(http.StatusOK, gin.H{
		"model": model,
		// Providers that publish the model. Empty means the catalog has no
		// entry, so the client must keep the configuration as-is.
		"providers": providers,
		// Unambiguous reports whether selection is unnecessary. A single
		// provider is chosen automatically; several require the operator.
		"unambiguous": len(providers) == 1,
	})
}
