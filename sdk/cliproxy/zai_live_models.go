package cliproxy

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"sync"
	"time"

	zaiauth "github.com/router-for-me/CLIProxyAPI/v8/internal/auth/zai"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/registry"
	coreauth "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/auth"
	"github.com/router-for-me/CLIProxyAPI/v8/sdk/proxyutil"
	log "github.com/sirupsen/logrus"
)

// Z.AI answers a rejected plan key with HTTP 200 and an error body
// ({"code":401,"msg":"token expired or incorrect","success":false}), so the
// discovery path never treats a 200 as proof of success: the payload shape
// decides, through registry.ConvertZaiLiveModelsCatalog.

const (
	// zaiLiveModelsPath is the plan-scoped discovery path on the Z.AI origin.
	zaiLiveModelsPath = "/api/v1/models"

	// zaiLiveModelsProbeTimeout bounds one discovery round trip. This is
	// credential acquisition against a catalog endpoint, not a request on an
	// established upstream connection, so it is bounded the same way the
	// Antigravity capability probe it parallels is.
	zaiLiveModelsProbeTimeout = 10 * time.Second

	// zaiLiveModelsMaxSize caps the discovery payload. The response is one
	// entry per plan lane, not a full catalog.
	zaiLiveModelsMaxSize = 1 << 20

	// zaiLiveModelsCacheTTL is how long one credential's discovered lanes are
	// reused before the refresh is due. The Z.AI coding plan ships a fixed
	// roster, so this only has to be short enough that a plan change shows up
	// promptly, and long enough that a periodic re-registration is not a
	// network round trip.
	zaiLiveModelsCacheTTL = time.Hour

	// zaiLiveModelsAuthFailureTTL suppresses discovery for a credential Z.AI
	// rejected. Without it every re-registration would spend one request to be
	// told the same key is still bad.
	zaiLiveModelsAuthFailureTTL = 15 * time.Minute

	// zaiLiveModelsFailureTTL is the shorter suppression for a transport or
	// payload fault, which is far likelier to clear on its own.
	zaiLiveModelsFailureTTL = time.Minute
)

var (
	// zaiLiveModelsNowFunc is the clock, overridable in tests so TTL behavior
	// is deterministic without wall-clock sleeps.
	zaiLiveModelsNowFunc = time.Now

	// zaiLiveModelsMu guards the suppression ledger.
	zaiLiveModelsMu sync.Mutex

	// zaiLiveModelsBlockedUntil records, per credential digest, how long
	// discovery stays off after a failed attempt. Keyed by digest so neither
	// the auth ID nor the key is retained here; the successful result and its
	// freshness live in the registry store.
	zaiLiveModelsBlockedUntil = make(map[string]time.Time)
)

// zaiLiveModelKeyID identifies one Z.AI plan credential for the discovery
// cache. It is the auth ID plus a digest of the key, so re-minting a key for
// the same auth never serves the previous key's lanes.
func zaiLiveModelKeyID(auth *coreauth.Auth) (string, string) {
	if auth == nil {
		return "", ""
	}
	apiKey := zaiauth.ZaiCreds(auth.Metadata, auth.Attributes)
	keyID := registry.GetZaiLiveModelsCacheKey(auth.ID, apiKey)
	return keyID, apiKey
}

// zaiLiveModelDigest derives the ledger key from a discovery cache key.
func zaiLiveModelDigest(keyID string) string {
	digest := sha256.Sum256([]byte(keyID))
	return hex.EncodeToString(digest[:])
}

// zaiLiveModelsFetchURL resolves the discovery endpoint for a credential. A
// credential's own base URL wins, so a proxy or a non-default Z.AI deployment
// is honored; the documented origin is used when the attribute is absent.
func zaiLiveModelsFetchURL(auth *coreauth.Auth) string {
	origin := zaiauth.ZaiAPIBaseURL
	if auth != nil {
		base := strings.TrimSpace(auth.Attributes["base_url"])
		if base == "" {
			if value, ok := auth.Metadata["base_url"].(string); ok {
				base = strings.TrimSpace(value)
			}
		}
		if parsed, ok := zaiOriginFromBaseURL(base); ok {
			origin = parsed
		}
	}
	return origin + zaiLiveModelsPath
}

// zaiOriginFromBaseURL reduces a Z.AI base URL to its origin. The Anthropic and
// OpenAI-coding base URLs live on the same origin as discovery, so a credential
// recorded against either resolves to the same endpoint.
func zaiOriginFromBaseURL(raw string) (string, bool) {
	trimmed := strings.TrimRight(strings.TrimSpace(raw), "/")
	scheme, rest, found := strings.Cut(trimmed, "://")
	if !found || (scheme != "http" && scheme != "https") {
		return "", false
	}
	host := rest
	if slash := strings.Index(rest, "/"); slash >= 0 {
		host = rest[:slash]
	}
	if host == "" {
		return "", false
	}
	return scheme + "://" + host, true
}

// zaiLiveModelsProxyURL resolves the proxy a discovery request should use,
// preferring the credential's own proxy over the global one — the same
// precedence the Antigravity capability fetch uses.
func (s *Service) zaiLiveModelsProxyURL(auth *coreauth.Auth) string {
	if auth != nil {
		if proxyURL := strings.TrimSpace(auth.ProxyURL); proxyURL != "" {
			return proxyURL
		}
	}
	if s == nil {
		return ""
	}
	s.cfgMu.RLock()
	defer s.cfgMu.RUnlock()
	if s.cfg != nil {
		return strings.TrimSpace(s.cfg.ProxyURL)
	}
	return ""
}

// zaiLiveModelSuppressed reports whether discovery should be skipped: either
// a recent attempt failed and is still in effect, or the stored lanes are
// still fresh.
func zaiLiveModelSuppressed(keyID, digest string) bool {
	now := zaiLiveModelsNowFunc()
	zaiLiveModelsMu.Lock()
	until, blocked := zaiLiveModelsBlockedUntil[digest]
	zaiLiveModelsMu.Unlock()
	if blocked {
		if now.Before(until) {
			return true
		}
		zaiLiveModelsMu.Lock()
		delete(zaiLiveModelsBlockedUntil, digest)
		zaiLiveModelsMu.Unlock()
	}
	fetchedAt, ok := registry.ZaiLiveModelsFetchedAt(keyID)
	return ok && now.Before(fetchedAt.Add(zaiLiveModelsCacheTTL))
}

// zaiLiveModelBlock suppresses discovery for one credential digest.
func zaiLiveModelBlock(digest string, ttl time.Duration) {
	zaiLiveModelsMu.Lock()
	defer zaiLiveModelsMu.Unlock()
	zaiLiveModelsBlockedUntil[digest] = zaiLiveModelsNowFunc().Add(ttl)
}

// resetZaiLiveModelsLedgerForTest clears the suppression ledger. Tests only.
func resetZaiLiveModelsLedgerForTest() {
	zaiLiveModelsMu.Lock()
	defer zaiLiveModelsMu.Unlock()
	zaiLiveModelsBlockedUntil = make(map[string]time.Time)
}

// zaiLiveModelsRoundTrip fetches and converts one credential's plan catalog.
//
// A credential with no key never reaches the network: there is nothing to ask,
// and returning "no lanes" would unregister the lane instead of leaving it on
// the offline snapshot.
func (s *Service) zaiLiveModelsRoundTrip(ctx context.Context, auth *coreauth.Auth, apiKey string) ([]*registry.ModelInfo, error) {
	if strings.TrimSpace(apiKey) == "" {
		return nil, registry.ErrZaiLiveModelsNoCredential
	}
	probeCtx, cancel := context.WithTimeout(ctx, zaiLiveModelsProbeTimeout)
	defer cancel()

	req, errReq := http.NewRequestWithContext(probeCtx, http.MethodGet, zaiLiveModelsFetchURL(auth), nil)
	if errReq != nil {
		return nil, fmt.Errorf("zai live model discovery: create request: %w", errReq)
	}
	req.Header.Set("Accept", "application/json")
	// Z.AI rejects the Bearer prefix here, the same reason the Anthropic lanes
	// run natively instead of through the shared Claude delegation. The key
	// goes out verbatim, as it does on inference and quota.
	req.Header.Set("Authorization", apiKey)

	client := &http.Client{Timeout: zaiLiveModelsProbeTimeout}
	if proxyURL := s.zaiLiveModelsProxyURL(auth); proxyURL != "" {
		if transport, _, errProxy := proxyutil.BuildHTTPTransport(proxyURL); errProxy == nil && transport != nil {
			client.Transport = transport
		}
	}
	resp, errDo := client.Do(req)
	if errDo != nil {
		return nil, fmt.Errorf("zai live model discovery: request failed: %w", errDo)
	}
	defer func() {
		if errClose := resp.Body.Close(); errClose != nil {
			log.Debugf("zai live model discovery: close response body: %v", errClose)
		}
	}()
	body, errRead := io.ReadAll(io.LimitReader(resp.Body, zaiLiveModelsMaxSize))
	if errRead != nil {
		return nil, fmt.Errorf("zai live model discovery: read response: %w", errRead)
	}
	// The body is inspected on every status. Z.AI puts its errors in the
	// payload, so a coarse status check would discard a real error body and a
	// payload check would accept a 200 that carries one.
	models, errConvert := registry.ConvertZaiLiveModelsCatalog(body)
	if errConvert == nil {
		return models, nil
	}
	if resp.StatusCode >= http.StatusOK && resp.StatusCode < http.StatusMultipleChoices {
		return nil, errConvert
	}
	var credentialErr *registry.ZaiLiveModelError
	if errors.As(errConvert, &credentialErr) {
		credentialErr.Status = resp.StatusCode
		return nil, credentialErr
	}
	return nil, &registry.ZaiLiveModelError{Code: resp.StatusCode, Msg: string(body), Status: resp.StatusCode}
}

// RefreshZaiLiveModelsForAuth refreshes one Z.AI credential's plan catalog
// and reports whether its lane set changed, which is the caller's cue to
// re-register that credential's models.
//
// The call is synchronous, unlike the Antigravity capability probe: that probe
// is best-effort enrichment of a model list that is already correct, whereas a
// stale lane list is the defect being fixed here. Registration blocks on the
// answer rather than publishing something already known to be a guess.
func (s *Service) RefreshZaiLiveModelsForAuth(ctx context.Context, auth *coreauth.Auth) (bool, error) {
	keyID, apiKey := zaiLiveModelKeyID(auth)
	if keyID == "" {
		// No credential in hand: there is nothing to discover, and the lane
		// stays on the offline snapshot rather than being emptied.
		return false, registry.ErrZaiLiveModelsNoCredential
	}
	digest := zaiLiveModelDigest(keyID)
	if zaiLiveModelSuppressed(keyID, digest) {
		return false, nil
	}
	probeCtx := ctx
	if probeCtx == nil {
		probeCtx = context.Background()
	}
	models, errRound := s.zaiLiveModelsRoundTrip(probeCtx, auth, apiKey)
	if errRound != nil {
		ttl := zaiLiveModelsFailureTTL
		var credentialErr *registry.ZaiLiveModelError
		if errors.As(errRound, &credentialErr) && credentialErr.Credential() {
			ttl = zaiLiveModelsAuthFailureTTL
			log.Warnf("zai live model discovery: credential rejected, keeping previous lanes: %v", errRound)
		} else {
			log.Warnf("zai live model discovery: keeping previous lanes: %v", errRound)
		}
		zaiLiveModelBlock(digest, ttl)
		return false, errRound
	}
	return registry.SetZaiLiveModels(keyID, models), nil
}

// discoverZaiLiveModels refreshes one Z.AI credential's plan catalog and, when
// the lane set changed, re-registers that credential's models so new lanes
// reach clients without waiting for the next periodic registration.
func (s *Service) discoverZaiLiveModels(ctx context.Context, auth *coreauth.Auth) {
	if s == nil || auth == nil {
		return
	}
	changed, errRefresh := s.RefreshZaiLiveModelsForAuth(ctx, auth)
	if errRefresh != nil || !changed {
		return
	}
	log.Infof("zai: plan lanes changed for credential %s; re-registering its models", auth.ID)
	// Re-run registration directly rather than through
	// completeModelRegistrationForAuth: the scheduler and state reconciliation
	// that helper adds are no-ops for an auth whose registry entry is already
	// populated, and discovery must also work for a Service without an auth
	// manager. It cannot loop — the TTL means this runs once per credential per
	// hour, and the re-registration observes already-fresh lanes, so it finds no
	// change left to report.
	s.registerModelsForAuthWithCache(ctx, auth, nil)
}
