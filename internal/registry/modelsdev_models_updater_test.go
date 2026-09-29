package registry

import (
	"context"
	"net/http"
	"net/http/httptest"
	"os"
	"testing"
)

func TestTryRefreshModelsDevFromHTTP(t *testing.T) {
	resetModelsDevLiveForTest()
	t.Cleanup(resetModelsDevLiveForTest)

	fixture, err := os.ReadFile("modelsdev_testdata_api.json")
	if err != nil {
		t.Fatalf("read fixture: %v", err)
	}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write(fixture)
	}))
	t.Cleanup(server.Close)

	previous := modelsdevURLs
	modelsdevURLs = []string{server.URL}
	t.Cleanup(func() { modelsdevURLs = previous })

	tryRefreshModelsDev(context.Background(), "test refresh")
	if got := GetOpencodeModels(); len(got) != 2 {
		t.Fatalf("opencode after refresh = %d, want 2", len(got))
	}
	// The Z.AI lane is untouched by a models.dev refresh; it is discovered from
	// Z.AI's own plan-scoped catalog, keyed by credential.
	zaiOffline := len(GetZaiModels())
	tryRefreshModelsDev(context.Background(), "test refresh")
	if got := len(GetZaiModels()); got != zaiOffline {
		t.Fatalf("zai after refresh = %d, want the unchanged offline %d", got, zaiOffline)
	}

	// A failing endpoint keeps current data.
	modelsdevURLs = []string{server.URL + "/missing"}
	tryRefreshModelsDev(context.Background(), "test refresh failure")
	if got := GetOpencodeModels(); len(got) != 2 {
		t.Fatalf("opencode after failed refresh = %d, want 2 kept", len(got))
	}
}

func TestTryRefreshModelsDevRejectsGarbage(t *testing.T) {
	resetModelsDevLiveForTest()
	t.Cleanup(resetModelsDevLiveForTest)

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"unrelated": true}`))
	}))
	t.Cleanup(server.Close)

	previous := modelsdevURLs
	modelsdevURLs = []string{server.URL}
	t.Cleanup(func() { modelsdevURLs = previous })

	tryRefreshModelsDev(context.Background(), "test refresh garbage")
	if len(GetModelsDevLive("opencode")) != 0 || GetModelsDevLive("zai") != nil {
		t.Fatal("garbage payload must not populate live store")
	}
}
