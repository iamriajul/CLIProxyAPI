package auth

import (
	"context"
	"fmt"
	"strings"
	"time"

	museauth "github.com/router-for-me/CLIProxyAPI/v7/internal/auth/muse"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/browser"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/config"
	coreauth "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/auth"
	log "github.com/sirupsen/logrus"
)

// MuseAuthenticator implements the Muse Code OAuth device-code flow.
type MuseAuthenticator struct{}

// NewMuseAuthenticator constructs a new Muse authenticator.
func NewMuseAuthenticator() Authenticator {
	return &MuseAuthenticator{}
}

// Provider returns the provider key for Muse.
func (MuseAuthenticator) Provider() string {
	return "muse"
}

// RefreshLead returns nil: Muse subscription keys are durable and Meta rejects
// refresh_token grants, so no proactive refresh should be scheduled.
func (MuseAuthenticator) RefreshLead() *time.Duration {
	return nil
}

// Login launches the OAuth device-code flow to obtain Muse tokens and mints the subscription key.
func (a MuseAuthenticator) Login(ctx context.Context, cfg *config.Config, opts *LoginOptions) (*coreauth.Auth, error) {
	if cfg == nil {
		return nil, fmt.Errorf("cliproxy auth: configuration is required")
	}
	if ctx == nil {
		ctx = context.Background()
	}
	if opts == nil {
		opts = &LoginOptions{}
	}

	authSvc := museauth.NewMuseAuth(cfg)

	fmt.Println("Starting Muse authentication...")
	deviceCode, err := authSvc.StartDeviceFlow(ctx)
	if err != nil {
		return nil, fmt.Errorf("muse: failed to start device flow: %w", err)
	}

	verificationURL := strings.TrimSpace(deviceCode.VerificationURIComplete)
	if verificationURL == "" {
		verificationURL = strings.TrimSpace(deviceCode.VerificationURI)
	}

	fmt.Printf("\nTo authenticate, please visit:\n%s\n\n", verificationURL)
	if deviceCode.UserCode != "" {
		fmt.Printf("Enter code: %s\n\n", deviceCode.UserCode)
	}

	if !opts.NoBrowser {
		if browser.IsAvailable() {
			if errOpen := browser.OpenURL(verificationURL); errOpen != nil {
				log.Warnf("Failed to open browser automatically: %v", errOpen)
			} else {
				fmt.Println("Browser opened automatically.")
			}
		} else {
			log.Warn("No browser available; please open the URL manually")
		}
	}

	fmt.Println("Waiting for authorization...")
	if deviceCode.ExpiresIn > 0 {
		fmt.Printf("(This will timeout in %d seconds if not authorized)\n", deviceCode.ExpiresIn)
	}

	bundle, errWait := authSvc.WaitForAuthorization(ctx, deviceCode)
	if errWait != nil {
		return nil, fmt.Errorf("muse: %w", errWait)
	}

	tokenStorage := authSvc.CreateTokenStorage(bundle)
	if tokenStorage == nil || strings.TrimSpace(tokenStorage.MuseAPIKey) == "" {
		return nil, fmt.Errorf("muse token storage missing subscription key")
	}

	fileName := museauth.CredentialFileName(tokenStorage.Email, tokenStorage.UserID)
	label := strings.TrimSpace(tokenStorage.Email)
	if label == "" {
		label = "Muse"
	}

	metadata := map[string]any{
		"type":          "muse",
		"access_token":  tokenStorage.AccessToken,
		"refresh_token": tokenStorage.RefreshToken,
		"token_type":    tokenStorage.TokenType,
		"muse_api_key":  tokenStorage.MuseAPIKey,
		"base_url":      museauth.MuseAPIBaseURL,
		"auth_kind":     "oauth",
	}
	if tokenStorage.Email != "" {
		metadata["email"] = tokenStorage.Email
	}
	if tokenStorage.UserID != "" {
		metadata["user_id"] = tokenStorage.UserID
	}
	if tokenStorage.SubsTierID != "" {
		metadata["subs_tier_id"] = tokenStorage.SubsTierID
	}
	if tokenStorage.SubsTierName != "" {
		metadata["subs_tier_name"] = tokenStorage.SubsTierName
	}
	if tokenStorage.IsSubsActive != nil {
		metadata["is_subs_active"] = *tokenStorage.IsSubsActive
	}

	fmt.Println("Muse authentication successful")

	return &coreauth.Auth{
		ID:       fileName,
		Provider: a.Provider(),
		FileName: fileName,
		Label:    label,
		Storage:  tokenStorage,
		Metadata: metadata,
		Attributes: map[string]string{
			"auth_kind": "oauth",
			"base_url":  museauth.MuseAPIBaseURL,
		},
	}, nil
}
