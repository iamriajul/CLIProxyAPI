package auth

import (
	"bufio"
	"context"
	"fmt"
	"os"
	"strings"
	"time"

	zaiauth "github.com/router-for-me/CLIProxyAPI/v7/internal/auth/zai"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/browser"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/config"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/misc"
	coreauth "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/auth"
	log "github.com/sirupsen/logrus"
)

// ZaiAuthenticator implements the Z.AI GLM Coding Plan browser OAuth flow.
type ZaiAuthenticator struct{}

// NewZaiAuthenticator constructs a new Z.AI authenticator.
func NewZaiAuthenticator() Authenticator {
	return &ZaiAuthenticator{}
}

// Provider returns the provider key for Z.AI.
func (ZaiAuthenticator) Provider() string {
	return "zai"
}

// RefreshLead returns nil: provisioned Z.AI keys are durable and never refresh.
func (ZaiAuthenticator) RefreshLead() *time.Duration {
	return nil
}

// Login launches the browser authorization-code flow with manual paste-back.
func (a ZaiAuthenticator) Login(ctx context.Context, cfg *config.Config, opts *LoginOptions) (*coreauth.Auth, error) {
	if cfg == nil {
		return nil, fmt.Errorf("cliproxy auth: configuration is required")
	}
	if ctx == nil {
		ctx = context.Background()
	}
	if opts == nil {
		opts = &LoginOptions{}
	}

	state, err := misc.GenerateRandomState()
	if err != nil {
		return nil, fmt.Errorf("zai: failed to generate state: %w", err)
	}
	redirectURI := zaiauth.RedirectURI()
	authURL := zaiauth.AuthorizeURL(state, redirectURI)

	fmt.Println("Starting Z.AI authentication...")
	fmt.Printf("\nTo authenticate, please visit:\n%s\n\n", authURL)
	fmt.Println("Complete the login, then paste the resulting zcode:// callback URL or bare code.")

	if !opts.NoBrowser {
		if browser.IsAvailable() {
			if errOpen := browser.OpenURL(authURL); errOpen != nil {
				log.Warnf("Failed to open browser automatically: %v", errOpen)
			} else {
				fmt.Println("Browser opened automatically.")
			}
		}
	}

	prompt := opts.Prompt
	if prompt == nil {
		reader := bufio.NewReader(os.Stdin)
		prompt = func(message string) (string, error) {
			fmt.Printf("%s: ", message)
			line, errRead := reader.ReadString('\n')
			if errRead != nil {
				return "", errRead
			}
			return strings.TrimSpace(line), nil
		}
	}
	pasted, err := prompt("Paste the callback URL or authorization code")
	if err != nil {
		return nil, fmt.Errorf("zai: failed to read callback: %w", err)
	}
	code, _, err := zaiauth.ParsePastedCallback(pasted, state)
	if err != nil {
		return nil, fmt.Errorf("zai: %w", err)
	}

	authSvc := zaiauth.NewZaiAuth(cfg)
	bundle, err := authSvc.WaitForAuthorization(ctx, code, state, redirectURI)
	if err != nil {
		return nil, fmt.Errorf("zai: %w", err)
	}

	tokenStorage := authSvc.CreateTokenStorage(bundle)
	if tokenStorage == nil || strings.TrimSpace(tokenStorage.AccessToken) == "" {
		return nil, fmt.Errorf("zai token storage missing api key")
	}

	fileName := zaiauth.CredentialFileName(tokenStorage.Email, tokenStorage.UserID)
	label := strings.TrimSpace(tokenStorage.Email)
	if label == "" {
		label = "Z.AI"
	}

	metadata := map[string]any{
		"type":         "zai",
		"access_token": tokenStorage.AccessToken,
		"base_url":     zaiauth.ZaiAnthropicBaseURL,
		"auth_kind":    "oauth",
	}
	if tokenStorage.Email != "" {
		metadata["email"] = tokenStorage.Email
	}
	if tokenStorage.UserID != "" {
		metadata["user_id"] = tokenStorage.UserID
	}

	fmt.Println("Z.AI authentication successful")

	return &coreauth.Auth{
		ID:       fileName,
		Provider: a.Provider(),
		FileName: fileName,
		Label:    label,
		Storage:  tokenStorage,
		Metadata: metadata,
		Attributes: map[string]string{
			"auth_kind": "oauth",
			"base_url":  zaiauth.ZaiAnthropicBaseURL,
		},
	}, nil
}
