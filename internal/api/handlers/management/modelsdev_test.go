package management

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/gin-gonic/gin"

	"github.com/router-for-me/CLIProxyAPI/v8/internal/config"
	coreauth "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/auth"
)

func TestGetModelsDevStatus_Shape(t *testing.T) {
	h := NewHandlerWithoutConfigFilePath(&config.Config{AuthDir: t.TempDir()}, coreauth.NewManager(nil, nil, nil))

	rec := httptest.NewRecorder()
	ctx, _ := gin.CreateTestContext(rec)
	ctx.Request = httptest.NewRequest(http.MethodGet, "/v0/management/modelsdev/status", nil)
	h.GetModelsDevStatus(ctx)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected status %d, got %d with body %s", http.StatusOK, rec.Code, rec.Body.String())
	}
	var payload struct {
		Providers []struct {
			ID     string `json:"id"`
			Source string `json:"source"`
			Models int    `json:"models"`
		} `json:"providers"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &payload); err != nil {
		t.Fatalf("decode status: %v", err)
	}
	// Only opencode-go belongs in a models.dev freshness payload: it is the one
	// provider whose lanes come from the models.dev catalogue. Z.AI is
	// discovered from Z.AI itself, and claude, antigravity, gemini, kimi, meta
	// and xai all serve from their own sources, so none of them are listed.
	if len(payload.Providers) != 1 || payload.Providers[0].ID != "opencode-go" {
		t.Fatalf("providers = %+v, want only opencode-go", payload.Providers)
	}
	if p := payload.Providers[0]; p.Source != "live" && p.Source != "fallback" {
		t.Fatalf("%s source = %q", p.ID, p.Source)
	}
}
