// Package opencode provides authentication and token management for OpenCode Zen Go.
package opencode

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/router-for-me/CLIProxyAPI/v7/internal/config"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/util"
	log "github.com/sirupsen/logrus"
)

const (
	// OpenCodeGoAPIBaseURL is the default OpenCode Zen Go gateway base URL.
	OpenCodeGoAPIBaseURL = "https://opencode.ai/zen/go/v1"
	// OpenCodeGoAuthURL is where users mint a Go API key after subscribing.
	OpenCodeGoAuthURL = "https://opencode.ai/auth"
	// OpenCodeGoUsagePath is the first-party usage endpoint, relative to the base.
	OpenCodeGoUsagePath = "/v1/usage"
	// OpenCodeGoUserAgent identifies CPA requests to the Zen gateway.
	OpenCodeGoUserAgent = "cli-proxy-api"

	httpClientTimeout = 30 * time.Second
)

// OpenCodeAuth performs OpenCode API key validation.
type OpenCodeAuth struct {
	httpClient *http.Client
}

// NewOpenCodeAuth creates an OpenCode helper using config proxy settings.
func NewOpenCodeAuth(cfg *config.Config) *OpenCodeAuth {
	return NewOpenCodeAuthWithProxyURL(cfg, "")
}

// NewOpenCodeAuthWithProxyURL creates an OpenCode helper with an explicit proxy URL.
func NewOpenCodeAuthWithProxyURL(cfg *config.Config, proxyURL string) *OpenCodeAuth {
	effectiveProxyURL := strings.TrimSpace(proxyURL)
	var sdkCfg config.SDKConfig
	if cfg != nil {
		sdkCfg = cfg.SDKConfig
		if effectiveProxyURL == "" {
			effectiveProxyURL = strings.TrimSpace(cfg.ProxyURL)
		}
	}
	sdkCfg.ProxyURL = effectiveProxyURL
	return &OpenCodeAuth{httpClient: util.SetProxy(&sdkCfg, &http.Client{Timeout: httpClientTimeout})}
}

// NormalizeBaseURL resolves the gateway base URL, tolerating a missing /v1 suffix.
func NormalizeBaseURL(raw string) string {
	trimmed := strings.TrimRight(strings.TrimSpace(raw), "/")
	if trimmed == "" {
		return OpenCodeGoAPIBaseURL
	}
	if strings.HasSuffix(strings.ToLower(trimmed), "/v1") {
		return trimmed
	}
	return trimmed + "/v1"
}

// UsageURL resolves the usage endpoint for a base URL.
func UsageURL(baseURL string) string {
	return strings.TrimRight(NormalizeBaseURL(baseURL), "/") + "/usage"
}

// ValidateKey checks an API key against the gateway models endpoint.
func (a *OpenCodeAuth) ValidateKey(ctx context.Context, baseURL, apiKey string) error {
	apiKey = strings.TrimSpace(apiKey)
	if apiKey == "" {
		return fmt.Errorf("opencode: api key is required")
	}
	if ctx == nil {
		ctx = context.Background()
	}
	url := strings.TrimRight(NormalizeBaseURL(baseURL), "/") + "/models"
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return fmt.Errorf("opencode: create validation request: %w", err)
	}
	req.Header.Set("Accept", "application/json")
	req.Header.Set("Authorization", "Bearer "+apiKey)
	req.Header.Set("User-Agent", OpenCodeGoUserAgent)

	resp, err := a.httpClient.Do(req)
	if err != nil {
		return fmt.Errorf("opencode: validation request failed: %w", err)
	}
	defer func() {
		if errClose := resp.Body.Close(); errClose != nil {
			log.Errorf("opencode: close validation body error: %v", errClose)
		}
	}()
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return fmt.Errorf("opencode: read validation response: %w", err)
	}
	if resp.StatusCode == http.StatusUnauthorized || resp.StatusCode == http.StatusForbidden {
		return fmt.Errorf("opencode: api key rejected (status %d)", resp.StatusCode)
	}
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("opencode: validation failed with status %d: %s", resp.StatusCode, strings.TrimSpace(string(body)))
	}
	var payload struct {
		Data []struct {
			ID string `json:"id"`
		} `json:"data"`
	}
	if err = json.Unmarshal(body, &payload); err != nil {
		return fmt.Errorf("opencode: parse validation response: %w", err)
	}
	if len(payload.Data) == 0 {
		return fmt.Errorf("opencode: gateway returned no models")
	}
	return nil
}

// OpenCodeCreds extracts the Zen Go API key from auth metadata/attributes.
func OpenCodeCreds(metadata map[string]any, attributes map[string]string) string {
	if metadata != nil {
		for _, key := range []string{"api_key", "access_token", "apikey"} {
			if v, ok := metadata[key].(string); ok && strings.TrimSpace(v) != "" {
				return strings.TrimSpace(v)
			}
		}
	}
	if attributes != nil {
		for _, key := range []string{"api_key", "access_token"} {
			if v := strings.TrimSpace(attributes[key]); v != "" {
				return v
			}
		}
	}
	return ""
}

// ResolveBaseURL extracts an explicit gateway base URL override, if any.
func ResolveBaseURL(metadata map[string]any, attributes map[string]string) string {
	if metadata != nil {
		if v, ok := metadata["base_url"].(string); ok && strings.TrimSpace(v) != "" {
			return strings.TrimSpace(v)
		}
	}
	if attributes != nil {
		if v := strings.TrimSpace(attributes["base_url"]); v != "" {
			return v
		}
	}
	return ""
}
