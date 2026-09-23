package management

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/gin-gonic/gin"

	"github.com/router-for-me/CLIProxyAPI/v7/internal/config"
	coreauth "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/auth"
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
	if len(payload.Providers) != 2 || payload.Providers[0].ID != "opencode-go" || payload.Providers[1].ID != "zai-coding-plan" {
		t.Fatalf("providers = %+v", payload.Providers)
	}
	for _, p := range payload.Providers {
		if p.Source != "live" && p.Source != "fallback" {
			t.Fatalf("%s source = %q", p.ID, p.Source)
		}
	}
}
