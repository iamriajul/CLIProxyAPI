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
	if got := GetZaiModels(); len(got) != 2 {
		t.Fatalf("zai after refresh = %d, want 2", len(got))
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
	if len(GetModelsDevLive("opencode")) != 0 || len(GetModelsDevLive("zai")) != 0 {
		t.Fatal("garbage payload must not populate live store")
	}
}
