package muse

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func overrideEndpoints(t *testing.T, device, token, key string) {
	t.Helper()
	oldDevice, oldToken, oldKey := museDeviceAuthorizationEndpoint, museTokenEndpoint, museKeyEndpoint
	museDeviceAuthorizationEndpoint, museTokenEndpoint, museKeyEndpoint = device, token, key
	oldInterval := museMinPollInterval
	museMinPollInterval = time.Millisecond
	t.Cleanup(func() {
		museDeviceAuthorizationEndpoint, museTokenEndpoint, museKeyEndpoint = oldDevice, oldToken, oldKey
		museMinPollInterval = oldInterval
	})
}

func TestRequestDeviceCodePostsClientID(t *testing.T) {
	var gotClientID, gotAPIVersion, gotContentType, gotUA string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = r.ParseForm()
		gotClientID = r.PostForm.Get("client_id")
		gotAPIVersion = r.Header.Get("x-api-version")
		gotContentType = r.Header.Get("Content-Type")
		gotUA = r.Header.Get("User-Agent")
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{
			"device_code":               "device-abc",
			"user_code":                 "ABCD-1234",
			"verification_uri":          "https://auth.meta.com/device",
			"verification_uri_complete": "https://auth.meta.com/device?user_code=ABCD-1234",
			"expires_in":                600,
			"interval":                  5,
		})
	}))
	defer server.Close()
	overrideEndpoints(t, server.URL, server.URL, server.URL)

	auth := NewMuseAuth(nil)
	deviceCode, err := auth.StartDeviceFlow(context.Background())
	if err != nil {
		t.Fatalf("StartDeviceFlow err = %v", err)
	}
	if gotClientID != MuseClientID {
		t.Fatalf("client_id = %q, want %q", gotClientID, MuseClientID)
	}
	if gotAPIVersion != MuseAPIVersion {
		t.Fatalf("x-api-version = %q, want %q", gotAPIVersion, MuseAPIVersion)
	}
	if !strings.HasPrefix(gotContentType, "application/x-www-form-urlencoded") {
		t.Fatalf("Content-Type = %q, want form urlencoded", gotContentType)
	}
	if gotUA != UserAgent {
		t.Fatalf("User-Agent = %q, want official family %q", gotUA, UserAgent)
	}
	if deviceCode.DeviceCode != "device-abc" || deviceCode.UserCode != "ABCD-1234" {
		t.Fatalf("device code = %+v", deviceCode)
	}
}

func TestPollForTokenExchangesDeviceCode(t *testing.T) {
	calls := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		_ = r.ParseForm()
		if r.PostForm.Get("client_id") != MuseClientID {
			t.Errorf("client_id = %q, want %q", r.PostForm.Get("client_id"), MuseClientID)
		}
		if r.PostForm.Get("grant_type") != DeviceCodeGrantType {
			t.Errorf("grant_type = %q, want device_code grant", r.PostForm.Get("grant_type"))
		}
		if got := r.Header.Get("x-api-version"); got != MuseAPIVersion {
			t.Errorf("x-api-version = %q, want %q", got, MuseAPIVersion)
		}
		w.Header().Set("Content-Type", "application/json")
		if calls == 1 {
			w.WriteHeader(http.StatusBadRequest)
			_ = json.NewEncoder(w).Encode(map[string]string{"error": "authorization_pending"})
			return
		}
		if calls == 2 {
			w.WriteHeader(http.StatusBadRequest)
			_ = json.NewEncoder(w).Encode(map[string]string{"error": "slow_down"})
			return
		}
		_ = json.NewEncoder(w).Encode(map[string]string{
			"access_token":  "meta-account-access",
			"refresh_token": "meta-refresh",
		})
	}))
	defer server.Close()
	overrideEndpoints(t, server.URL, server.URL, server.URL)

	auth := NewMuseAuth(nil)
	token, err := auth.PollForToken(context.Background(), &DeviceCodeResponse{
		DeviceCode: "device-abc",
		ExpiresIn:  60,
		Interval:   0,
	})
	if err != nil {
		t.Fatalf("PollForToken err = %v", err)
	}
	if token.AccessToken != "meta-account-access" {
		t.Fatalf("access_token = %q, want meta-account-access", token.AccessToken)
	}
	if calls != 3 {
		t.Fatalf("token calls = %d, want 3 (pending + slow_down + success)", calls)
	}
}

func TestPollForTokenTerminalErrors(t *testing.T) {
	for _, tc := range []struct {
		name    string
		errCode string
		want    string
	}{
		{"denied", "access_denied", "denied"},
		{"expired", "expired_token", "expired"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.WriteHeader(http.StatusBadRequest)
				_ = json.NewEncoder(w).Encode(map[string]string{"error": tc.errCode})
			}))
			defer server.Close()
			overrideEndpoints(t, server.URL, server.URL, server.URL)
			auth := NewMuseAuth(nil)
			_, err := auth.PollForToken(context.Background(), &DeviceCodeResponse{DeviceCode: "d", ExpiresIn: 60})
			if err == nil || !strings.Contains(strings.ToLower(err.Error()), tc.want) {
				t.Fatalf("err = %v, want containing %q", err, tc.want)
			}
		})
	}
}

func TestRequestKeySendsOnboardAndVersion(t *testing.T) {
	var gotAuth, gotVersion, gotBody, gotUA string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotAuth = r.Header.Get("Authorization")
		gotVersion = r.Header.Get("x-api-version")
		gotUA = r.Header.Get("User-Agent")
		buf := make([]byte, 1024)
		n, _ := r.Body.Read(buf)
		gotBody = string(buf[:n])
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{
			"api_key":        "LLM|subscription-key",
			"user_email":     "Muse@Example.com",
			"user_id":        "meta-account-1",
			"is_subs_active": true,
		})
	}))
	defer server.Close()
	overrideEndpoints(t, server.URL, server.URL, server.URL)

	auth := NewMuseAuth(nil)
	key, err := auth.RequestKey(context.Background(), "meta-account-access", true)
	if err != nil {
		t.Fatalf("RequestKey err = %v", err)
	}
	if gotAuth != "Bearer meta-account-access" {
		t.Fatalf("Authorization = %q", gotAuth)
	}
	if gotVersion != MuseAPIVersion {
		t.Fatalf("x-api-version = %q", gotVersion)
	}
	if gotUA != UserAgent {
		t.Fatalf("User-Agent = %q, want official family %q", gotUA, UserAgent)
	}
	if !strings.Contains(gotBody, `"onboard":true`) {
		t.Fatalf("body = %q, want onboard:true", gotBody)
	}
	if key.APIKey != "LLM|subscription-key" || key.UserID != "meta-account-1" {
		t.Fatalf("key = %+v", key)
	}

	// Refresh/usage probe must not send onboard.
	var gotBody2 string
	server2 := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		buf := make([]byte, 1024)
		n, _ := r.Body.Read(buf)
		gotBody2 = string(buf[:n])
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{"api_key": "LLM|x"})
	}))
	defer server2.Close()
	overrideEndpoints(t, server2.URL, server2.URL, server2.URL)
	if _, err = NewMuseAuth(nil).RequestKey(context.Background(), "meta-account-access", false); err != nil {
		t.Fatalf("RequestKey refresh err = %v", err)
	}
	if strings.Contains(gotBody2, "onboard") {
		t.Fatalf("refresh body = %q, want no onboard", gotBody2)
	}
}

func TestRequestKeyFailureIncludesStatus(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusTooManyRequests)
		_, _ = w.Write([]byte("rate limited"))
	}))
	defer server.Close()
	overrideEndpoints(t, server.URL, server.URL, server.URL)
	if _, err := NewMuseAuth(nil).RequestKey(context.Background(), "tok", true); err == nil || !strings.Contains(err.Error(), "429") {
		t.Fatalf("err = %v, want 429", err)
	}
}

func TestWaitForAuthorizationMintsKey(t *testing.T) {
	tokenCalls, keyCalls := 0, 0
	mux := http.NewServeMux()
	mux.HandleFunc("/device", func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string]string{"access_token": "meta-account-access"})
		tokenCalls++
	})
	mux.HandleFunc("/key", func(w http.ResponseWriter, r *http.Request) {
		keyCalls++
		if got := r.Header.Get("Authorization"); got != "Bearer meta-account-access" {
			t.Errorf("Authorization = %q", got)
		}
		_ = json.NewEncoder(w).Encode(map[string]any{
			"api_key": "LLM|k", "user_email": "a@b.c", "user_id": "uid", "is_subs_active": true,
		})
	})
	server := httptest.NewServer(mux)
	defer server.Close()

	auth := NewMuseAuth(nil)
	oldToken, oldKey := museTokenEndpoint, museKeyEndpoint
	museTokenEndpoint = server.URL + "/device"
	museKeyEndpoint = server.URL + "/key"
	defer func() { museTokenEndpoint, museKeyEndpoint = oldToken, oldKey }()

	// PollForToken posts to museTokenEndpoint; serve a success immediately.
	bundle, err := auth.WaitForAuthorization(context.Background(), &DeviceCodeResponse{DeviceCode: "d", ExpiresIn: 60})
	if err != nil {
		t.Fatalf("WaitForAuthorization err = %v", err)
	}
	if bundle.Key.APIKey != "LLM|k" {
		t.Fatalf("key = %+v", bundle.Key)
	}
	if tokenCalls != 1 || keyCalls != 1 {
		t.Fatalf("calls token=%d key=%d, want 1/1", tokenCalls, keyCalls)
	}
}

func TestEnsureSubscriptionActive(t *testing.T) {
	active := true
	if err := EnsureSubscriptionActive(&MuseKeyResponse{APIKey: "LLM|x", IsSubsActive: &active}); err != nil {
		t.Fatalf("active subscription err = %v, want nil", err)
	}
	inactive := false
	if err := EnsureSubscriptionActive(&MuseKeyResponse{IsSubsActive: &inactive}); err == nil {
		t.Fatalf("inactive subscription err = nil, want error")
	}
	action := "https://www.meta.ai/subscribe"
	payment := true
	if err := EnsureSubscriptionActive(&MuseKeyResponse{RequirePayment: &payment, ActionURL: &action}); err == nil || !strings.Contains(err.Error(), action) {
		t.Fatalf("payment required err = %v, want action URL", err)
	}
	if err := EnsureSubscriptionActive(&MuseKeyResponse{}); err == nil {
		t.Fatalf("missing api_key err = nil, want error")
	}
	// Null tier fields must not fail validation when a key is present.
	if err := EnsureSubscriptionActive(&MuseKeyResponse{APIKey: "LLM|x", SubsTierID: nil, SubsTierName: nil}); err != nil {
		t.Fatalf("null tiers err = %v, want nil", err)
	}
}

func TestParseCombinedCredential(t *testing.T) {
	encoded := EncodeCombinedCredential("meta-account-access", "LLM|subscription-key")
	cred, err := ParseCombinedCredential(encoded)
	if err != nil {
		t.Fatalf("ParseCombinedCredential err = %v", err)
	}
	if cred.OAuthAccessToken != "meta-account-access" || cred.APIKey != "LLM|subscription-key" {
		t.Fatalf("credential = %+v, want oauth + api key", cred)
	}
	if _, err = ParseCombinedCredential("not-json"); err == nil {
		t.Fatalf("invalid credential err = nil, want error")
	}
	if _, err = ParseCombinedCredential(`{"oauthAccessToken":"","apiKey":""}`); err == nil {
		t.Fatalf("empty credential err = nil, want error")
	}
}

func TestResolveMuseAPIKey(t *testing.T) {
	metadata := map[string]any{"muse_api_key": "LLM|direct", "access_token": "oauth-token"}
	if got := ResolveMuseAPIKey(metadata, nil); got != "LLM|direct" {
		t.Fatalf("ResolveMuseAPIKey = %q, want LLM|direct", got)
	}
	combined := EncodeCombinedCredential("oauth-token", "LLM|combined")
	metadata = map[string]any{"access_token": combined}
	if got := ResolveMuseAPIKey(metadata, nil); got != "LLM|combined" {
		t.Fatalf("ResolveMuseAPIKey combined = %q, want LLM|combined", got)
	}
	if got := ResolveMuseOAuthToken(metadata, nil); got != "oauth-token" {
		t.Fatalf("ResolveMuseOAuthToken = %q, want oauth-token", got)
	}
	// A flat api_key only counts when it has the minted LLM| shape; anything
	// else belongs in openai-compatibility, not a muse file.
	if got := ResolveMuseAPIKey(map[string]any{"api_key": "LLM|flat"}, nil); got != "LLM|flat" {
		t.Fatalf("ResolveMuseAPIKey flat LLM| = %q, want LLM|flat", got)
	}
	if got := ResolveMuseAPIKey(map[string]any{"api_key": "sk-payg-1234567890abcdef"}, nil); got != "" {
		t.Fatalf("ResolveMuseAPIKey payg = %q, want empty", got)
	}
	if got := ResolveMuseAPIKey(nil, map[string]string{"api_key": "sk-payg-1234567890abcdef"}); got != "" {
		t.Fatalf("ResolveMuseAPIKey attr payg = %q, want empty", got)
	}
}

func TestCredentialFileName(t *testing.T) {
	if got := CredentialFileName("Muse@Example.com", ""); got != "muse-muse@example.com.json" {
		t.Fatalf("CredentialFileName = %q", got)
	}
	if got := CredentialFileName("", "user-123"); got != "muse-user-123.json" {
		t.Fatalf("CredentialFileName user = %q", got)
	}
	if got := CredentialFileName("", ""); !strings.HasPrefix(got, "muse-") {
		t.Fatalf("CredentialFileName fallback = %q, want muse- prefix", got)
	}
}

func TestCreateTokenStorageDurable(t *testing.T) {
	active := true
	bundle := &AuthBundle{
		TokenData: TokenData{AccessToken: "oauth", RefreshToken: "refresh"},
		Key:       MuseKeyResponse{APIKey: "LLM|k", UserEmail: "Muse@Example.com", UserID: "uid", IsSubsActive: &active},
	}
	auth := (&MuseAuth{}).CreateTokenStorage(bundle)
	if auth == nil {
		t.Fatalf("CreateTokenStorage = nil")
	}
	if auth.Type != "muse" || auth.MuseAPIKey != "LLM|k" || auth.Email != "muse@example.com" {
		t.Fatalf("storage = %+v", auth)
	}
	if auth.IsExpired() {
		t.Fatalf("durable key should never expire when Expired is empty")
	}
	if got := time.Now().Format("2006"); got == "" {
		t.Fatalf("time sanity")
	}
}
