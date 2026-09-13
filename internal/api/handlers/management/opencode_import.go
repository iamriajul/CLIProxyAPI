package management

import (
	"context"
	"net/http"
	"strings"
	"time"

	"github.com/gin-gonic/gin"
	opencodeauth "github.com/router-for-me/CLIProxyAPI/v7/internal/auth/opencode"
	coreauth "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/auth"
)

// ImportOpenCodeKey validates a pasted OpenCode Zen Go API key and saves it as
// an auth record, so keys can be added completely from the management UI.
func (h *Handler) ImportOpenCodeKey(c *gin.Context) {
	if h == nil || h.cfg == nil {
		c.JSON(http.StatusServiceUnavailable, gin.H{"error": "config unavailable"})
		return
	}
	if h.cfg.AuthDir == "" {
		c.JSON(http.StatusServiceUnavailable, gin.H{"error": "auth directory not configured"})
		return
	}

	var payload struct {
		APIKey  string `json:"api_key"`
		BaseURL string `json:"base_url"`
	}
	if err := c.ShouldBindJSON(&payload); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid request", "message": err.Error()})
		return
	}
	apiKey := strings.TrimSpace(payload.APIKey)
	if apiKey == "" {
		c.JSON(http.StatusBadRequest, gin.H{"error": "api_key is required"})
		return
	}
	baseURL := opencodeauth.NormalizeBaseURL(payload.BaseURL)

	ctx := PopulateAuthContext(context.Background(), c)
	if err := opencodeauth.NewOpenCodeAuth(h.cfg).ValidateKey(ctx, baseURL, apiKey); err != nil {
		c.JSON(http.StatusBadGateway, gin.H{"error": "key validation failed", "message": err.Error()})
		return
	}

	storage := &opencodeauth.TokenStorage{
		APIKey:  apiKey,
		BaseURL: baseURL,
		Label:   "OpenCode Go",
	}
	fileName := opencodeauth.CredentialFileName()
	now := time.Now().UnixMilli()
	record := &coreauth.Auth{
		ID:       fileName,
		Provider: "opencode",
		FileName: fileName,
		Label:    "OpenCode Go",
		Storage:  storage,
		Metadata: map[string]any{
			"type":         "opencode",
			"api_key":      apiKey,
			"access_token": apiKey,
			"base_url":     baseURL,
			"auth_kind":    "apikey",
			"timestamp":    now,
		},
		Attributes: map[string]string{
			"auth_kind": "apikey",
			"base_url":  baseURL,
		},
	}
	savedPath, errSave := h.saveTokenRecord(ctx, record)
	if errSave != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "failed to save key", "message": errSave.Error()})
		return
	}
	c.JSON(http.StatusOK, gin.H{"status": "ok", "auth_file": savedPath, "file": fileName})
}
