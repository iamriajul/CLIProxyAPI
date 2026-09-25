package quota

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	devinauth "github.com/router-for-me/CLIProxyAPI/v7/internal/auth/devin"
	coreauth "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/auth"
	"google.golang.org/protobuf/encoding/protowire"
)

func mockDevinStatusBytes() []byte {
	var planInfo []byte
	planInfo = protowire.AppendTag(planInfo, 2, protowire.BytesType)
	planInfo = protowire.AppendString(planInfo, "Pro")
	var planStatus []byte
	planStatus = protowire.AppendTag(planStatus, 1, protowire.BytesType)
	planStatus = protowire.AppendBytes(planStatus, planInfo)
	planStatus = protowire.AppendTag(planStatus, 14, protowire.VarintType)
	planStatus = protowire.AppendVarint(planStatus, 100)
	planStatus = protowire.AppendTag(planStatus, 15, protowire.VarintType)
	planStatus = protowire.AppendVarint(planStatus, 50)
	planStatus = protowire.AppendTag(planStatus, 17, protowire.VarintType)
	planStatus = protowire.AppendVarint(planStatus, 1789200000)
	planStatus = protowire.AppendTag(planStatus, 18, protowire.VarintType)
	planStatus = protowire.AppendVarint(planStatus, 1789286400)
	var userStatus []byte
	userStatus = protowire.AppendTag(userStatus, 13, protowire.BytesType)
	userStatus = protowire.AppendBytes(userStatus, planStatus)
	var resp []byte
	resp = protowire.AppendTag(resp, 1, protowire.BytesType)
	resp = protowire.AppendBytes(resp, userStatus)
	return resp
}

func TestDevinFetcher(t *testing.T) {
	payload := mockDevinStatusBytes()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != devinauth.DevinGetUserStatusPath {
			http.NotFound(w, r)
			return
		}
		_, _ = w.Write(payload)
	}))
	defer server.Close()

	fetcher := NewDevinFetcher()
	if fetcher.Provider() != "devin" {
		t.Fatalf("provider = %q", fetcher.Provider())
	}
	auth := &coreauth.Auth{
		Provider:   "devin",
		Attributes: map[string]string{"session_token": "devin-session-token$abc", "base_url": server.URL},
	}
	snapshot, err := fetcher.Fetch(context.Background(), FetchRequest{Auth: auth, Client: server.Client()})
	if err != nil {
		t.Fatalf("Fetch: %v", err)
	}
	if snapshot == nil || snapshot.Plan != "Pro" || len(snapshot.Windows) != 2 {
		t.Fatalf("snapshot = %+v", snapshot)
	}
	if snapshot.Windows[0].Name != "daily" || *snapshot.Windows[0].UsedPercent != 0 {
		t.Fatalf("daily = %+v", snapshot.Windows[0])
	}
	if snapshot.Windows[1].Name != "weekly" || *snapshot.Windows[1].UsedPercent != 50 {
		t.Fatalf("weekly = %+v", snapshot.Windows[1])
	}
	if !snapshot.Windows[0].ResetAt.Equal(time.Unix(1789200000, 0).UTC()) {
		t.Fatalf("daily reset = %v", snapshot.Windows[0].ResetAt)
	}

	if _, err := fetcher.Fetch(context.Background(), FetchRequest{Auth: &coreauth.Auth{}}); err == nil {
		t.Fatal("expected missing-token error")
	}
}
