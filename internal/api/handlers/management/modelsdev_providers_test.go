package management

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/gin-gonic/gin"

	"github.com/router-for-me/CLIProxyAPI/v8/internal/config"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/registry"
	coreauth "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/auth"
)

func seedModelsDevCatalogForHandlerTest(t *testing.T) {
	t.Helper()
	if err := registry.SeedModelsDevCustomProvidersForTest([]byte(`{
		"alpha": {"id": "alpha", "name": "Alpha AI", "api": "https://api.alpha.test/v1",
			"models": {"solo": {"id": "solo", "reasoning": false, "limit": {"context": 1000, "output": 100}}}},
		"beta": {"id": "beta", "name": "Beta AI", "api": "https://api.beta.test/v1",
			"models": {"duo": {"id": "duo", "reasoning": false, "limit": {"context": 2000, "output": 200}}}},
		"gamma": {"id": "gamma", "name": "Gamma AI", "api": "https://api.gamma.test/v1",
			"models": {"duo": {"id": "duo", "reasoning": false, "limit": {"context": 3000, "output": 300}}}}
	}`)); err != nil {
		t.Fatalf("seed catalog: %v", err)
	}
	t.Cleanup(registry.ResetModelsDevCustomProvidersForTest)
}

type modelsDevProvidersResponse struct {
	Model       string                           `json:"model"`
	Unambiguous bool                             `json:"unambiguous"`
	Providers   []registry.ModelsDevProviderInfo `json:"providers"`
}

func getModelsDevProviders(t *testing.T, target string) (*httptest.ResponseRecorder, modelsDevProvidersResponse) {
	t.Helper()
	h := NewHandlerWithoutConfigFilePath(&config.Config{AuthDir: t.TempDir()}, coreauth.NewManager(nil, nil, nil))
	rec := httptest.NewRecorder()
	ctx, _ := gin.CreateTestContext(rec)
	ctx.Request = httptest.NewRequest(http.MethodGet, target, nil)
	h.GetModelsDevProviders(ctx)
	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d with body %s", rec.Code, rec.Body.String())
	}
	var payload modelsDevProvidersResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &payload); err != nil {
		t.Fatalf("decode providers: %v", err)
	}
	return rec, payload
}

func TestGetModelsDevProviders_AmbiguousModel(t *testing.T) {
	seedModelsDevCatalogForHandlerTest(t)
	_, payload := getModelsDevProviders(t, "/v0/management/modelsdev/providers?model=duo")
	if payload.Unambiguous {
		t.Fatal("a model with two providers must not report unambiguous")
	}
	if len(payload.Providers) != 2 {
		t.Fatalf("providers = %d, want 2", len(payload.Providers))
	}
	if payload.Providers[0].ID != "beta" || payload.Providers[1].ID != "gamma" {
		t.Fatalf("providers = %+v, want beta then gamma", payload.Providers)
	}
}

func TestGetModelsDevProviders_SingleProviderIsUnambiguous(t *testing.T) {
	seedModelsDevCatalogForHandlerTest(t)
	_, payload := getModelsDevProviders(t, "/v0/management/modelsdev/providers?model=solo")
	if !payload.Unambiguous {
		t.Fatal("a model with one provider must report unambiguous so the client selects it automatically")
	}
	if len(payload.Providers) != 1 || payload.Providers[0].ID != "alpha" {
		t.Fatalf("providers = %+v, want just alpha", payload.Providers)
	}
}

func TestGetModelsDevProviders_BaseURLRanksFirst(t *testing.T) {
	seedModelsDevCatalogForHandlerTest(t)
	_, payload := getModelsDevProviders(t,
		"/v0/management/modelsdev/providers?model=duo&base_url=https%3A%2F%2Fapi.gamma.test%2Fv1")
	if len(payload.Providers) != 2 {
		t.Fatalf("providers = %d, want 2", len(payload.Providers))
	}
	if payload.Providers[0].ID != "gamma" {
		t.Fatalf("base-url hint must rank its provider first, got %q", payload.Providers[0].ID)
	}
}

func TestGetModelsDevProviders_UnknownModelIsEmptyNotError(t *testing.T) {
	seedModelsDevCatalogForHandlerTest(t)
	rec, payload := getModelsDevProviders(t, "/v0/management/modelsdev/providers?model=not-a-real-model")
	if payload.Unambiguous {
		t.Fatal("an unknown model must not report unambiguous")
	}
	if len(payload.Providers) != 0 {
		t.Fatalf("providers = %+v, want empty", payload.Providers)
	}
	// The list must serialize as [] rather than null so a client can iterate.
	if !bytes.Contains(rec.Body.Bytes(), []byte(`"providers":[]`)) {
		t.Fatalf("empty provider list must serialize as [], got %s", rec.Body.String())
	}
}

func TestGetModelsDevProviders_RequiresModel(t *testing.T) {
	h := NewHandlerWithoutConfigFilePath(&config.Config{AuthDir: t.TempDir()}, coreauth.NewManager(nil, nil, nil))
	rec := httptest.NewRecorder()
	ctx, _ := gin.CreateTestContext(rec)
	ctx.Request = httptest.NewRequest(http.MethodGet, "/v0/management/modelsdev/providers", nil)
	h.GetModelsDevProviders(ctx)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("expected 400 for a missing model, got %d", rec.Code)
	}
}
