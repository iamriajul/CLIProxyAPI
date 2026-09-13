// Package zai provides authentication and token management for Z.AI (GLM Coding Plan).
// It handles the authorization-code OAuth flow (no PKCE) against chat.z.ai plus
// the business-API sequence that provisions a durable id.secret API key.
package zai

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"strings"
	"time"

	"github.com/router-for-me/CLIProxyAPI/v7/internal/config"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/util"
	log "github.com/sirupsen/logrus"
)

const (
	// ZaiClientID is the public ZCode desktop OAuth client ID mirrored by this flow.
	ZaiClientID = "client_P8X5CMWmlaRO9gyO-KSqtg"
	// ZaiAuthorizeURL is the browser authorization endpoint.
	ZaiAuthorizeURL = "https://chat.z.ai/api/oauth/authorize"
	// ZaiTokenURL exchanges the authorization code for a short-lived token.
	ZaiTokenURL = "https://zcode.z.ai/api/v1/oauth/token"
	// ZaiBusinessLoginURL exchanges the OAuth token for a business token.
	ZaiBusinessLoginURL = "https://api.z.ai/api/auth/z/login"
	// ZaiAPIBaseURL is the default Z.AI API base (origin form).
	ZaiAPIBaseURL = "https://api.z.ai"
	// ZaiAnthropicBaseURL serves the Claude-protocol GLM lanes.
	ZaiAnthropicBaseURL = "https://api.z.ai/api/anthropic"
	// ZaiOpenAIBaseURL serves the OpenAI-completions coding lane.
	ZaiOpenAIBaseURL = "https://api.z.ai/api/coding/paas/v4"
	// ZaiQuotaURL reports subscription quota windows.
	ZaiQuotaURL = "https://api.z.ai/api/monitor/usage/quota/limit"
	// ZaiKeyName provisions keys under a CPA-owned name so sign-in never
	// mutates ZCode's own key.
	ZaiKeyName = "cli-proxy-api"
	// ZaiCallbackRedirectURI is the allowlisted native-scheme callback. Z.AI's
	// server-side allowlist rejects loopback redirect URIs for this client, so
	// users paste the resulting zcode:// URL (or bare code) back into the UI.
	ZaiCallbackRedirectURI = "zcode://zai-auth/callback"

	httpClientTimeout = 30 * time.Second
	// MaxPollDuration bounds waiting on user authorization.
	MaxPollDuration = 30 * time.Minute
)

var (
	zaiTokenEndpoint         = ZaiTokenURL
	zaiBusinessLoginEndpoint = ZaiBusinessLoginURL
	zaiAPIBaseEndpoint       = ZaiAPIBaseURL
)

// RedirectURI returns the OAuth callback URI, honoring an env override for
// allowlist shifts (mirrors the reference ZAI_OAUTH_REDIRECT_URI knob).
func RedirectURI() string {
	if v := strings.TrimSpace(os.Getenv("ZAI_OAUTH_REDIRECT_URI")); v != "" {
		return v
	}
	return ZaiCallbackRedirectURI
}

// DeviceCodeResponse carries the browser authorization URL and state.
// (Named for symmetry with the device-flow providers; Z.AI uses a browser
// authorization-code flow with manual paste-back.)
type DeviceCodeResponse struct {
	VerificationURI         string `json:"verification_uri"`
	VerificationURIComplete string `json:"verification_uri_complete"`
	UserCode                string `json:"user_code"`
	State                   string `json:"state"`
	ExpiresIn               int    `json:"expires_in"`
	Interval                int    `json:"interval"`
}

// TokenData holds the short-lived Z.AI OAuth token data.
type TokenData struct {
	AccessToken string `json:"access_token"`
	Email       string `json:"email"`
	UserID      string `json:"user_id"`
}

// AuthBundle aggregates the minted durable key for persistence.
type AuthBundle struct {
	APIKey string
	Email  string
	UserID string
}

// ZaiAuth performs Z.AI OAuth login and key provisioning.
type ZaiAuth struct {
	httpClient *http.Client
}

// NewZaiAuth creates a Z.AI OAuth helper using config proxy settings.
func NewZaiAuth(cfg *config.Config) *ZaiAuth {
	return NewZaiAuthWithProxyURL(cfg, "")
}

// NewZaiAuthWithProxyURL creates a Z.AI OAuth helper with an explicit proxy URL.
func NewZaiAuthWithProxyURL(cfg *config.Config, proxyURL string) *ZaiAuth {
	effectiveProxyURL := strings.TrimSpace(proxyURL)
	var sdkCfg config.SDKConfig
	if cfg != nil {
		sdkCfg = cfg.SDKConfig
		if effectiveProxyURL == "" {
			effectiveProxyURL = strings.TrimSpace(cfg.ProxyURL)
		}
	}
	sdkCfg.ProxyURL = effectiveProxyURL
	return &ZaiAuth{httpClient: util.SetProxy(&sdkCfg, &http.Client{Timeout: httpClientTimeout})}
}

// AuthorizeURL builds the browser URL: client_id, response_type=code,
// redirect_uri, and state (no PKCE, matching ZCode verbatim).
func AuthorizeURL(state, redirectURI string) string {
	params := url.Values{}
	params.Set("client_id", ZaiClientID)
	params.Set("response_type", "code")
	params.Set("redirect_uri", redirectURI)
	if strings.TrimSpace(state) != "" {
		params.Set("state", strings.TrimSpace(state))
	}
	return ZaiAuthorizeURL + "?" + params.Encode()
}

// StartDeviceFlow creates the authorization URL and state for manual paste-back.
func (a *ZaiAuth) StartDeviceFlow(ctx context.Context, state string) (*DeviceCodeResponse, error) {
	redirectURI := RedirectURI()
	complete := AuthorizeURL(state, redirectURI)
	return &DeviceCodeResponse{
		VerificationURI:         ZaiAuthorizeURL,
		VerificationURIComplete: complete,
		State:                   state,
		ExpiresIn:               int(MaxPollDuration / time.Second),
	}, nil
}

// ParsePastedCallback extracts code and state from a pasted zcode:// URL,
// https URL, code#state fragment, or bare code.
func ParsePastedCallback(input, sessionState string) (code, state string, err error) {
	trimmed := strings.TrimSpace(input)
	if trimmed == "" {
		return "", "", fmt.Errorf("zai: pasted callback is empty")
	}
	sessionState = strings.TrimSpace(sessionState)
	if strings.Contains(trimmed, "://") || strings.Contains(trimmed, "?") || strings.Contains(trimmed, "&") || strings.Contains(trimmed, "=") {
		normalized := trimmed
		if idx := strings.Index(normalized, "://"); idx >= 0 {
			normalized = "https://" + normalized[idx+3:]
		}
		if !strings.Contains(normalized, "?") && strings.Contains(normalized, "#") {
			normalized = strings.Replace(normalized, "#", "?", 1)
		}
		var parsed *url.URL
		parsed, err = url.Parse(normalized)
		if err != nil {
			return "", "", fmt.Errorf("zai: parse callback: %w", err)
		}
		query := parsed.Query()
		code = strings.TrimSpace(query.Get("code"))
		state = strings.TrimSpace(query.Get("state"))
		if code == "" {
			if frag := strings.TrimSpace(parsed.Fragment); frag != "" {
				parts := strings.SplitN(frag, "#", 2)
				code = strings.TrimSpace(parts[0])
				if len(parts) > 1 && strings.TrimSpace(parts[1]) != "" {
					state = strings.TrimSpace(parts[1])
				} else if fragCode := strings.TrimSpace(query.Get("code")); fragCode != "" {
					code = fragCode
				}
			}
		}
		// A URL-shaped paste without a code is a wrong paste (e.g. the
		// authorize URL), not a bare code — fail instead of sending the
		// whole URL to the token endpoint as the code.
		if code == "" {
			return "", "", fmt.Errorf("zai: no authorization code found in callback URL")
		}
	} else if idx := strings.Index(trimmed, "#"); idx >= 0 {
		code = strings.TrimSpace(trimmed[:idx])
		state = strings.TrimSpace(trimmed[idx+1:])
	} else {
		code = trimmed
	}
	if code == "" {
		return "", "", fmt.Errorf("zai: no authorization code found")
	}
	if state == "" {
		state = sessionState
	}
	if sessionState != "" && state != "" && state != sessionState {
		return "", "", fmt.Errorf("zai: state mismatch")
	}
	if state == "" {
		state = sessionState
	}
	return code, state, nil
}

func envelopeError(operation string, body map[string]any) error {
	code, _ := body["code"]
	msg, _ := body["msg"].(string)
	if success, ok := body["success"].(bool); ok && !success {
		if msg == "" {
			msg = fmt.Sprintf("code %v", code)
		}
		return fmt.Errorf("zai %s failed: %s", operation, msg)
	}
	switch c := code.(type) {
	case nil:
		return nil
	case float64:
		if c == 0 || c == 200 {
			return nil
		}
	case string:
		if c == "0" || c == "200" {
			return nil
		}
	}
	if msg == "" {
		msg = fmt.Sprintf("code %v", code)
	}
	return fmt.Errorf("zai %s failed: %s", operation, msg)
}

func decodeJSONResponse(resp *http.Response) (map[string]any, []byte, error) {
	raw, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, nil, err
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return nil, raw, fmt.Errorf("status %d: %s", resp.StatusCode, strings.TrimSpace(string(raw)))
	}
	var body map[string]any
	if len(bytes.TrimSpace(raw)) == 0 {
		return map[string]any{}, raw, nil
	}
	if err = json.Unmarshal(raw, &body); err != nil {
		return nil, raw, fmt.Errorf("parse response: %w", err)
	}
	return body, raw, nil
}

func stringField(m map[string]any, keys ...string) string {
	for _, key := range keys {
		if v, ok := m[key].(string); ok && strings.TrimSpace(v) != "" {
			return strings.TrimSpace(v)
		}
	}
	return ""
}

// ExchangeCode trades an authorization code for the short-lived OAuth token.
func (a *ZaiAuth) ExchangeCode(ctx context.Context, code, state, redirectURI string) (*TokenData, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	payload, _ := json.Marshal(map[string]string{
		"provider":     "zai",
		"code":         strings.TrimSpace(code),
		"redirect_uri": redirectURI,
		"state":        strings.TrimSpace(state),
	})
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, zaiTokenEndpoint, bytes.NewReader(payload))
	if err != nil {
		return nil, fmt.Errorf("zai token exchange: create request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json")

	resp, err := a.httpClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("zai token exchange request failed: %w", err)
	}
	defer func() {
		if errClose := resp.Body.Close(); errClose != nil {
			log.Errorf("zai token exchange: close response body error: %v", errClose)
		}
	}()
	body, _, err := decodeJSONResponse(resp)
	if err != nil {
		return nil, fmt.Errorf("zai token exchange: %w", err)
	}
	if err = envelopeError("token exchange", body); err != nil {
		return nil, err
	}
	data, _ := body["data"].(map[string]any)
	if data == nil {
		data = body
	}
	zai, _ := data["zai"].(map[string]any)
	accessToken := stringField(zai, "access_token", "accessToken")
	if accessToken == "" {
		accessToken = stringField(data, "access_token", "accessToken")
	}
	if accessToken == "" {
		return nil, fmt.Errorf("zai token exchange returned no access token")
	}
	user, _ := data["user"].(map[string]any)
	return &TokenData{
		AccessToken: accessToken,
		Email:       strings.ToLower(stringField(user, "email")),
		UserID:      stringField(user, "id", "userId", "user_id"),
	}, nil
}

func (a *ZaiAuth) postBizJSON(ctx context.Context, url string, payload map[string]string, bizToken string) (map[string]any, error) {
	raw, _ := json.Marshal(payload)
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(raw))
	if err != nil {
		return nil, fmt.Errorf("zai biz request: create request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json")
	if strings.TrimSpace(bizToken) != "" {
		req.Header.Set("Authorization", "Bearer "+strings.TrimSpace(bizToken))
	}
	resp, err := a.httpClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("zai biz request failed: %w", err)
	}
	defer func() {
		if errClose := resp.Body.Close(); errClose != nil {
			log.Errorf("zai biz request: close response body error: %v", errClose)
		}
	}()
	body, _, err := decodeJSONResponse(resp)
	if err != nil {
		return nil, err
	}
	if err = envelopeError("biz request", body); err != nil {
		return nil, err
	}
	if data, ok := body["data"].(map[string]any); ok {
		return data, nil
	}
	return body, nil
}

func (a *ZaiAuth) getBizJSON(ctx context.Context, url, bizToken string) (any, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return nil, fmt.Errorf("zai biz request: create request: %w", err)
	}
	req.Header.Set("Accept", "application/json")
	if strings.TrimSpace(bizToken) != "" {
		req.Header.Set("Authorization", "Bearer "+strings.TrimSpace(bizToken))
	}
	resp, err := a.httpClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("zai biz request failed: %w", err)
	}
	defer func() {
		if errClose := resp.Body.Close(); errClose != nil {
			log.Errorf("zai biz request: close response body error: %v", errClose)
		}
	}()
	body, _, err := decodeJSONResponse(resp)
	if err != nil {
		return nil, err
	}
	if err = envelopeError("biz request", body); err != nil {
		return nil, err
	}
	if data, ok := body["data"]; ok {
		return data, nil
	}
	return body, nil
}

// MintKey provisions (or reuses) the durable id.secret API key: business
// login, default org/project resolution, find-or-create key, copy secret.
func (a *ZaiAuth) MintKey(ctx context.Context, oauthAccessToken string) (string, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	oauthAccessToken = strings.TrimSpace(oauthAccessToken)
	if oauthAccessToken == "" {
		return "", fmt.Errorf("zai key mint: access token is required")
	}

	loginData, err := a.postBizJSON(ctx, zaiBusinessLoginEndpoint, map[string]string{"token": oauthAccessToken}, "")
	if err != nil {
		return "", fmt.Errorf("zai business login: %w", err)
	}
	bizToken := stringField(loginData, "access_token", "accessToken")
	if bizToken == "" {
		return "", fmt.Errorf("zai business login returned no access token")
	}

	customerRaw, err := a.getBizJSON(ctx, zaiAPIBaseEndpoint+"/api/biz/customer/getCustomerInfo", bizToken)
	if err != nil {
		return "", fmt.Errorf("zai customer lookup: %w", err)
	}
	customer, _ := customerRaw.(map[string]any)
	var orgs []any
	if customer != nil {
		orgs, _ = customer["organizations"].([]any)
	}
	var org map[string]any
	for _, o := range orgs {
		if m, ok := o.(map[string]any); ok {
			if org == nil {
				org = m
			}
			if def, _ := m["isDefault"].(bool); def {
				org = m
				break
			}
		}
	}
	var organizationID, projectID string
	var projects []any
	if org != nil {
		organizationID = stringField(org, "organizationId", "organization_id")
		projects, _ = org["projects"].([]any)
	}
	var project map[string]any
	for _, p := range projects {
		if m, ok := p.(map[string]any); ok {
			if project == nil {
				project = m
			}
			if def, _ := m["isDefault"].(bool); def {
				project = m
				break
			}
		}
	}
	if project != nil {
		projectID = stringField(project, "projectId", "project_id")
	}
	if organizationID == "" || projectID == "" {
		return "", fmt.Errorf("zai key provisioning failed: no organization/project on account")
	}

	keysURL := fmt.Sprintf("%s/api/biz/v1/organization/%s/projects/%s/api_keys", zaiAPIBaseEndpoint, url.PathEscape(organizationID), url.PathEscape(projectID))
	listRaw, err := a.getBizJSON(ctx, keysURL, bizToken)
	if err != nil {
		return "", fmt.Errorf("zai api key list: %w", err)
	}
	var keys []any
	switch v := listRaw.(type) {
	case []any:
		keys = v
	case map[string]any:
		for _, field := range []string{"list", "keys", "apiKeys", "records"} {
			if arr, ok := v[field].([]any); ok {
				keys = arr
				break
			}
		}
	}
	var keyRecord map[string]any
	for _, k := range keys {
		if m, ok := k.(map[string]any); ok && stringField(m, "name") == ZaiKeyName {
			keyRecord = m
			break
		}
	}
	if keyRecord == nil {
		created, err := a.postBizJSON(ctx, keysURL, map[string]string{"name": ZaiKeyName}, bizToken)
		if err != nil {
			return "", fmt.Errorf("zai api key create: %w", err)
		}
		keyRecord = created
	}
	apiKey := stringField(keyRecord, "apiKey", "api_key")
	if apiKey == "" {
		return "", fmt.Errorf("zai key provisioning returned no apiKey")
	}
	// Always fetch the secret via copy: list entries mask it and the create
	// response inline secret is unreliable across account states.
	copiedRaw, err := a.getBizJSON(ctx, keysURL+"/copy/"+url.PathEscape(apiKey), bizToken)
	if err != nil {
		return "", fmt.Errorf("zai api key copy: %w", err)
	}
	copied, _ := copiedRaw.(map[string]any)
	secretKey := stringField(copied, "secretKey", "secret_key")
	if secretKey == "" {
		return "", fmt.Errorf("zai key provisioning returned no secretKey")
	}
	return apiKey + "." + secretKey, nil
}

// WaitForAuthorization exchanges a pasted code and mints the durable key.
func (a *ZaiAuth) WaitForAuthorization(ctx context.Context, code, state, redirectURI string) (*AuthBundle, error) {
	tokenData, err := a.ExchangeCode(ctx, code, state, redirectURI)
	if err != nil {
		return nil, err
	}
	apiKey, err := a.MintKey(ctx, tokenData.AccessToken)
	if err != nil {
		return nil, err
	}
	return &AuthBundle{APIKey: apiKey, Email: tokenData.Email, UserID: tokenData.UserID}, nil
}

// CreateTokenStorage converts an auth bundle into persistable storage.
func (a *ZaiAuth) CreateTokenStorage(bundle *AuthBundle) *TokenStorage {
	if bundle == nil {
		return nil
	}
	return &TokenStorage{
		Type:        "zai",
		AccessToken: strings.TrimSpace(bundle.APIKey),
		Email:       strings.ToLower(strings.TrimSpace(bundle.Email)),
		UserID:      strings.TrimSpace(bundle.UserID),
		AuthKind:    "oauth",
	}
}

// ZaiCreds extracts the minted Z.AI key from auth metadata/attributes.
// The key is sent verbatim (no Bearer prefix) on both inference and quota.
func ZaiCreds(metadata map[string]any, attributes map[string]string) string {
	if metadata != nil {
		for _, key := range []string{"access_token", "api_key", "apikey"} {
			if v, ok := metadata[key].(string); ok && strings.TrimSpace(v) != "" {
				return strings.TrimSpace(v)
			}
		}
	}
	if attributes != nil {
		for _, key := range []string{"access_token", "api_key"} {
			if v := strings.TrimSpace(attributes[key]); v != "" {
				return v
			}
		}
	}
	return ""
}
