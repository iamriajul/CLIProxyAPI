package quota

import (
	"context"
	"errors"
	"strings"

	devinauth "github.com/router-for-me/CLIProxyAPI/v7/internal/auth/devin"
)

// DevinFetcher refreshes Devin quota via GetUserStatus, reusing the login
// flow's client. The empty device seed derives the fingerprint from the
// session token, exactly as at login.
type DevinFetcher struct{}

// NewDevinFetcher builds a DevinFetcher.
func NewDevinFetcher() *DevinFetcher { return &DevinFetcher{} }

// Provider returns the canonical provider key.
func (*DevinFetcher) Provider() string { return "devin" }

// Fetch returns daily/weekly remaining quota plus the plan, or an error.
func (*DevinFetcher) Fetch(ctx context.Context, req FetchRequest) (*Snapshot, error) {
	token := devinSessionToken(req)
	if token == "" {
		return nil, errors.New("devin quota fetch: missing session token")
	}
	service := devinauth.NewDevinAuthService(req.Client)
	if baseURL := devinBaseURL(req); baseURL != "" {
		service.SetServerBaseURL(baseURL)
	}
	status, err := service.FetchUserStatus(ctx, token, "")
	if err != nil {
		return nil, err
	}
	if status == nil {
		return nil, nil
	}
	return devinSnapshotFromStatus(status), nil
}

func devinSessionToken(req FetchRequest) string {
	if req.Auth == nil {
		return ""
	}
	if req.Auth.Metadata != nil {
		if token, _ := req.Auth.Metadata["session_token"].(string); strings.TrimSpace(token) != "" {
			return devinauth.FormatSessionToken(token)
		}
	}
	if req.Auth.Attributes != nil {
		for _, key := range []string{"session_token", "api_key"} {
			if token := strings.TrimSpace(req.Auth.Attributes[key]); token != "" {
				return devinauth.FormatSessionToken(token)
			}
		}
	}
	if req.Auth.Metadata != nil {
		if token, _ := req.Auth.Metadata["api_key"].(string); strings.TrimSpace(token) != "" {
			return devinauth.FormatSessionToken(token)
		}
	}
	return ""
}

func devinBaseURL(req FetchRequest) string {
	if req.Auth == nil {
		return ""
	}
	if req.Auth.Attributes != nil {
		if baseURL := strings.TrimSpace(req.Auth.Attributes["base_url"]); baseURL != "" {
			return baseURL
		}
	}
	if req.Auth.Metadata != nil {
		if baseURL, _ := req.Auth.Metadata["base_url"].(string); strings.TrimSpace(baseURL) != "" {
			return strings.TrimSpace(baseURL)
		}
	}
	return ""
}

func devinSnapshotFromStatus(status *devinauth.DevinUserStatus) *Snapshot {
	snapshot := &Snapshot{Plan: strings.TrimSpace(status.Plan), Windows: []Window{}}
	daily := Window{Name: "daily", UsedPercent: UsedPercent(float64(100 - status.DailyQuotaRemainingPercent))}
	if !status.DailyQuotaResetAt.IsZero() {
		at := status.DailyQuotaResetAt
		daily.ResetAt = &at
	}
	weekly := Window{Name: "weekly", UsedPercent: UsedPercent(float64(100 - status.WeeklyQuotaRemainingPercent))}
	if !status.WeeklyQuotaResetAt.IsZero() {
		at := status.WeeklyQuotaResetAt
		weekly.ResetAt = &at
	}
	snapshot.Windows = []Window{daily, weekly}
	return snapshot
}
