// Package muse provides authentication and token management for Muse Code (Meta) API.
// It handles the RFC 8628 OAuth2 Device Authorization Grant flow plus the
// subscription key-mint exchange used by Muse Code subscriptions.
package muse

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/router-for-me/CLIProxyAPI/v7/internal/config"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/util"
	log "github.com/sirupsen/logrus"
	"golang.org/x/sync/singleflight"
)

const (
	// MuseClientID is the public Muse Code OAuth client ID.
	MuseClientID = "1031625952748946"
	// MuseDeviceAuthorizationURL is the endpoint for requesting device codes.
	MuseDeviceAuthorizationURL = "https://auth.meta.com/oidc/device/authorization/"
	// MuseTokenURL is the endpoint for exchanging device codes for tokens.
	MuseTokenURL = "https://auth.meta.com/oidc/device/token/"
	// MuseKeyURL mints the subscription-backed Model API key.
	MuseKeyURL = "https://api.meta.ai/muse-code/key"
	// MuseAPIBaseURL is the base URL for Muse Model API requests.
	MuseAPIBaseURL = "https://api.meta.ai/v1"
	// MuseModelsURL lists available Muse models.
	MuseModelsURL = MuseAPIBaseURL + "/models"
	// MuseAPIVersion is sent as x-api-version on device, token, key, and model requests.
	MuseAPIVersion = "1.0.0"
	// UserAgent identifies Meta-bound requests as the official Muse client family.
	// Measured basis: Meta's own launcher (api.meta.ai/muse-launcher.sh) sends
	// muse-code/launcher-2 on every request. The bare family token below avoids
	// fabricating a component/version we have not measured, while staying
	// unmistakably inside the official family instead of leaking Go's transport
	// default or a foreign harness identity.
	UserAgent = "muse-code"
	// DeviceCodeGrantType is the OAuth2 device authorization grant type (RFC 8628).
	DeviceCodeGrantType = "urn:ietf:params:oauth:grant-type:device_code"

	defaultPollInterval = 5 * time.Second
	httpClientTimeout   = 30 * time.Second
	// MaxPollDuration bounds waiting on user authorization.
	MaxPollDuration = 30 * time.Minute
)

var museRefreshGroup singleflight.Group

var (
	museDeviceAuthorizationEndpoint = MuseDeviceAuthorizationURL
	museTokenEndpoint               = MuseTokenURL
	museKeyEndpoint                 = MuseKeyURL
	museMinPollInterval             = defaultPollInterval
)

// DeviceCodeResponse represents Meta's device authorization response.
type DeviceCodeResponse struct {
	DeviceCode              string `json:"device_code"`
	UserCode                string `json:"user_code"`
	VerificationURI         string `json:"verification_uri"`
	VerificationURIComplete string `json:"verification_uri_complete"`
	ExpiresIn               int    `json:"expires_in"`
	Interval                int    `json:"interval"`
}

// TokenData holds Meta OAuth token data.
type TokenData struct {
	AccessToken  string `json:"access_token"`
	RefreshToken string `json:"refresh_token"`
	TokenType    string `json:"token_type,omitempty"`
	ExpiresIn    int    `json:"expires_in,omitempty"`
}

// AuthBundle aggregates token data and the minted subscription key for persistence.
type AuthBundle struct {
	TokenData TokenData
	Key       MuseKeyResponse
}

// SubscriptionUsageWindow is one subscription usage bucket from the key endpoint.
type SubscriptionUsageWindow struct {
	UsedPercent       *float64 `json:"used_percent,omitempty"`
	ResetsAt          any      `json:"resets_at,omitempty"`
	WindowDurationMin *float64 `json:"window_duration_mins,omitempty"`
}

// SubscriptionUsage carries rolling + weekly subscription usage.
type SubscriptionUsage struct {
	Window *SubscriptionUsageWindow `json:"window,omitempty"`
	Weekly *SubscriptionUsageWindow `json:"weekly,omitempty"`
}

// MuseKeyResponse is the subscription key-mint response.
type MuseKeyResponse struct {
	APIKey                  string             `json:"api_key,omitempty"`
	RequirePayment          *bool              `json:"require_payment,omitempty"`
	RequirePaymentActionURL string             `json:"require_payment_action_url,omitempty"`
	ActionURL               *string            `json:"action_url,omitempty"`
	UserEmail               string             `json:"user_email,omitempty"`
	UserID                  string             `json:"user_id,omitempty"`
	IsSubsActive            *bool              `json:"is_subs_active,omitempty"`
	SubsTierID              *string            `json:"subs_tier_id,omitempty"`
	SubsTierName            *string            `json:"subs_tier_name,omitempty"`
	SubsUsage               *SubscriptionUsage `json:"subs_usage,omitempty"`
}

// MuseAuth performs Muse device-code login and subscription key minting.
type MuseAuth struct {
	httpClient *http.Client
}

// NewMuseAuth creates a Muse OAuth helper using config proxy settings.
func NewMuseAuth(cfg *config.Config) *MuseAuth {
	return NewMuseAuthWithProxyURL(cfg, "")
}

// NewMuseAuthWithProxyURL creates a Muse OAuth helper with an explicit proxy URL.
func NewMuseAuthWithProxyURL(cfg *config.Config, proxyURL string) *MuseAuth {
	effectiveProxyURL := strings.TrimSpace(proxyURL)
	var sdkCfg config.SDKConfig
	if cfg != nil {
		sdkCfg = cfg.SDKConfig
		if effectiveProxyURL == "" {
			effectiveProxyURL = strings.TrimSpace(cfg.ProxyURL)
		}
	}
	sdkCfg.ProxyURL = effectiveProxyURL
	return &MuseAuth{httpClient: util.SetProxy(&sdkCfg, &http.Client{Timeout: httpClientTimeout})}
}

// StartDeviceFlow requests a device code from Meta.
func (a *MuseAuth) StartDeviceFlow(ctx context.Context) (*DeviceCodeResponse, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	form := url.Values{
		"client_id": {MuseClientID},
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, museDeviceAuthorizationEndpoint, strings.NewReader(form.Encode()))
	if err != nil {
		return nil, fmt.Errorf("muse device code: create request: %w", err)
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.Header.Set("Accept", "application/json")
	req.Header.Set("x-api-version", MuseAPIVersion)
	req.Header.Set("User-Agent", UserAgent)

	resp, err := a.httpClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("muse device code request failed: %w", err)
	}
	defer func() {
		if errClose := resp.Body.Close(); errClose != nil {
			log.Errorf("muse device code: close response body error: %v", errClose)
		}
	}()

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("muse device code: read response: %w", err)
	}
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("muse device code request failed with status %d: %s", resp.StatusCode, strings.TrimSpace(string(body)))
	}

	var deviceCode DeviceCodeResponse
	if err = json.Unmarshal(body, &deviceCode); err != nil {
		return nil, fmt.Errorf("muse device code: parse response: %w", err)
	}
	if strings.TrimSpace(deviceCode.DeviceCode) == "" {
		return nil, fmt.Errorf("muse device code: response missing device_code")
	}
	if strings.TrimSpace(deviceCode.UserCode) == "" {
		return nil, fmt.Errorf("muse device code: response missing user_code")
	}
	if strings.TrimSpace(deviceCode.VerificationURI) == "" && strings.TrimSpace(deviceCode.VerificationURIComplete) == "" {
		return nil, fmt.Errorf("muse device code: response missing verification URI")
	}
	return &deviceCode, nil
}

// WaitForAuthorization polls until the user authorizes the device code,
// then mints the subscription Model API key.
func (a *MuseAuth) WaitForAuthorization(ctx context.Context, deviceCode *DeviceCodeResponse) (*AuthBundle, error) {
	tokenData, err := a.PollForToken(ctx, deviceCode)
	if err != nil {
		return nil, err
	}
	key, err := a.RequestKey(ctx, strings.TrimSpace(tokenData.AccessToken), true)
	if err != nil {
		return nil, err
	}
	if err = EnsureSubscriptionActive(key); err != nil {
		return nil, err
	}
	return &AuthBundle{TokenData: *tokenData, Key: *key}, nil
}

// PollForToken polls the token endpoint until the user authorizes or the device code expires.
func (a *MuseAuth) PollForToken(ctx context.Context, deviceCode *DeviceCodeResponse) (*TokenData, error) {
	if deviceCode == nil {
		return nil, fmt.Errorf("muse device code: response is nil")
	}
	if ctx == nil {
		ctx = context.Background()
	}

	interval := time.Duration(deviceCode.Interval) * time.Second
	if interval < museMinPollInterval {
		interval = museMinPollInterval
	}

	deadline := time.Now().Add(MaxPollDuration)
	if deviceCode.ExpiresIn > 0 {
		codeDeadline := time.Now().Add(time.Duration(deviceCode.ExpiresIn) * time.Second)
		if codeDeadline.Before(deadline) {
			deadline = codeDeadline
		}
	}

	firstAttempt := true
	timer := time.NewTimer(0)
	defer timer.Stop()

	for {
		select {
		case <-ctx.Done():
			return nil, fmt.Errorf("muse device code: context cancelled: %w", ctx.Err())
		case <-timer.C:
			if !firstAttempt && time.Now().After(deadline) {
				return nil, fmt.Errorf("muse device code expired")
			}
			firstAttempt = false

			token, pollErr, nextInterval, shouldContinue := a.exchangeDeviceCode(ctx, deviceCode.DeviceCode, interval)
			if token != nil {
				return token, nil
			}
			if !shouldContinue {
				return nil, pollErr
			}
			interval = nextInterval
			timer.Reset(interval)
		}
	}
}

func (a *MuseAuth) exchangeDeviceCode(ctx context.Context, deviceCode string, interval time.Duration) (*TokenData, error, time.Duration, bool) {
	form := url.Values{
		"grant_type":  {DeviceCodeGrantType},
		"device_code": {strings.TrimSpace(deviceCode)},
		"client_id":   {MuseClientID},
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, museTokenEndpoint, strings.NewReader(form.Encode()))
	if err != nil {
		return nil, fmt.Errorf("muse device token: create request: %w", err), interval, false
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.Header.Set("Accept", "application/json")
	req.Header.Set("x-api-version", MuseAPIVersion)
	req.Header.Set("User-Agent", UserAgent)

	resp, err := a.httpClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("muse device token request failed: %w", err), interval, false
	}
	defer func() {
		if errClose := resp.Body.Close(); errClose != nil {
			log.Errorf("muse device token: close response body error: %v", errClose)
		}
	}()

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("muse device token: read response: %w", err), interval, false
	}

	var payload struct {
		Error            string `json:"error"`
		ErrorDescription string `json:"error_description"`
		AccessToken      string `json:"access_token"`
		RefreshToken     string `json:"refresh_token"`
		TokenType        string `json:"token_type"`
		ExpiresIn        int    `json:"expires_in"`
	}
	if err = json.Unmarshal(body, &payload); err != nil {
		return nil, fmt.Errorf("muse device token: parse response: %w", err), interval, false
	}

	if payload.Error != "" {
		switch payload.Error {
		case "authorization_pending":
			return nil, nil, interval, true
		case "slow_down":
			return nil, nil, interval + museMinPollInterval, true
		case "expired_token":
			return nil, fmt.Errorf("muse device code expired"), interval, false
		case "access_denied":
			return nil, fmt.Errorf("muse device authorization denied"), interval, false
		default:
			desc := strings.TrimSpace(payload.ErrorDescription)
			if desc != "" {
				return nil, fmt.Errorf("muse device token error: %s: %s", payload.Error, desc), interval, false
			}
			return nil, fmt.Errorf("muse device token error: %s", payload.Error), interval, false
		}
	}

	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("muse device token request failed with status %d: %s", resp.StatusCode, strings.TrimSpace(string(body))), interval, false
	}
	if strings.TrimSpace(payload.AccessToken) == "" {
		return nil, fmt.Errorf("muse device token response missing access_token"), interval, false
	}

	return &TokenData{
		AccessToken:  strings.TrimSpace(payload.AccessToken),
		RefreshToken: strings.TrimSpace(payload.RefreshToken),
		TokenType:    strings.TrimSpace(payload.TokenType),
		ExpiresIn:    payload.ExpiresIn,
	}, nil, interval, false
}

// RequestKey mints (or re-fetches) the subscription Model API key for an account token.
// onboard should be true on interactive login and false on refresh/usage probes.
// The key endpoint is aggressively rate-limited: callers must reuse an already-minted
// key instead of calling this on every refresh.
func (a *MuseAuth) RequestKey(ctx context.Context, accessToken string, onboard bool) (*MuseKeyResponse, error) {
	return RequestMuseKeyWithClient(ctx, a.httpClient, strings.TrimSpace(accessToken), onboard)
}

// RequestMuseKeyWithClient mints the subscription key with an explicit HTTP client.
func RequestMuseKeyWithClient(ctx context.Context, client *http.Client, accessToken string, onboard bool) (*MuseKeyResponse, error) {
	accessToken = strings.TrimSpace(accessToken)
	if accessToken == "" {
		return nil, fmt.Errorf("muse key exchange: access token is required")
	}
	if ctx == nil {
		ctx = context.Background()
	}
	if client == nil {
		client = &http.Client{Timeout: httpClientTimeout}
	}
	body := "{}"
	if onboard {
		body = `{"onboard":true}`
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, museKeyEndpoint, strings.NewReader(body))
	if err != nil {
		return nil, fmt.Errorf("muse key exchange: create request: %w", err)
	}
	req.Header.Set("Accept", "application/json")
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("x-api-version", MuseAPIVersion)
	req.Header.Set("User-Agent", UserAgent)
	req.Header.Set("Authorization", "Bearer "+accessToken)

	resp, err := client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("muse key exchange request failed: %w", err)
	}
	defer func() {
		if errClose := resp.Body.Close(); errClose != nil {
			log.Errorf("muse key exchange: close response body error: %v", errClose)
		}
	}()

	raw, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("muse key exchange: read response: %w", err)
	}
	if resp.StatusCode != http.StatusOK {
		excerpt := strings.TrimSpace(string(raw))
		if len(excerpt) > 500 {
			excerpt = excerpt[:500]
		}
		if excerpt != "" {
			return nil, fmt.Errorf("muse key exchange failed with status %d: %s", resp.StatusCode, excerpt)
		}
		return nil, fmt.Errorf("muse key exchange failed with status %d", resp.StatusCode)
	}

	var key MuseKeyResponse
	if err = json.Unmarshal(raw, &key); err != nil {
		return nil, fmt.Errorf("muse key exchange: parse response: %w", err)
	}
	return &key, nil
}

// EnsureSubscriptionActive fails closed when the subscription is inactive or payment is required.
func EnsureSubscriptionActive(key *MuseKeyResponse) error {
	if key == nil {
		return fmt.Errorf("muse key exchange returned empty response")
	}
	if key.IsSubsActive != nil && !*key.IsSubsActive {
		return fmt.Errorf("muse subscription is inactive (is_subs_active=false)")
	}
	apiKey := strings.TrimSpace(key.APIKey)
	if apiKey != "" {
		return nil
	}
	actionURL := ""
	if key.ActionURL != nil {
		actionURL = strings.TrimSpace(*key.ActionURL)
	}
	if actionURL == "" {
		actionURL = strings.TrimSpace(key.RequirePaymentActionURL)
	}
	if (key.RequirePayment != nil && *key.RequirePayment) || actionURL != "" {
		if actionURL != "" {
			return fmt.Errorf("muse subscription is required: %s", actionURL)
		}
		return fmt.Errorf("muse subscription is required")
	}
	return fmt.Errorf("muse key response is missing api_key")
}

// CreateTokenStorage converts an auth bundle into persistable storage.
func (a *MuseAuth) CreateTokenStorage(bundle *AuthBundle) *TokenStorage {
	if bundle == nil {
		return nil
	}
	email := strings.ToLower(strings.TrimSpace(bundle.Key.UserEmail))
	storage := &TokenStorage{
		Type:         "muse",
		AccessToken:  strings.TrimSpace(bundle.TokenData.AccessToken),
		RefreshToken: strings.TrimSpace(bundle.TokenData.RefreshToken),
		TokenType:    strings.TrimSpace(bundle.TokenData.TokenType),
		MuseAPIKey:   strings.TrimSpace(bundle.Key.APIKey),
		Email:        email,
		UserID:       strings.TrimSpace(bundle.Key.UserID),
		AuthKind:     "oauth",
	}
	if bundle.Key.SubsTierID != nil {
		storage.SubsTierID = strings.TrimSpace(*bundle.Key.SubsTierID)
	}
	if bundle.Key.SubsTierName != nil {
		storage.SubsTierName = strings.TrimSpace(*bundle.Key.SubsTierName)
	}
	if bundle.Key.IsSubsActive != nil {
		storage.IsSubsActive = bundle.Key.IsSubsActive
	}
	// Meta's device response omits expiry and rejects refresh_token grants:
	// the minted subscription key is durable. Leave Expired empty (never).
	return storage
}

// CombinedCredential is the oh-my-pi compatible JSON credential shape.
type CombinedCredential struct {
	OAuthAccessToken string `json:"oauthAccessToken"`
	APIKey           string `json:"apiKey"`
}

// ParseCombinedCredential parses a JSON-encoded {oauthAccessToken, apiKey} credential.
func ParseCombinedCredential(value string) (CombinedCredential, error) {
	var cred CombinedCredential
	value = strings.TrimSpace(value)
	if value == "" {
		return cred, fmt.Errorf("muse credential is empty; sign in again")
	}
	if err := json.Unmarshal([]byte(value), &cred); err != nil {
		return cred, fmt.Errorf("muse credential is invalid; sign in again: %w", err)
	}
	cred.OAuthAccessToken = strings.TrimSpace(cred.OAuthAccessToken)
	cred.APIKey = strings.TrimSpace(cred.APIKey)
	if cred.OAuthAccessToken == "" || cred.APIKey == "" {
		return cred, fmt.Errorf("muse credential is invalid; sign in again")
	}
	return cred, nil
}

// EncodeCombinedCredential encodes the oh-my-pi compatible JSON credential shape.
func EncodeCombinedCredential(oauthAccessToken, apiKey string) string {
	raw, _ := json.Marshal(CombinedCredential{
		OAuthAccessToken: strings.TrimSpace(oauthAccessToken),
		APIKey:           strings.TrimSpace(apiKey),
	})
	return string(raw)
}

// ResolveMuseAPIKey extracts the minted subscription Model API key from auth metadata/attributes.
// It supports the native muse file shape (muse_api_key) and the combined JSON shape.
func ResolveMuseAPIKey(metadata map[string]any, attributes map[string]string) string {
	if metadata != nil {
		for _, key := range []string{"muse_api_key", "museApiKey", "api_key_minted", "minted_api_key"} {
			if v, ok := metadata[key].(string); ok && strings.TrimSpace(v) != "" {
				return strings.TrimSpace(v)
			}
		}
		if v, ok := metadata["access_token"].(string); ok && strings.TrimSpace(v) != "" {
			trimmed := strings.TrimSpace(v)
			if strings.HasPrefix(trimmed, "{") {
				if cred, err := ParseCombinedCredential(trimmed); err == nil {
					return cred.APIKey
				}
			}
		}
		// Direct Meta subscription key stored flat under api_key (imported
		// credential). Only the known LLM| minted-key shape is accepted here;
		// anything else belongs in an openai-compatibility entry, not a muse file.
		if v, ok := metadata["api_key"].(string); ok && strings.TrimSpace(v) != "" {
			candidate := strings.TrimSpace(v)
			if strings.HasPrefix(candidate, "LLM|") {
				return candidate
			}
		}
	}
	if attributes != nil {
		if v := strings.TrimSpace(attributes["muse_api_key"]); v != "" && !strings.HasPrefix(v, "{") {
			return v
		}
		if v := strings.TrimSpace(attributes["api_key"]); strings.HasPrefix(v, "LLM|") {
			return v
		}
	}
	return ""
}

// ResolveMuseOAuthToken extracts the Meta account OAuth token for key refresh/usage probes.
func ResolveMuseOAuthToken(metadata map[string]any, attributes map[string]string) string {
	if metadata != nil {
		if v, ok := metadata["access_token"].(string); ok && strings.TrimSpace(v) != "" {
			trimmed := strings.TrimSpace(v)
			if strings.HasPrefix(trimmed, "{") {
				if cred, err := ParseCombinedCredential(trimmed); err == nil {
					return cred.OAuthAccessToken
				}
				return ""
			}
			return trimmed
		}
		if v, ok := metadata["oauth_access_token"].(string); ok && strings.TrimSpace(v) != "" {
			return strings.TrimSpace(v)
		}
	}
	if attributes != nil {
		if v := strings.TrimSpace(attributes["access_token"]); v != "" {
			return v
		}
	}
	return ""
}

// AccountIDForKey derives a stable account identity from the key response.
func AccountIDForKey(key *MuseKeyResponse, fallbackEmail string) string {
	if key != nil {
		if id := strings.TrimSpace(key.UserID); id != "" {
			return id
		}
		if email := strings.ToLower(strings.TrimSpace(key.UserEmail)); email != "" {
			return email
		}
	}
	return strings.ToLower(strings.TrimSpace(fallbackEmail))
}
