package quota

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestDoJSONNilClient(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"ok":true}`))
	}))
	defer server.Close()

	var out struct {
		OK bool `json:"ok"`
	}
	// Must not panic; falls back to http.DefaultClient.
	if err := DoJSON(context.Background(), nil, http.MethodGet, server.URL, nil, nil, &out); err != nil {
		t.Fatalf("DoJSON: %v", err)
	}
	if !out.OK {
		t.Fatalf("out = %+v", out)
	}
}
