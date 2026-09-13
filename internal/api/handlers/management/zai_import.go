package management

import (
	"context"
	"net/http"
	"strings"
	"time"

	"github.com/gin-gonic/gin"
	zaiauth "github.com/router-for-me/CLIProxyAPI/v7/internal/auth/zai"
	coreauth "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/auth"
)

// ImportZaiKey validates a pasted Z.AI dashboard API key and saves it as an
// auth record, so keys can be added completely from the management UI without
// the browser OAuth flow. The key is the same id.secret shape the OAuth flow
// provisions, so inference, routing, and quota behave identically.
func (h *Handler) ImportZaiKey(c *gin.Context) {
	if h == nil || h.cfg == nil {
		c.JSON(http.StatusServiceUnavailable, gin.H{"error": "config unavailable"})
		return
	}
	if h.cfg.AuthDir == "" {
		c.JSON(http.StatusServiceUnavailable, gin.H{"error": "auth directory not configured"})
		return
	}

	var payload struct {
		APIKey string `json:"api_key"`
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

	ctx := PopulateAuthContext(context.Background(), c)
	if err := zaiauth.NewZaiAuth(h.cfg).ValidateKey(ctx, apiKey); err != nil {
		c.JSON(http.StatusBadGateway, gin.H{"error": "key validation failed", "message": err.Error()})
		return
	}

	storage := &zaiauth.TokenStorage{
		AccessToken: apiKey,
		AuthKind:    "apikey",
	}
	fileName := zaiauth.CredentialFileName("", "")
	now := time.Now().UnixMilli()
	record := &coreauth.Auth{
		ID:       fileName,
		Provider: "zai",
		FileName: fileName,
		Label:    "Z.AI",
		Storage:  storage,
		Metadata: map[string]any{
			"type":         "zai",
			"access_token": apiKey,
			"api_key":      apiKey,
			"base_url":     zaiauth.ZaiAnthropicBaseURL,
			"auth_kind":    "apikey",
			"timestamp":    now,
		},
		Attributes: map[string]string{
			"auth_kind": "apikey",
			"base_url":  zaiauth.ZaiAnthropicBaseURL,
		},
	}
	savedPath, errSave := h.saveTokenRecord(ctx, record)
	if errSave != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "failed to save key", "message": errSave.Error()})
		return
	}
	c.JSON(http.StatusOK, gin.H{"status": "ok", "auth_file": savedPath, "file": fileName})
}
