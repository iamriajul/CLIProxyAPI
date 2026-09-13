package muse

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/router-for-me/CLIProxyAPI/v7/internal/misc"
	log "github.com/sirupsen/logrus"
)

// TokenStorage stores Muse OAuth credentials on disk.
type TokenStorage struct {
	Type          string `json:"type"`
	AccessToken   string `json:"access_token"`
	RefreshToken  string `json:"refresh_token,omitempty"`
	TokenType     string `json:"token_type,omitempty"`
	MuseAPIKey    string `json:"muse_api_key,omitempty"`
	Email         string `json:"email,omitempty"`
	UserID        string `json:"user_id,omitempty"`
	SubsTierID    string `json:"subs_tier_id,omitempty"`
	SubsTierName  string `json:"subs_tier_name,omitempty"`
	IsSubsActive  *bool  `json:"is_subs_active,omitempty"`
	Expired       string `json:"expired,omitempty"`
	LastRefresh   string `json:"last_refresh,omitempty"`
	BaseURL       string `json:"base_url,omitempty"`
	AuthKind      string `json:"auth_kind,omitempty"`
	TokenEndpoint string `json:"token_endpoint,omitempty"`

	Metadata map[string]any `json:"-"`
}

// SetMetadata allows the token store to merge status fields before saving.
func (ts *TokenStorage) SetMetadata(meta map[string]any) {
	ts.Metadata = meta
}

// SaveTokenToFile writes Muse credentials to a JSON auth file.
func (ts *TokenStorage) SaveTokenToFile(authFilePath string) error {
	misc.LogSavingCredentials(authFilePath)
	ts.Type = "muse"
	if strings.TrimSpace(ts.AuthKind) == "" {
		ts.AuthKind = "oauth"
	}
	if strings.TrimSpace(ts.BaseURL) == "" {
		ts.BaseURL = MuseAPIBaseURL
	}
	if errMkdirAll := os.MkdirAll(filepath.Dir(authFilePath), 0o700); errMkdirAll != nil {
		return fmt.Errorf("muse token storage: create directory: %w", errMkdirAll)
	}

	data, errMerge := misc.MergeMetadata(ts, ts.Metadata)
	if errMerge != nil {
		return fmt.Errorf("muse token storage: merge metadata: %w", errMerge)
	}

	file, err := os.Create(authFilePath)
	if err != nil {
		return fmt.Errorf("muse token storage: create token file: %w", err)
	}
	defer func() {
		if errClose := file.Close(); errClose != nil {
			log.Errorf("muse token storage: close token file error: %v", errClose)
		}
	}()

	encoder := json.NewEncoder(file)
	encoder.SetIndent("", "  ")
	if err = encoder.Encode(data); err != nil {
		return fmt.Errorf("muse token storage: write token file: %w", err)
	}
	return nil
}

// IsExpired reports whether the credential needs a refresh.
// Muse subscription keys are durable (no expiry); only an explicit expired
// timestamp can mark them stale. Missing expiry means never expires.
func (ts *TokenStorage) IsExpired() bool {
	if ts == nil || strings.TrimSpace(ts.Expired) == "" {
		return false
	}
	t, err := time.Parse(time.RFC3339, strings.TrimSpace(ts.Expired))
	if err != nil {
		return true
	}
	return time.Now().After(t)
}

// CredentialFileName returns the filename used for Muse credentials.
func CredentialFileName(email, userID string) string {
	email = sanitizeFileSegment(strings.ToLower(strings.TrimSpace(email)))
	if email != "" {
		return fmt.Sprintf("muse-%s.json", email)
	}
	userID = sanitizeFileSegment(userID)
	if userID != "" {
		return fmt.Sprintf("muse-%s.json", userID)
	}
	return fmt.Sprintf("muse-%d.json", time.Now().UnixMilli())
}

func sanitizeFileSegment(value string) string {
	value = strings.TrimSpace(value)
	if value == "" {
		return ""
	}
	var b strings.Builder
	for _, r := range value {
		switch {
		case r >= 'a' && r <= 'z':
			b.WriteRune(r)
		case r >= 'A' && r <= 'Z':
			b.WriteRune(r)
		case r >= '0' && r <= '9':
			b.WriteRune(r)
		case r == '@' || r == '.' || r == '_' || r == '-':
			b.WriteRune(r)
		default:
			b.WriteRune('-')
		}
	}
	return strings.Trim(b.String(), "-")
}
