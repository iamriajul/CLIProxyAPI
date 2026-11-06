package auth

import (
	"context"
	"errors"
	"strings"
	"time"

	"github.com/router-for-me/CLIProxyAPI/v8/internal/registry"
	cliproxyexecutor "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/executor"
)

// poolEarliestRecovery reports the earliest instant at which any credential
// able to serve routeModel recovers, counting credentials already attempted by
// this request. It reports false unless the pool has at least two such
// credentials and every one of them is blocked: a single credential keeps its
// own upstream hint, and a usable credential means the pool is not exhausted.
// Credentials that will never recover on their own (disabled, unauthorized,
// expired token) are skipped and cannot set the instant.
func (m *Manager) poolEarliestRecovery(ctx context.Context, providers []string, routeModel string, opts cliproxyexecutor.Options) (time.Time, bool) {
	if m == nil || strings.TrimSpace(routeModel) == "" {
		return time.Time{}, false
	}
	providerSet := make(map[string]struct{}, len(providers))
	for _, provider := range providers {
		if key := canonicalSchedulingProvider(provider); key != "" {
			providerSet[key] = struct{}{}
		}
	}
	pinnedAuthID := pinnedAuthIDFromMetadata(opts.Metadata)
	eligibility := authSelectionEligibilityForRequest(ctx, opts)
	registryRef := registry.GetGlobalRegistry()
	now := time.Now()

	m.mu.RLock()
	defer m.mu.RUnlock()
	servable := 0
	var earliest time.Time
	for _, candidate := range m.auths {
		if candidate == nil || candidate.Disabled {
			continue
		}
		if pinnedAuthID != "" && candidate.ID != pinnedAuthID {
			continue
		}
		if !eligibility.allows(candidate) {
			continue
		}
		if _, ok := providerSet[canonicalSchedulingProvider(executorKeyFromAuth(candidate))]; !ok {
			continue
		}
		if !m.authSupportsRouteModel(registryRef, candidate, routeModel) {
			continue
		}
		servable++
		blocked, _, next := isAuthBlockedForModelForRequest(ctx, candidate, m.selectionModelForAuth(candidate, routeModel), now, opts.Headers)
		if !blocked {
			return time.Time{}, false
		}
		if !next.After(now) {
			continue
		}
		if earliest.IsZero() || next.Before(earliest) {
			earliest = next
		}
	}
	if servable < 2 || earliest.IsZero() {
		return time.Time{}, false
	}
	return earliest.UTC(), true
}

// withPoolRecoveryTime stamps the error returned when a request gives up with
// the pool's earliest recovery instant. It falls back to the pick error's own
// instant, which only covers credentials not yet attempted. Home mode owns its
// pool state remotely, so it keeps the pick error's instant.
func (m *Manager) withPoolRecoveryTime(ctx context.Context, providers []string, routeModel string, opts cliproxyexecutor.Options, err, errPick error) error {
	if !m.HomeEnabled() {
		if rec, ok := m.poolEarliestRecovery(ctx, providers, routeModel, opts); ok {
			return WithErrorRecoveryTime(err, rec)
		}
	}
	if rec, ok := EarliestRecoveryTime(errPick); ok && !rec.IsZero() {
		return WithErrorRecoveryTime(err, rec)
	}
	if rec, ok := upstreamRetryAfterRecovery(err); ok {
		return WithErrorRecoveryTime(err, rec)
	}
	return err
}

// upstreamRetryAfterRecovery reads the upstream's own retry hint off the last
// attempt. It is consulted only when the conductor gives up, so the proxy
// reports the instant it routed by; a raw executor error is never rewritten.
func upstreamRetryAfterRecovery(err error) (time.Time, bool) {
	var hint interface{ RetryAfter() *time.Duration }
	if errors.As(err, &hint) && hint != nil {
		if ra := hint.RetryAfter(); ra != nil && *ra > 0 {
			return time.Now().Add(*ra).UTC(), true
		}
	}
	return time.Time{}, false
}
