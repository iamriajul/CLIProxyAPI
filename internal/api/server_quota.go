package api

import (
	"net/http"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/gin-gonic/gin"
	codexauth "github.com/router-for-me/CLIProxyAPI/v7/internal/auth/codex"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/quota"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/registry"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/thinking"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/util"
	coreauth "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/auth"
)

// inferenceQuotaAccount is one serving account in GET /v1/quota?model=<id>.
// The response body is a JSON array of these, sorted by provider then name.
//
// Compatibility policy (this endpoint is consumed by external integrations):
//   - The route, the "model" query parameter, and every non-omitempty field
//     are stable under /v1. Changes are additive only: new optional fields
//     may appear, but existing fields never change type or meaning.
//   - "windows" is always an array, never null (possibly empty).
//   - "omitempty" fields may be absent; clients MUST tolerate absence.
//   - Errors are {"error": string} with HTTP semantics (400 missing model,
//     401 bad key, 404 no serving account, 503 auth manager unavailable),
//     matching the v1 authentication middleware envelope on this endpoint.
//   - Data is live-first per account (provider quota APIs, 60s TTL cache,
//     stale fallback on errors), degrading to passive local snapshots when
//     live is unavailable. ?refresh=live forces a fetch; ?refresh=cached
//     serves passive snapshots only. windows_observed_at reports which.
//   - "windows" is the stable normalized usage view: entry structure and
//     well-known names ("5h", "7d", "monthly", "daily", "weekly") are
//     guaranteed; namespaced names ("<group>/5h") carry upstream slugs.
//   - NOT guaranteed: plan strings, status vocabulary behind in_cooldown,
//     and masking affix lengths. "description" is reserved for future use
//     and currently never present.
//   - Grouping (à la CPAMC provider tabs) is done client-side on "provider",
//     which carries the raw provider key (gemini/vertex/aistudio stay
//     distinct, exactly as CPAMC badges them).
//     The machine-readable contract lives in api/v1-quota.schema.json
//     and is enforced by TestInferenceQuotaSchemaParity.
type inferenceQuotaAccount struct {
	Provider          string                 `json:"provider"`
	Name              string                 `json:"name,omitempty"`
	Type              string                 `json:"type"`
	Plan              string                 `json:"plan,omitempty"`
	Description       string                 `json:"description,omitempty"`
	InCooldown        bool                   `json:"in_cooldown"`
	WindowsObservedAt *time.Time             `json:"windows_observed_at,omitempty"`
	Windows           []inferenceQuotaWindow `json:"windows"`
}

// handleInferenceQuota serves live-first quota snapshots for the accounts serving
// a model. It is authenticated with the inference API key (v1 group
// middleware), fans out to provider quota APIs with per-account passive
// fallback, and masks account identity.
// Operator-disabled accounts are excluded; anything else is returned with an
// in_cooldown flag when effectively blocked.
func (s *Server) handleInferenceQuota(c *gin.Context) {
	requested := strings.TrimSpace(c.Query("model"))
	if requested == "" {
		requested = strings.TrimSpace(c.Query("model_id"))
	}
	if requested == "" {
		c.JSON(http.StatusBadRequest, gin.H{"error": "model query parameter is required"})
		return
	}
	if s == nil || s.handlers == nil || s.handlers.AuthManager == nil {
		c.JSON(http.StatusServiceUnavailable, gin.H{"error": "auth manager unavailable"})
		return
	}

	model := requested
	if parsed := thinking.ParseSuffix(requested); strings.TrimSpace(parsed.ModelName) != "" {
		model = strings.TrimSpace(parsed.ModelName)
	}

	providers := util.GetProviderName(model)
	if len(providers) == 0 && model != requested {
		providers = util.GetProviderName(requested)
	}
	providerSet := make(map[string]struct{}, len(providers))
	for _, provider := range providers {
		if key := strings.ToLower(strings.TrimSpace(provider)); key != "" {
			providerSet[key] = struct{}{}
		}
	}

	registryRef := registry.GetGlobalRegistry()
	now := time.Now()
	mode := quota.ParseRefreshMode(c.Query("refresh"))
	matched := make([]*coreauth.Auth, 0)
	for _, auth := range s.handlers.AuthManager.List() {
		if auth == nil || auth.Disabled || auth.Status == coreauth.StatusDisabled {
			continue
		}
		if len(providerSet) > 0 {
			if _, ok := providerSet[strings.ToLower(strings.TrimSpace(auth.Provider))]; !ok {
				continue
			}
		}
		if registryRef != nil && !registryRef.ClientSupportsModel(auth.ID, model) {
			continue
		}
		matched = append(matched, auth)
	}
	if len(matched) == 0 {
		c.JSON(http.StatusNotFound, gin.H{"error": "no credentials serve model " + model})
		return
	}

	// Live snapshots fan out per account; the quota service bounds global
	// upstream concurrency. Each failure degrades to that account's passive
	// snapshot independently.
	ctx := c.Request.Context()
	accounts := make([]inferenceQuotaAccount, len(matched))
	var wg sync.WaitGroup
	for i, auth := range matched {
		wg.Add(1)
		go func() {
			defer wg.Done()
			var live *quota.Snapshot
			if s.quotaService != nil {
				live, _ = s.quotaService.Snapshot(ctx, auth, mode)
			}
			accounts[i] = buildInferenceQuotaAccount(auth, model, now, live)
		}()
	}
	wg.Wait()

	sort.Slice(accounts, func(i, j int) bool {
		if accounts[i].Provider != accounts[j].Provider {
			return accounts[i].Provider < accounts[j].Provider
		}
		return accounts[i].Name < accounts[j].Name
	})
	c.JSON(http.StatusOK, accounts)
}

func buildInferenceQuotaAccount(auth *coreauth.Auth, model string, now time.Time, live *quota.Snapshot) inferenceQuotaAccount {
	modelState := inferenceQuotaModelState(auth, model)
	inCooldown, _ := inferenceQuotaCooldown(auth, modelState, now)
	plan := inferenceQuotaPlan(auth)
	observedAt := inferenceQuotaObservedAt(auth.Quota, modelState)
	windows := inferenceQuotaAccountWindows(auth.Provider, auth.Quota, modelState)
	if live != nil {
		if len(live.Windows) > 0 {
			windows = mapQuotaWindows(live.Windows)
			if !live.ObservedAt.IsZero() {
				at := live.ObservedAt
				observedAt = &at
			}
		}
		if strings.TrimSpace(live.Plan) != "" {
			plan = strings.TrimSpace(live.Plan)
		}
	}
	return inferenceQuotaAccount{
		Provider:          strings.TrimSpace(auth.Provider),
		Name:              inferenceQuotaAccountName(auth),
		Type:              inferenceQuotaAccountType(auth),
		Plan:              plan,
		InCooldown:        inCooldown,
		WindowsObservedAt: observedAt,
		Windows:           windows,
	}
}

func mapQuotaWindows(windows []quota.Window) []inferenceQuotaWindow {
	out := make([]inferenceQuotaWindow, 0, len(windows))
	for _, window := range windows {
		out = append(out, inferenceQuotaWindow{
			Name:        window.Name,
			UsedPercent: window.UsedPercent,
			ResetAt:     window.ResetAt,
			Status:      window.Status,
		})
	}
	return out
}

// inferenceQuotaCooldown reports whether the account is effectively blocked
// for the requested model, following the scheduler's availability semantics:
// flags without a recovery time block indefinitely, while expired timers
// count as recovered. It returns the latest future recovery time, if any.
func inferenceQuotaCooldown(auth *coreauth.Auth, model *coreauth.ModelState, now time.Time) (bool, time.Time) {
	blocked, recoverAt := quotaFlagsBlocked(auth.Unavailable, auth.Quota.Exceeded, auth.NextRetryAfter, auth.Quota.NextRecoverAt, now)
	if model != nil {
		modelBlocked, modelRecover := quotaFlagsBlocked(
			model.Unavailable || model.Status == coreauth.StatusDisabled,
			model.Quota.Exceeded, model.NextRetryAfter, model.Quota.NextRecoverAt, now,
		)
		if modelBlocked {
			blocked = true
			if modelRecover.After(recoverAt) {
				recoverAt = modelRecover
			}
		}
	}
	return blocked, recoverAt
}

func quotaFlagsBlocked(unavailable, exceeded bool, retryAfter, recoverAt time.Time, now time.Time) (bool, time.Time) {
	if !unavailable && !exceeded {
		return false, time.Time{}
	}
	var latest time.Time
	if !retryAfter.IsZero() && retryAfter.After(now) {
		latest = retryAfter
	}
	if exceeded && !recoverAt.IsZero() && recoverAt.After(now) && recoverAt.After(latest) {
		latest = recoverAt
	}
	if !latest.IsZero() {
		return true, latest
	}
	if !retryAfter.IsZero() || (exceeded && !recoverAt.IsZero()) {
		return false, time.Time{}
	}
	return true, time.Time{}
}

// inferenceQuotaAccountName returns the masked display identity, CPAMC-style:
// the masked auth filename first (the key quota UIs show), then the masked
// label, then the masked account email or key suffix.
func inferenceQuotaAccountName(auth *coreauth.Auth) string {
	if auth == nil {
		return ""
	}
	if name := maskInferenceFileName(inferenceQuotaFileName(auth)); name != "" {
		return name
	}
	if label := maskInferenceEmailsInText(strings.TrimSpace(auth.Label)); label != "" {
		return label
	}
	_, account := inferenceQuotaMaskedAccount(auth)
	return account
}

func inferenceQuotaAccountType(auth *coreauth.Auth) string {
	if auth != nil && auth.AuthKind() == coreauth.AuthKindAPIKey {
		return "api"
	}
	return "oauth"
}

// inferenceQuotaFileName returns the auth file base name without any
// directory, for correlation with filename-keyed quota UIs.
func inferenceQuotaFileName(auth *coreauth.Auth) string {
	if auth == nil {
		return ""
	}
	if name := strings.TrimSpace(auth.FileName); name != "" {
		return filepath.Base(name)
	}
	if auth.Attributes != nil {
		for _, key := range []string{"path", "source"} {
			if value := strings.TrimSpace(auth.Attributes[key]); value != "" {
				return filepath.Base(value)
			}
		}
	}
	return ""
}

// maskInferenceFileName masks emails embedded in an auth filename while
// keeping the surrounding structure (provider prefix, plan/hash suffixes) so
// owners can correlate with filename-keyed quota UIs. All in-repo filename
// builders use a "<provider>-<identity>" shape, so an alphanumeric leading
// segment is preserved verbatim and only the identity part is masked.
func maskInferenceFileName(name string) string {
	name = strings.TrimSpace(name)
	if name == "" {
		return ""
	}
	prefix, rest := "", name
	if before, after, found := strings.Cut(name, "-"); found && isAlnum(before) {
		prefix, rest = before+"-", after
	}
	masked := maskInferenceEmailsInText(rest)
	if strings.EqualFold(strings.TrimSuffix(prefix, "-"), "meta") {
		masked = maskInferenceUnderscoreLocal(masked)
	}
	return prefix + masked
}

// maskInferenceUnderscoreLocal masks the sanitized local part of a meta
// filename identity ("<local>_<domain>-<hash>.json"), where "@" (and any other
// non-filename rune) was replaced by "_" at creation time. It is scoped to
// meta filenames because no other provider sanitizes "@" away.
func maskInferenceUnderscoreLocal(identity string) string {
	suffix := ""
	stem := identity
	if before, found := strings.CutSuffix(stem, ".json"); found {
		stem, suffix = before, ".json"
	}
	// The sanitizer maps every non-filename rune to "_", so split at the last
	// underscore: anything before it is the local part, anything after starts
	// the domain. Requiring a dot in the domain leaves "oauth" and "<hash>"
	// fallback identities untouched.
	separator := strings.LastIndex(stem, "_")
	if separator <= 0 {
		return identity
	}
	local, domain := stem[:separator], stem[separator+1:]
	if local == "" || !strings.Contains(domain, ".") {
		return identity
	}
	return maskInferenceLocal(local) + "_" + domain + suffix
}

func isAlnum(value string) bool {
	if value == "" {
		return false
	}
	for _, r := range value {
		if r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z' || r >= '0' && r <= '9' {
			continue
		}
		return false
	}
	return true
}

// inferenceQuotaAccountWindows merges credential-level and model-level
// normalized windows, preferring the model-specific observation on name
// collisions.
func inferenceQuotaAccountWindows(provider string, quota coreauth.QuotaState, model *coreauth.ModelState) []inferenceQuotaWindow {
	windows := inferenceQuotaWindows(provider, quota)
	if model == nil {
		return windows
	}
	return mergeQuotaWindows(provider, windows, inferenceQuotaWindows(provider, model.Quota))
}

func inferenceQuotaObservedAt(quota coreauth.QuotaState, model *coreauth.ModelState) *time.Time {
	observed := quota.ObservedAt
	if model != nil && model.Quota.ObservedAt.After(observed) {
		observed = model.Quota.ObservedAt
	}
	if observed.IsZero() {
		return nil
	}
	return &observed
}

// inferenceQuotaModelState resolves the per-model state for the requested
// model, tolerating credential prefixes and case differences.
func inferenceQuotaModelState(auth *coreauth.Auth, model string) *coreauth.ModelState {
	if auth == nil || len(auth.ModelStates) == 0 || strings.TrimSpace(model) == "" {
		return nil
	}
	candidates := []string{strings.TrimSpace(model)}
	if prefix := strings.TrimSpace(auth.Prefix); prefix != "" {
		if stripped, ok := strings.CutPrefix(candidates[0], prefix+"/"); ok && strings.TrimSpace(stripped) != "" {
			candidates = append(candidates, strings.TrimSpace(stripped))
		} else if stripped, ok := strings.CutPrefix(candidates[0], prefix); ok && strings.TrimSpace(stripped) != "" {
			candidates = append(candidates, strings.TrimSpace(stripped))
		}
	}
	for _, candidate := range candidates {
		for key, state := range auth.ModelStates {
			if state == nil {
				continue
			}
			if strings.EqualFold(strings.TrimSpace(key), candidate) {
				return state
			}
		}
	}
	return nil
}

// inferenceQuotaMaskedAccount returns the account kind and a masked account
// identifier. Metadata email is not guaranteed (hand-made files, key-only
// credentials), so the lookup falls back through attributes to an email-like
// label. OAuth emails keep local-part affixes plus the full domain; API keys
// keep only a short suffix.
func inferenceQuotaMaskedAccount(auth *coreauth.Auth) (string, string) {
	if auth == nil {
		return "", ""
	}
	kind, account := auth.AccountInfo()
	kind = strings.TrimSpace(kind)
	account = strings.TrimSpace(account)
	if account == "" {
		account = inferenceQuotaEmail(auth)
	}
	if account == "" {
		account = inferenceQuotaLabelEmail(auth)
	}
	if account == "" {
		return kind, ""
	}
	if strings.Contains(account, "@") {
		return kind, maskInferenceEmail(account)
	}
	return kind, maskInferenceSecret(account)
}

func inferenceQuotaEmail(auth *coreauth.Auth) string {
	if auth == nil {
		return ""
	}
	if auth.Metadata != nil {
		if email, ok := auth.Metadata["email"].(string); ok && strings.TrimSpace(email) != "" {
			return strings.TrimSpace(email)
		}
	}
	if auth.Attributes != nil {
		if email := strings.TrimSpace(auth.Attributes["email"]); email != "" {
			return email
		}
		if email := strings.TrimSpace(auth.Attributes["account_email"]); email != "" {
			return email
		}
	}
	return ""
}

// inferenceQuotaLabelEmail returns the credential label when it is itself an
// email address, covering files whose JSON carries no email field.
func inferenceQuotaLabelEmail(auth *coreauth.Auth) string {
	if auth == nil {
		return ""
	}
	label := strings.TrimSpace(auth.Label)
	local, domain, found := strings.Cut(label, "@")
	if !found || strings.TrimSpace(local) == "" || !strings.Contains(domain, ".") {
		return ""
	}
	return label
}

// maskInferenceEmail partially reveals an email so the owner can recognize the
// account without exposing it as a whole: "jane.doe@example.com" becomes
// "ja***oe@example.com". Short local parts that would be fully revealed by a
// suffix keep a prefix-only mask instead.
func maskInferenceEmail(email string) string {
	email = strings.TrimSpace(email)
	local, domain, found := strings.Cut(email, "@")
	domain = strings.TrimSpace(domain)
	local = strings.TrimSpace(local)
	if !found || local == "" || domain == "" {
		return maskInferenceSecret(email)
	}
	return maskInferenceLocal(local) + "@" + domain
}

// maskInferenceLocal masks an email local part, revealing at most two leading
// and two trailing runes.
func maskInferenceLocal(local string) string {
	runes := []rune(local)
	if len(runes) == 0 {
		return ""
	}
	if len(runes) <= 4 {
		return string(runes[:min(2, len(runes))]) + "***"
	}
	return string(runes[:2]) + "***" + string(runes[len(runes)-2:])
}

var inferenceEmailInTextPattern = regexp.MustCompile(`[\w.+-]+@[\w-]+(?:\.[\w-]+)+`)

// maskInferenceEmailsInText masks every email address embedded in free-form
// text such as credential labels, which may carry raw account emails.
func maskInferenceEmailsInText(text string) string {
	if text == "" || !strings.Contains(text, "@") {
		return text
	}
	return inferenceEmailInTextPattern.ReplaceAllStringFunc(text, maskInferenceEmail)
}

// maskInferenceSecret reveals only a short suffix of a secret identifier such
// as an API key.
func maskInferenceSecret(secret string) string {
	secret = strings.TrimSpace(secret)
	if secret == "" {
		return ""
	}
	runes := []rune(secret)
	if len(runes) <= 4 {
		return "***"
	}
	return "***" + string(runes[len(runes)-4:])
}

// inferenceQuotaPlan reports the subscription plan when it is already known
// locally (quota signals or Codex ID token claims). Codex values pass through
// the same display table as live snapshots so passive and live plans match.
// Account identifiers from those sources are never exposed.
func inferenceQuotaPlan(auth *coreauth.Auth) string {
	if auth == nil {
		return ""
	}
	isCodex := strings.EqualFold(strings.TrimSpace(auth.Provider), "codex")
	planByKey := make(map[string]string, len(auth.Quota.Signals))
	for key, value := range auth.Quota.Signals {
		if trimmed := strings.TrimSpace(value); trimmed != "" {
			planByKey[strings.ToLower(strings.TrimSpace(key))] = trimmed
		}
	}
	// Prefer the explicit Codex plan header over the generic key so concurrent
	// signals resolve the same way on every poll.
	if plan, ok := planByKey["x-codex-plan-type"]; ok {
		return quota.CodexPlanDisplay(plan)
	}
	if plan, ok := planByKey["plan"]; ok {
		return plan
	}
	if !isCodex || auth.Metadata == nil {
		return ""
	}
	raw, ok := auth.Metadata["id_token"].(string)
	if !ok || strings.TrimSpace(raw) == "" {
		return ""
	}
	claims, err := codexauth.ParseJWTToken(strings.TrimSpace(raw))
	if err != nil || claims == nil {
		return ""
	}
	return quota.CodexPlanDisplay(claims.CodexAuthInfo.ChatgptPlanType)
}
