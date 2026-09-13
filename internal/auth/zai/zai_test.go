package zai

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestAuthorizeURLShape(t *testing.T) {
	got := AuthorizeURL("state-123", ZaiCallbackRedirectURI)
	if !strings.HasPrefix(got, ZaiAuthorizeURL+"?") {
		t.Fatalf("url = %q", got)
	}
	for _, want := range []string{
		"client_id=" + ZaiClientID,
		"response_type=code",
		"redirect_uri=" + "zcode%3A%2F%2Fzai-auth%2Fcallback",
		"state=state-123",
	} {
		if !strings.Contains(got, want) {
			t.Fatalf("url missing %q: %s", want, got)
		}
	}
	if strings.Contains(got, "code_challenge") {
		t.Fatalf("zai uses no PKCE, url = %q", got)
	}
}

func TestParsePastedCallback(t *testing.T) {
	code, state, err := ParsePastedCallback("zcode://zai-auth/callback?code=abc&state=s1", "s1")
	if err != nil || code != "abc" || state != "s1" {
		t.Fatalf("got %q %q %v", code, state, err)
	}
	code, state, err = ParsePastedCallback("bare-code-xyz", "s1")
	if err != nil || code != "bare-code-xyz" || state != "s1" {
		t.Fatalf("bare code: got %q %q %v", code, state, err)
	}
	code, state, err = ParsePastedCallback("qrs#s1", "s1")
	if err != nil || code != "qrs" || state != "s1" {
		t.Fatalf("fragment: got %q %q %v", code, state, err)
	}
	if _, _, err = ParsePastedCallback("qrs#s1", "other"); err == nil {
		t.Fatalf("expected state mismatch error")
	}
	if _, _, err = ParsePastedCallback("zcode://zai-auth/callback?code=abc&state=evil", "s1"); err == nil {
		t.Fatalf("expected state mismatch error")
	}
	if _, _, err = ParsePastedCallback("", "s1"); err == nil {
		t.Fatalf("expected empty input error")
	}
}

func overrideEndpoints(t *testing.T, token, bizLogin, apiBase string) {
	t.Helper()
	oldToken, oldLogin, oldBase := zaiTokenEndpoint, zaiBusinessLoginEndpoint, zaiAPIBaseEndpoint
	zaiTokenEndpoint, zaiBusinessLoginEndpoint, zaiAPIBaseEndpoint = token, bizLogin, apiBase
	t.Cleanup(func() {
		zaiTokenEndpoint, zaiBusinessLoginEndpoint, zaiAPIBaseEndpoint = oldToken, oldLogin, oldBase
	})
}

func TestExchangeCode(t *testing.T) {
	var gotBody map[string]string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewDecoder(r.Body).Decode(&gotBody)
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{
			"code": 0,
			"data": map[string]any{
				"zai":  map[string]any{"access_token": "oauth-short"},
				"user": map[string]any{"email": "Z@Example.com", "id": "u-1"},
			},
		})
	}))
	defer server.Close()
	overrideEndpoints(t, server.URL, server.URL, server.URL)

	tokenData, err := NewZaiAuth(nil).ExchangeCode(context.Background(), "auth-code", "s1", ZaiCallbackRedirectURI)
	if err != nil {
		t.Fatalf("ExchangeCode err = %v", err)
	}
	if gotBody["provider"] != "zai" || gotBody["code"] != "auth-code" {
		t.Fatalf("token body = %v", gotBody)
	}
	if tokenData.AccessToken != "oauth-short" || tokenData.Email != "z@example.com" || tokenData.UserID != "u-1" {
		t.Fatalf("token data = %+v", tokenData)
	}

	denied := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string]any{"code": 400, "msg": "bad code"})
	}))
	defer denied.Close()
	overrideEndpoints(t, denied.URL, denied.URL, denied.URL)
	if _, err = NewZaiAuth(nil).ExchangeCode(context.Background(), "bad", "s1", ZaiCallbackRedirectURI); err == nil {
		t.Fatalf("expected envelope error")
	}
}

func TestMintKeyProvisionsDurableKey(t *testing.T) {
	var authed []string
	mux := http.NewServeMux()
	mux.HandleFunc("/api/auth/z/login", func(w http.ResponseWriter, r *http.Request) {
		var body map[string]string
		_ = json.NewDecoder(r.Body).Decode(&body)
		if body["token"] != "oauth-short" {
			t.Errorf("business login token = %q", body["token"])
		}
		_ = json.NewEncoder(w).Encode(map[string]any{
			"code": 200, "success": true,
			"data": map[string]any{"access_token": "biz-token"},
		})
	})
	mux.HandleFunc("/api/biz/customer/getCustomerInfo", func(w http.ResponseWriter, r *http.Request) {
		authed = append(authed, r.Header.Get("Authorization"))
		_ = json.NewEncoder(w).Encode(map[string]any{
			"code": 200, "success": true,
			"data": map[string]any{"organizations": []any{map[string]any{
				"organizationId": "org-1", "isDefault": true,
				"projects": []any{map[string]any{"projectId": "proj-1", "isDefault": true}},
			}}},
		})
	})
	mux.HandleFunc("/api/biz/v1/organization/org-1/projects/proj-1/api_keys", func(w http.ResponseWriter, r *http.Request) {
		authed = append(authed, r.Header.Get("Authorization"))
		if r.Method == http.MethodGet {
			_ = json.NewEncoder(w).Encode(map[string]any{
				"code": 200, "success": true, "data": []any{},
			})
			return
		}
		_ = json.NewEncoder(w).Encode(map[string]any{
			"code": 200, "success": true,
			"data": map[string]any{"apiKey": "ak-123"},
		})
	})
	mux.HandleFunc("/api/biz/v1/organization/org-1/projects/proj-1/api_keys/copy/ak-123", func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string]any{
			"code": 200, "success": true,
			"data": map[string]any{"secretKey": "sk-456"},
		})
	})
	server := httptest.NewServer(mux)
	defer server.Close()
	overrideEndpoints(t, server.URL+"/unused-token", server.URL+"/api/auth/z/login", server.URL)

	auth := NewZaiAuth(nil)
	key, err := auth.MintKey(context.Background(), "oauth-short")
	if err != nil {
		t.Fatalf("MintKey err = %v", err)
	}
	if key != "ak-123.sk-456" {
		t.Fatalf("minted key = %q", key)
	}
	for _, header := range authed {
		if header != "Bearer biz-token" {
			t.Fatalf("biz auth header = %q, want Bearer biz-token", header)
		}
	}
	if _, err := auth.MintKey(context.Background(), ""); err == nil {
		t.Fatalf("expected error for empty token")
	}
}

func TestZaiCreds(t *testing.T) {
	if got := ZaiCreds(map[string]any{"access_token": "k.1"}, nil); got != "k.1" {
		t.Fatalf("creds = %q", got)
	}
	if got := ZaiCreds(nil, map[string]string{"api_key": "k.2"}); got != "k.2" {
		t.Fatalf("attr creds = %q", got)
	}
}

func TestCredentialFileName(t *testing.T) {
	if got := CredentialFileName("Z@Example.com", ""); got != "zai-z@example.com.json" {
		t.Fatalf("name = %q", got)
	}
}
