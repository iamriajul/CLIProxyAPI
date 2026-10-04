package executor

import (
	"bufio"
	"bytes"
	"context"
	"fmt"
	"io"
	"net/http"
	"strings"

	opencodeauth "github.com/router-for-me/CLIProxyAPI/v8/internal/auth/opencode"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/buildinfo"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/config"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/registry"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/runtime/executor/helps"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/thinking"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/util"
	cliproxyauth "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/auth"
	cliproxyexecutor "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/executor"
	cliproxysession "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/session"
	sdktranslator "github.com/router-for-me/CLIProxyAPI/v8/sdk/translator"
	log "github.com/sirupsen/logrus"
	"github.com/tidwall/gjson"
	"github.com/tidwall/sjson"
)

// OpenCodeExecutor is a stateless executor for the OpenCode Zen Go gateway.
// Model routing follows the reference catalog per-model wire contract:
// anthropic-routed lanes go through the Claude protocol, Responses-native
// lanes accept Responses input natively, and chat lanes translate every
// source format to OpenAI chat completions.
type OpenCodeExecutor struct {
	ClaudeExecutor
	cfg *config.Config
}

// NewOpenCodeExecutor creates a new OpenCode executor.
func NewOpenCodeExecutor(cfg *config.Config) *OpenCodeExecutor {
	return &OpenCodeExecutor{
		ClaudeExecutor: ClaudeExecutor{
			cfg:                     cfg,
			requestLogProvider:      "opencode",
			upstreamModelNormalizer: normalizeOpencodeUpstreamModel,
		},
		cfg: cfg,
	}
}

// Identifier returns the executor identifier.
func (e *OpenCodeExecutor) Identifier() string { return "opencode" }

// opencodeUpstreamRoute reports the gateway wire protocol for a model.
func opencodeUpstreamRoute(model string) string {
	return registry.OpencodeUpstreamRoute(model)
}

// RequestToFormat reports the upstream request format used after auth selection.
//
// The lane's wire protocol is authoritative, not the caller's: a Responses-native
// model is served at /responses whatever the source format was, and the request is
// translated up to it. Gating on the source format instead (the old behavior) left a
// chat-completions caller on /chat/completions for a Responses-only lane, which the
// gateway rejects with ModelProtocolUnsupported. MetaExecutor has always worked this
// way: it pins the wire and translates the caller.
func (e *OpenCodeExecutor) RequestToFormat(req cliproxyexecutor.Request, opts cliproxyexecutor.Options) sdktranslator.Format {
	switch opencodeUpstreamRoute(req.Model) {
	case "responses":
		// Codex format, matching executeResponses: it is the Responses wire and
		// the only target every source format has a registered transform for.
		return sdktranslator.FormatCodex
	case "anthropic":
		return sdktranslator.FormatClaude
	default:
		return sdktranslator.FormatOpenAI
	}
}

// PrepareRequest injects OpenCode credentials into the outgoing HTTP request.
func (e *OpenCodeExecutor) PrepareRequest(req *http.Request, auth *cliproxyauth.Auth) error {
	if req == nil {
		return nil
	}
	if token := opencodeCreds(auth); strings.TrimSpace(token) != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	req.Header.Set("User-Agent", "CLIProxyAPI/"+buildinfo.Version)
	var attrs map[string]string
	if auth != nil {
		attrs = auth.Attributes
	}
	util.ApplyCustomHeadersFromAttrs(req, attrs)
	return nil
}

// HttpRequest injects OpenCode credentials into the request and executes it.
func (e *OpenCodeExecutor) HttpRequest(ctx context.Context, auth *cliproxyauth.Auth, req *http.Request) (*http.Response, error) {
	if req == nil {
		return nil, fmt.Errorf("opencode executor: request is nil")
	}
	if ctx == nil {
		ctx = req.Context()
	}
	httpReq := req.WithContext(ctx)
	if err := e.PrepareRequest(httpReq, auth); err != nil {
		return nil, err
	}
	httpClient := helps.NewProxyAwareHTTPClient(ctx, e.cfg, auth, 0)
	return httpClient.Do(httpReq)
}

// Execute performs a non-streaming request to the Zen Go gateway.
func (e *OpenCodeExecutor) Execute(ctx context.Context, auth *cliproxyauth.Auth, req cliproxyexecutor.Request, opts cliproxyexecutor.Options) (resp cliproxyexecutor.Response, err error) {
	from := opts.SourceFormat
	if from.String() == "claude" && opencodeUpstreamRoute(req.Model) == "anthropic" {
		e.ensureAttributes(auth)
		auth.Attributes["base_url"] = opencodeAnthropicBaseURL(auth)
		ensureOpencodeSessionHeader(&opts, sessionPayloadForOptions(req, opts))
		return e.ClaudeExecutor.Execute(ctx, auth, req, opts)
	}
	// The lane decides the wire, not the caller: a Responses-native model always
	// rides /responses, and executeResponses translates the request up from
	// whatever source format arrived. Chat-route models keep the chat lane,
	// which the gateway serves most broadly.
	if opencodeUpstreamRoute(req.Model) == "responses" {
		return e.executeResponses(ctx, auth, req, opts)
	}
	responseFormat := cliproxyexecutor.ResponseFormatOrSource(opts)

	baseModel := thinking.ParseSuffix(req.Model).ModelName
	token := opencodeCreds(auth)
	if strings.TrimSpace(token) == "" {
		return resp, statusErr{code: http.StatusUnauthorized, msg: "opencode executor: missing api key"}
	}

	reporter := helps.NewExecutorUsageReporter(ctx, e, baseModel, auth)
	defer reporter.TrackFailure(ctx, &err)

	to := sdktranslator.FromString("openai")
	originalPayloadSource := req.Payload
	if len(opts.OriginalRequest) > 0 {
		originalPayloadSource = opts.OriginalRequest
	}
	originalPayload := bytes.Clone(originalPayloadSource)
	originalTranslated := helps.TranslateRequestWithCodexMultiAgentV2(ctx, opts.Headers, e.cfg, from, to, baseModel, originalPayload, false)
	body := helps.TranslateRequestWithCodexMultiAgentV2(ctx, opts.Headers, e.cfg, from, to, baseModel, bytes.Clone(req.Payload), false)

	body, err = sjson.SetBytes(body, "model", baseModel)
	if err != nil {
		return resp, fmt.Errorf("opencode executor: failed to set model in payload: %w", err)
	}

	body, err = helps.ApplyRequestThinking(body, req, opts, from.String(), "opencode", e.Identifier())
	if err != nil {
		return resp, err
	}

	requestedModel := helps.PayloadRequestedModel(opts, req.Model)
	requestPath := helps.PayloadRequestPath(opts)
	body = helps.ApplyPayloadConfigWithRequest(e.cfg, baseModel, to.String(), from.String(), "", body, originalTranslated, requestedModel, requestPath, opts.Headers)
	body = normalizeOpencodeTools(body, baseModel)
	reporter.SetTranslatedReasoningEffort(body, e.Identifier())

	url := opencodeBaseURL(auth) + "/chat/completions"
	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(body))
	if err != nil {
		return resp, err
	}
	applyOpencodeHeaders(httpReq, token, false)
	applyOpencodeSessionHeader(httpReq, opencodeUpstreamSessionID(opts.Headers, originalPayloadSource, opts.Metadata))
	var attrs map[string]string
	if auth != nil {
		attrs = auth.Attributes
	}
	util.ApplyCustomHeadersFromAttrs(httpReq, attrs, opts.Headers)
	var authID, authLabel, authType, authValue string
	if auth != nil {
		authID = auth.ID
		authLabel = auth.Label
		authType, authValue = auth.AccountInfo()
	}
	helps.RecordAPIRequest(ctx, e.cfg, helps.UpstreamRequestLog{
		URL:       url,
		Method:    http.MethodPost,
		Headers:   httpReq.Header.Clone(),
		Body:      body,
		Provider:  e.Identifier(),
		AuthID:    authID,
		AuthLabel: authLabel,
		AuthType:  authType,
		AuthValue: authValue,
	})

	httpClient := helps.NewProxyAwareHTTPClient(ctx, e.cfg, auth, 0)
	httpClient = reporter.TrackHTTPClient(httpClient)
	httpResp, err := httpClient.Do(httpReq)
	if err != nil {
		helps.RecordAPIResponseError(ctx, e.cfg, err)
		return resp, err
	}
	defer func() {
		if errClose := httpResp.Body.Close(); errClose != nil {
			log.Errorf("opencode executor: close response body error: %v", errClose)
		}
	}()
	helps.RecordAPIResponseMetadata(ctx, e.cfg, httpResp.StatusCode, httpResp.Header.Clone())
	if httpResp.StatusCode < 200 || httpResp.StatusCode >= 300 {
		b, _ := io.ReadAll(httpResp.Body)
		helps.AppendAPIResponseChunk(ctx, e.cfg, b)
		helps.LogWithRequestID(ctx).Debugf("request error, error status: %d, error message: %s", httpResp.StatusCode, helps.SummarizeErrorBody(httpResp.Header.Get("Content-Type"), b))
		err = statusErr{code: httpResp.StatusCode, msg: string(b)}
		return resp, err
	}
	data, err := io.ReadAll(httpResp.Body)
	if err != nil {
		helps.RecordAPIResponseError(ctx, e.cfg, err)
		return resp, err
	}
	helps.AppendAPIResponseChunk(ctx, e.cfg, data)
	reporter.Publish(ctx, helps.ParseOpenAIUsage(data))
	var param any
	out := sdktranslator.TranslateNonStream(ctx, to, responseFormat, req.Model, opts.OriginalRequest, body, data, &param)
	if responseFormat == sdktranslator.FormatOpenAIResponse {
		out = helps.EnsureResponsesUsageDetails(out)
	}
	resp = cliproxyexecutor.Response{Payload: out, Headers: httpResp.Header.Clone()}
	return resp, nil
}

// ExecuteStream performs a streaming request to the Zen Go gateway.
func (e *OpenCodeExecutor) ExecuteStream(ctx context.Context, auth *cliproxyauth.Auth, req cliproxyexecutor.Request, opts cliproxyexecutor.Options) (_ *cliproxyexecutor.StreamResult, err error) {
	from := opts.SourceFormat
	// Lane-authoritative, matching Execute: a Responses-native model always
	// streams from /responses, whatever the caller's source format was.
	if opencodeUpstreamRoute(req.Model) == "responses" {
		return e.executeResponsesStream(ctx, auth, req, opts)
	}
	responseFormat := cliproxyexecutor.ResponseFormatOrSource(opts)

	baseModel := thinking.ParseSuffix(req.Model).ModelName
	token := opencodeCreds(auth)
	if strings.TrimSpace(token) == "" {
		return nil, statusErr{code: http.StatusUnauthorized, msg: "opencode executor: missing api key"}
	}

	reporter := helps.NewExecutorUsageReporter(ctx, e, baseModel, auth)
	defer reporter.TrackFailure(ctx, &err)

	to := sdktranslator.FromString("openai")
	originalPayloadSource := req.Payload
	if len(opts.OriginalRequest) > 0 {
		originalPayloadSource = opts.OriginalRequest
	}
	originalPayload := bytes.Clone(originalPayloadSource)
	originalTranslated := helps.TranslateRequestWithCodexMultiAgentV2(ctx, opts.Headers, e.cfg, from, to, baseModel, originalPayload, true)
	body := helps.TranslateRequestWithCodexMultiAgentV2(ctx, opts.Headers, e.cfg, from, to, baseModel, bytes.Clone(req.Payload), true)

	body, err = sjson.SetBytes(body, "model", baseModel)
	if err != nil {
		return nil, fmt.Errorf("opencode executor: failed to set model in payload: %w", err)
	}

	body, err = helps.ApplyRequestThinking(body, req, opts, from.String(), "opencode", e.Identifier())
	if err != nil {
		return nil, err
	}

	body, err = sjson.SetBytes(body, "stream_options.include_usage", true)
	if err != nil {
		return nil, fmt.Errorf("opencode executor: failed to set stream_options in payload: %w", err)
	}
	requestedModel := helps.PayloadRequestedModel(opts, req.Model)
	requestPath := helps.PayloadRequestPath(opts)
	body = helps.ApplyPayloadConfigWithRequest(e.cfg, baseModel, to.String(), from.String(), "", body, originalTranslated, requestedModel, requestPath, opts.Headers)
	body = normalizeOpencodeTools(body, baseModel)
	reporter.SetTranslatedReasoningEffort(body, e.Identifier())

	url := opencodeBaseURL(auth) + "/chat/completions"
	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(body))
	if err != nil {
		return nil, err
	}
	applyOpencodeHeaders(httpReq, token, true)
	applyOpencodeSessionHeader(httpReq, opencodeUpstreamSessionID(opts.Headers, originalPayloadSource, opts.Metadata))
	var attrs map[string]string
	if auth != nil {
		attrs = auth.Attributes
	}
	util.ApplyCustomHeadersFromAttrs(httpReq, attrs, opts.Headers)
	var authID, authLabel, authType, authValue string
	if auth != nil {
		authID = auth.ID
		authLabel = auth.Label
		authType, authValue = auth.AccountInfo()
	}
	helps.RecordAPIRequest(ctx, e.cfg, helps.UpstreamRequestLog{
		URL:       url,
		Method:    http.MethodPost,
		Headers:   httpReq.Header.Clone(),
		Body:      body,
		Provider:  e.Identifier(),
		AuthID:    authID,
		AuthLabel: authLabel,
		AuthType:  authType,
		AuthValue: authValue,
	})

	httpClient := helps.NewProxyAwareHTTPClient(ctx, e.cfg, auth, 0)
	httpClient = reporter.TrackHTTPClient(httpClient)
	httpResp, err := httpClient.Do(httpReq)
	if err != nil {
		helps.RecordAPIResponseError(ctx, e.cfg, err)
		return nil, err
	}
	helps.RecordAPIResponseMetadata(ctx, e.cfg, httpResp.StatusCode, httpResp.Header.Clone())
	if httpResp.StatusCode < 200 || httpResp.StatusCode >= 300 {
		b, _ := io.ReadAll(httpResp.Body)
		helps.AppendAPIResponseChunk(ctx, e.cfg, b)
		helps.LogWithRequestID(ctx).Debugf("request error, error status: %d, error message: %s", httpResp.StatusCode, helps.SummarizeErrorBody(httpResp.Header.Get("Content-Type"), b))
		if errClose := httpResp.Body.Close(); errClose != nil {
			log.Errorf("opencode executor: close response body error: %v", errClose)
		}
		err = statusErr{code: httpResp.StatusCode, msg: string(b)}
		return nil, err
	}
	out := make(chan cliproxyexecutor.StreamChunk)
	go func() {
		defer close(out)
		defer func() {
			if errClose := httpResp.Body.Close(); errClose != nil {
				log.Errorf("opencode executor: close response body error: %v", errClose)
			}
		}()
		scanner := bufio.NewScanner(httpResp.Body)
		scanner.Buffer(nil, 1_048_576)
		claudeInputTokens := helps.NewClaudeInputTokenState(from, to, responseFormat, originalPayload)
		var param any
		var streamUsage helps.StreamUsageBuffer
		defer streamUsage.Publish(ctx, reporter)
		for scanner.Scan() {
			line := scanner.Bytes()
			helps.AppendAPIResponseChunk(ctx, e.cfg, line)
			streamUsage.ObserveOpenAIStream(line)
			chunks := helps.TranslateStreamWithClaudeInputTokens(ctx, to, responseFormat, req.Model, opts.OriginalRequest, body, bytes.Clone(line), &param, claudeInputTokens)
			for i := range chunks {
				select {
				case out <- cliproxyexecutor.StreamChunk{Payload: chunks[i]}:
				case <-ctx.Done():
					return
				}
			}
		}
		doneChunks := helps.TranslateStreamWithClaudeInputTokens(ctx, to, responseFormat, req.Model, opts.OriginalRequest, body, []byte("[DONE]"), &param, claudeInputTokens)
		for i := range doneChunks {
			select {
			case out <- cliproxyexecutor.StreamChunk{Payload: doneChunks[i]}:
			case <-ctx.Done():
				return
			}
		}
		if errScan := scanner.Err(); errScan != nil {
			helps.RecordAPIResponseError(ctx, e.cfg, errScan)
			reporter.PublishFailure(ctx, errScan)
			select {
			case out <- cliproxyexecutor.StreamChunk{Err: errScan}:
			case <-ctx.Done():
			}
		}
	}()
	return &cliproxyexecutor.StreamResult{Headers: httpResp.Header.Clone(), Chunks: out}, nil
}

// CountTokens estimates token count for OpenCode requests.
func (e *OpenCodeExecutor) CountTokens(ctx context.Context, auth *cliproxyauth.Auth, req cliproxyexecutor.Request, opts cliproxyexecutor.Options) (cliproxyexecutor.Response, error) {
	if opts.SourceFormat.String() == "claude" && opencodeUpstreamRoute(req.Model) == "anthropic" {
		e.ensureAttributes(auth)
		auth.Attributes["base_url"] = opencodeAnthropicBaseURL(auth)
		return e.ClaudeExecutor.CountTokens(ctx, auth, req, opts)
	}
	baseModel := thinking.ParseSuffix(req.Model).ModelName

	from := opts.SourceFormat
	responseFormat := cliproxyexecutor.ResponseFormatOrSource(opts)
	to := sdktranslator.FromString("openai")
	// Mirror the Execute translation exactly (same helper, same thinking
	// provider) so counts cannot diverge from the payload actually sent.
	translated := helps.TranslateRequestWithCodexMultiAgentV2(ctx, opts.Headers, e.cfg, from, to, baseModel, bytes.Clone(req.Payload), false)

	translated, err := helps.ApplyRequestThinking(translated, req, opts, from.String(), "opencode", e.Identifier())
	if err != nil {
		return cliproxyexecutor.Response{}, err
	}

	enc, err := helps.TokenizerForModel(baseModel)
	if err != nil {
		return cliproxyexecutor.Response{}, fmt.Errorf("opencode executor: tokenizer init failed: %w", err)
	}

	count, err := helps.CountOpenAIChatTokens(enc, translated)
	if err != nil {
		return cliproxyexecutor.Response{}, fmt.Errorf("opencode executor: token counting failed: %w", err)
	}

	usageJSON := helps.BuildOpenAIUsageJSON(count)
	translatedUsage := sdktranslator.TranslateTokenCount(ctx, to, responseFormat, count, usageJSON)
	return cliproxyexecutor.Response{Payload: translatedUsage}, nil
}

// Refresh is a no-op for API-key credentials (with Home delegation support).
func (e *OpenCodeExecutor) Refresh(ctx context.Context, auth *cliproxyauth.Auth) (*cliproxyauth.Auth, error) {
	log.Debugf("opencode executor: refresh called")
	if refreshed, handled, err := helps.RefreshAuthViaHome(ctx, e.cfg, auth); handled {
		return refreshed, err
	}
	return auth, nil
}

func (e *OpenCodeExecutor) ensureAttributes(auth *cliproxyauth.Auth) {
	if auth != nil && auth.Attributes == nil {
		auth.Attributes = make(map[string]string)
	}
}

func applyOpencodeHeaders(r *http.Request, token string, stream bool) {
	r.Header.Set("Content-Type", "application/json")
	if strings.TrimSpace(token) != "" {
		r.Header.Set("Authorization", "Bearer "+token)
	}
	r.Header.Set("User-Agent", "CLIProxyAPI/"+buildinfo.Version)
	if stream {
		r.Header.Set("Accept", "text/event-stream")
		return
	}
	r.Header.Set("Accept", "application/json")
}

// sessionPayloadForOptions returns the downstream payload used for session
// resolution: the original request when preserved, else the current payload.
func sessionPayloadForOptions(req cliproxyexecutor.Request, opts cliproxyexecutor.Options) []byte {
	if len(opts.OriginalRequest) > 0 {
		return opts.OriginalRequest
	}
	return req.Payload
}

// opencodeIncomingSessionID returns the downstream x-opencode-session value, if present.
func opencodeIncomingSessionID(headers http.Header) string {
	if headers == nil {
		return ""
	}
	return cliproxysession.NormalizeExplicitID(helps.HeaderValueCaseInsensitive(headers, "x-opencode-session"))
}

// opencodeUpstreamSessionID resolves the session ID sent upstream to the Zen Go
// gateway. It prefers the downstream x-opencode-session verbatim so prompt
// caching stays aligned, and otherwise falls back to the canonical affinity
// identity (explicit Claude/Codex/session signals, then derived/message-hash
// identities) so requests without any session signal still route efficiently
// instead of failing with upstream MissingSessionID.
func opencodeUpstreamSessionID(headers http.Header, payload []byte, metadata map[string]any) string {
	if v := opencodeIncomingSessionID(headers); v != "" {
		return v
	}
	if id := strings.TrimSpace(cliproxyauth.CanonicalSessionID(headers, payload, metadata)); id != "" {
		return id
	}
	return ""
}

// applyOpencodeSessionHeader sets x-opencode-session on the upstream request.
func applyOpencodeSessionHeader(r *http.Request, sessionID string) {
	if r == nil {
		return
	}
	if strings.TrimSpace(sessionID) == "" {
		return
	}
	r.Header.Set("x-opencode-session", strings.TrimSpace(sessionID))
}

// ensureOpencodeSessionHeader makes sure opts.Headers carries an x-opencode-session
// value before delegating to the Claude executor on anthropic-routed lanes.
func ensureOpencodeSessionHeader(opts *cliproxyexecutor.Options, payload []byte) {
	if opts == nil {
		return
	}
	if opencodeIncomingSessionID(opts.Headers) != "" {
		return
	}
	sessionID := opencodeUpstreamSessionID(opts.Headers, payload, opts.Metadata)
	if sessionID == "" {
		return
	}
	if opts.Headers == nil {
		opts.Headers = make(http.Header)
	}
	opts.Headers.Set("x-opencode-session", sessionID)
}

// opencodeBaseURL resolves the chat/responses gateway base (always /v1-suffixed).
func opencodeBaseURL(auth *cliproxyauth.Auth) string {
	if raw := strings.TrimSpace(opencodeauth.ResolveBaseURL(authMetadata(auth), authAttributes(auth))); raw != "" {
		return strings.TrimRight(opencodeauth.NormalizeBaseURL(raw), "/")
	}
	return opencodeauth.OpenCodeGoAPIBaseURL
}

// opencodeAnthropicBaseURL resolves the Claude-protocol gateway base (no /v1
// suffix), honoring a per-credential base_url override like the chat base
// does instead of pinning the default Zen gateway.
func opencodeAnthropicBaseURL(auth *cliproxyauth.Auth) string {
	if raw := strings.TrimSpace(opencodeauth.ResolveBaseURL(authMetadata(auth), authAttributes(auth))); raw != "" {
		return strings.TrimSuffix(strings.TrimRight(raw, "/"), "/v1")
	}
	return strings.TrimSuffix(opencodeauth.OpenCodeGoAPIBaseURL, "/v1")
}

// opencodeCreds extracts the Zen Go API key from auth.
func opencodeCreds(a *cliproxyauth.Auth) string {
	if a == nil {
		return ""
	}
	return opencodeauth.OpenCodeCreds(a.Metadata, a.Attributes)
}

func authMetadata(auth *cliproxyauth.Auth) map[string]any {
	if auth == nil {
		return nil
	}
	return auth.Metadata
}

func authAttributes(auth *cliproxyauth.Auth) map[string]string {
	if auth == nil {
		return nil
	}
	return auth.Attributes
}

// normalizeOpencodeUpstreamModel returns the canonical upstream model ID.
func normalizeOpencodeUpstreamModel(model string) string {
	return strings.TrimSpace(thinking.ParseSuffix(model).ModelName)
}

// opencodeNoToolChoiceModels lists the exact Zen lanes whose gateway rejects
// tool_choice (reference contract notes). The vision-exp lane explicitly keeps
// it, so substring matching would over-strip — hence the explicit set.
var opencodeNoToolChoiceModels = map[string]bool{
	"deepseek-v4-flash": true,
	"deepseek-v4-pro":   true,
	"mimo-v2-omni":      true,
	"mimo-v2-pro":       true,
	"mimo-v2.5":         true,
	"mimo-v2.5-pro":     true,
}

// opencodeRejectsToolChoice reports whether the lane rejects tool_choice.
func opencodeRejectsToolChoice(model string) bool {
	key := strings.ToLower(strings.TrimSpace(thinking.ParseSuffix(model).ModelName))
	return opencodeNoToolChoiceModels[key]
}

// normalizeOpencodeTools converts `custom` tools to `function` tools, inlines
// local $refs, and drops tool_choice selectors on lanes that reject them.
func normalizeOpencodeTools(body []byte, model string) []byte {
	if len(body) == 0 || !gjson.ValidBytes(body) {
		return body
	}
	items := gjson.GetBytes(body, "tools")
	if items.Exists() && items.IsArray() && len(items.Array()) > 0 {
		updatedItems := make([]string, 0, len(items.Array()))
		changed := false
		for _, item := range items.Array() {
			raw := item.Raw
			if strings.EqualFold(strings.TrimSpace(item.Get("type").String()), "custom") {
				if updated, err := sjson.SetBytes([]byte(raw), "type", "function"); err == nil {
					raw = string(updated)
					changed = true
				}
			}
			if params := gjson.GetBytes([]byte(raw), "function.parameters"); params.Exists() && params.IsObject() {
				inlined := util.InlineLocalRefs(params.Raw)
				if inlined != params.Raw {
					if updated, err := sjson.SetRawBytes([]byte(raw), "function.parameters", []byte(inlined)); err == nil {
						raw = string(updated)
						changed = true
					}
				}
			}
			updatedItems = append(updatedItems, raw)
		}
		if changed {
			if updated, err := sjson.SetRawBytes(body, "tools", helps.JoinRawJSONStrings(updatedItems)); err == nil {
				body = updated
			}
		}
	}
	if opencodeRejectsToolChoice(model) {
		if updated, err := sjson.DeleteBytes(body, "tool_choice"); err == nil {
			body = updated
		}
	}
	return body
}

func (e *OpenCodeExecutor) executeResponses(ctx context.Context, auth *cliproxyauth.Auth, req cliproxyexecutor.Request, opts cliproxyexecutor.Options) (resp cliproxyexecutor.Response, err error) {
	responseFormat := cliproxyexecutor.ResponseFormatOrSource(opts)
	baseModel := thinking.ParseSuffix(req.Model).ModelName
	token := opencodeCreds(auth)
	if strings.TrimSpace(token) == "" {
		return resp, statusErr{code: http.StatusUnauthorized, msg: "opencode executor: missing api key"}
	}

	reporter := helps.NewExecutorUsageReporter(ctx, e, baseModel, auth)
	defer reporter.TrackFailure(ctx, &err)

	// Translate up when the caller did not already speak Responses. The lane's
	// protocol is authoritative, so a chat-completions or Claude payload is
	// converted here rather than being sent down a wire the gateway refuses.
	// This mirrors MetaExecutor, which always pins the Codex/Responses wire and
	// lets the translator handle every source format.
	//
	// The target is the Codex format, not "openai-response": only openai -> codex
	// has a registered transform, so a chat or Claude caller would otherwise be
	// forwarded untranslated. Codex output is the Responses shape this endpoint
	// expects, and codex -> openai-response is registered for the way back.
	sourcePayload := bytes.Clone(req.Payload)
	body := sourcePayload
	if opts.SourceFormat != sdktranslator.FormatOpenAIResponse {
		body = helps.TranslateRequestWithCodexMultiAgentV2(ctx, opts.Headers, e.cfg, opts.SourceFormat,
			sdktranslator.FormatCodex, baseModel, bytes.Clone(req.Payload), false)
	}
	var errSet error
	body, errSet = sjson.SetBytes(body, "model", baseModel)
	if errSet != nil {
		return resp, fmt.Errorf("opencode executor: failed to set model in payload: %w", errSet)
	}

	body = helps.SetBoolIfDifferent(body, "stream", false)

	var errThinking error
	body, errThinking = helps.ApplyRequestThinking(body, req, opts, opts.SourceFormat.String(), sdktranslator.FormatCodex.String(), e.Identifier())
	if errThinking != nil {
		return resp, errThinking
	}

	requestedModel := helps.PayloadRequestedModel(opts, req.Model)
	requestPath := helps.PayloadRequestPath(opts)
	body = helps.ApplyPayloadConfigWithRequest(e.cfg, baseModel, "openai-response", opts.SourceFormat.String(), "", body, sourcePayload, requestedModel, requestPath, opts.Headers)
	body = normalizeOpencodeTools(body, baseModel)
	// The gateway issues encrypted_content bound to its own caller, so replaying
	// it on a later turn is rejected upstream (omp #11928). Strip it the same way
	// the Meta lane does, or multi-step tool-call turns start failing after this
	// change moves chat callers onto /responses.
	body = sanitizeOpenAIResponsesReasoningEncryptedContent(ctx, "opencode executor", body)
	reporter.SetTranslatedReasoningEffort(body, e.Identifier())

	url := opencodeBaseURL(auth) + "/responses"
	httpReq, errNewRequest := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(body))
	if errNewRequest != nil {
		return resp, errNewRequest
	}
	applyOpencodeHeaders(httpReq, token, false)
	applyOpencodeSessionHeader(httpReq, opencodeUpstreamSessionID(opts.Headers, sessionPayloadForOptions(req, opts), opts.Metadata))
	var attrs map[string]string
	if auth != nil {
		attrs = auth.Attributes
	}
	util.ApplyCustomHeadersFromAttrs(httpReq, attrs, opts.Headers)

	var authID, authLabel, authType, authValue string
	if auth != nil {
		authID = auth.ID
		authLabel = auth.Label
		authType, authValue = auth.AccountInfo()
	}
	helps.RecordAPIRequest(ctx, e.cfg, helps.UpstreamRequestLog{
		URL:       url,
		Method:    http.MethodPost,
		Headers:   httpReq.Header.Clone(),
		Body:      body,
		Provider:  e.Identifier(),
		AuthID:    authID,
		AuthLabel: authLabel,
		AuthType:  authType,
		AuthValue: authValue,
	})

	httpClient := helps.NewProxyAwareHTTPClient(ctx, e.cfg, auth, 0)
	httpClient = reporter.TrackHTTPClient(httpClient)
	httpResp, errDo := httpClient.Do(httpReq)
	if errDo != nil {
		helps.RecordAPIResponseError(ctx, e.cfg, errDo)
		return resp, errDo
	}
	defer func() {
		if errClose := httpResp.Body.Close(); errClose != nil {
			log.Errorf("opencode executor: close response body error: %v", errClose)
		}
	}()

	helps.RecordAPIResponseMetadata(ctx, e.cfg, httpResp.StatusCode, httpResp.Header.Clone())
	if httpResp.StatusCode < 200 || httpResp.StatusCode >= 300 {
		b, _ := io.ReadAll(httpResp.Body)
		helps.AppendAPIResponseChunk(ctx, e.cfg, b)
		helps.LogWithRequestID(ctx).Debugf("request error, error status: %d, error message: %s", httpResp.StatusCode, helps.SummarizeErrorBody(httpResp.Header.Get("Content-Type"), b))
		err = statusErr{code: httpResp.StatusCode, msg: string(b)}
		return resp, err
	}

	data, errRead := io.ReadAll(httpResp.Body)
	if errRead != nil {
		helps.RecordAPIResponseError(ctx, e.cfg, errRead)
		return resp, errRead
	}
	helps.AppendAPIResponseChunk(ctx, e.cfg, data)

	if usage, ok := helps.ParseCodexUsage(data); ok && (usage.TotalTokens > 0 || usage.InputTokens > 0) {
		reporter.Publish(ctx, usage)
	} else if usage := helps.ParseOpenAIUsage(data); usage.TotalTokens > 0 || usage.InputTokens > 0 {
		reporter.Publish(ctx, usage)
	}

	out := data
	if responseFormat != sdktranslator.FormatOpenAIResponse {
		var param any
		out = sdktranslator.TranslateNonStream(ctx, sdktranslator.FormatOpenAIResponse, responseFormat, req.Model, opts.OriginalRequest, body, data, &param)
	}
	resp = cliproxyexecutor.Response{Payload: out, Headers: httpResp.Header.Clone()}
	return resp, nil
}

func (e *OpenCodeExecutor) executeResponsesStream(ctx context.Context, auth *cliproxyauth.Auth, req cliproxyexecutor.Request, opts cliproxyexecutor.Options) (_ *cliproxyexecutor.StreamResult, err error) {
	responseFormat := cliproxyexecutor.ResponseFormatOrSource(opts)
	baseModel := thinking.ParseSuffix(req.Model).ModelName
	token := opencodeCreds(auth)
	if strings.TrimSpace(token) == "" {
		return nil, statusErr{code: http.StatusUnauthorized, msg: "opencode executor: missing api key"}
	}

	reporter := helps.NewExecutorUsageReporter(ctx, e, baseModel, auth)
	defer reporter.TrackFailure(ctx, &err)

	// Same lane-authoritative translation as executeResponses: a non-Responses
	// caller is converted up to the /responses wire instead of being sent to a
	// chat endpoint the gateway refuses for this model.
	sourcePayload := bytes.Clone(req.Payload)
	body := sourcePayload
	if opts.SourceFormat != sdktranslator.FormatOpenAIResponse {
		body = helps.TranslateRequestWithCodexMultiAgentV2(ctx, opts.Headers, e.cfg, opts.SourceFormat,
			sdktranslator.FormatCodex, baseModel, bytes.Clone(req.Payload), true)
	}
	var errSet error
	body, errSet = sjson.SetBytes(body, "model", baseModel)
	if errSet != nil {
		return nil, fmt.Errorf("opencode executor: failed to set model in payload: %w", errSet)
	}

	body = helps.SetBoolIfDifferent(body, "stream", true)
	var errThinking error
	body, errThinking = helps.ApplyRequestThinking(body, req, opts, opts.SourceFormat.String(), sdktranslator.FormatCodex.String(), e.Identifier())
	if errThinking != nil {
		return nil, errThinking
	}

	requestedModel := helps.PayloadRequestedModel(opts, req.Model)
	requestPath := helps.PayloadRequestPath(opts)
	body = helps.ApplyPayloadConfigWithRequest(e.cfg, baseModel, "openai-response", opts.SourceFormat.String(), "", body, sourcePayload, requestedModel, requestPath, opts.Headers)
	body = normalizeOpencodeTools(body, baseModel)
	// See executeResponses: the gateway binds encrypted_content to its own
	// caller, so replayed reasoning items must be stripped (omp #11928).
	body = sanitizeOpenAIResponsesReasoningEncryptedContent(ctx, "opencode executor", body)
	reporter.SetTranslatedReasoningEffort(body, e.Identifier())

	url := opencodeBaseURL(auth) + "/responses"
	httpReq, errNewRequest := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(body))
	if errNewRequest != nil {
		return nil, errNewRequest
	}
	applyOpencodeHeaders(httpReq, token, true)
	applyOpencodeSessionHeader(httpReq, opencodeUpstreamSessionID(opts.Headers, sessionPayloadForOptions(req, opts), opts.Metadata))
	var attrs map[string]string
	if auth != nil {
		attrs = auth.Attributes
	}
	util.ApplyCustomHeadersFromAttrs(httpReq, attrs, opts.Headers)

	var authID, authLabel, authType, authValue string
	if auth != nil {
		authID = auth.ID
		authLabel = auth.Label
		authType, authValue = auth.AccountInfo()
	}
	helps.RecordAPIRequest(ctx, e.cfg, helps.UpstreamRequestLog{
		URL:       url,
		Method:    http.MethodPost,
		Headers:   httpReq.Header.Clone(),
		Body:      body,
		Provider:  e.Identifier(),
		AuthID:    authID,
		AuthLabel: authLabel,
		AuthType:  authType,
		AuthValue: authValue,
	})

	httpClient := helps.NewProxyAwareHTTPClient(ctx, e.cfg, auth, 0)
	httpClient = reporter.TrackHTTPClient(httpClient)
	httpResp, errDo := httpClient.Do(httpReq)
	if errDo != nil {
		helps.RecordAPIResponseError(ctx, e.cfg, errDo)
		return nil, errDo
	}

	helps.RecordAPIResponseMetadata(ctx, e.cfg, httpResp.StatusCode, httpResp.Header.Clone())
	if httpResp.StatusCode < 200 || httpResp.StatusCode >= 300 {
		b, _ := io.ReadAll(httpResp.Body)
		if errClose := httpResp.Body.Close(); errClose != nil {
			log.Errorf("opencode executor: close response body error: %v", errClose)
		}
		helps.AppendAPIResponseChunk(ctx, e.cfg, b)
		helps.LogWithRequestID(ctx).Debugf("request error, error status: %d, error message: %s", httpResp.StatusCode, helps.SummarizeErrorBody(httpResp.Header.Get("Content-Type"), b))
		err = statusErr{code: httpResp.StatusCode, msg: string(b)}
		return nil, err
	}

	out := make(chan cliproxyexecutor.StreamChunk)
	go func() {
		defer close(out)
		defer func() {
			if errClose := httpResp.Body.Close(); errClose != nil {
				log.Errorf("opencode executor: close response body error: %v", errClose)
			}
		}()

		scanner := bufio.NewScanner(httpResp.Body)
		scanner.Buffer(nil, 52_428_800)
		var param any

		emitTranslatedLine := func(line []byte) bool {
			if responseFormat == sdktranslator.FormatOpenAIResponse {
				chunkPayload := append(bytes.Clone(line), '\n')
				select {
				case out <- cliproxyexecutor.StreamChunk{Payload: chunkPayload}:
					return true
				case <-ctx.Done():
					return false
				}
			}
			chunks := sdktranslator.TranslateStream(ctx, sdktranslator.FormatOpenAIResponse, responseFormat, req.Model, opts.OriginalRequest, body, line, &param)
			for i := range chunks {
				select {
				case out <- cliproxyexecutor.StreamChunk{Payload: chunks[i]}:
				case <-ctx.Done():
					return false
				}
			}
			return true
		}

		for scanner.Scan() {
			line := scanner.Bytes()
			helps.AppendAPIResponseChunk(ctx, e.cfg, line)

			if bytes.HasPrefix(line, dataTag) {
				dataBytes := bytes.TrimSpace(line[len(dataTag):])
				eventType := gjson.GetBytes(dataBytes, "type").String()
				if eventType == "response.completed" || eventType == "response.incomplete" || eventType == "response.done" {
					if usage, ok := helps.ParseCodexUsage(dataBytes); ok && (usage.TotalTokens > 0 || usage.InputTokens > 0) {
						reporter.Publish(ctx, usage)
					} else if usage := helps.ParseOpenAIUsage(dataBytes); usage.TotalTokens > 0 || usage.InputTokens > 0 {
						reporter.Publish(ctx, usage)
					}
				}
			}

			if !emitTranslatedLine(line) {
				return
			}
		}

		if errScan := scanner.Err(); errScan != nil {
			helps.RecordAPIResponseError(ctx, e.cfg, errScan)
			reporter.PublishFailure(ctx, errScan)
			select {
			case out <- cliproxyexecutor.StreamChunk{Err: errScan}:
			case <-ctx.Done():
			}
		}
	}()

	return &cliproxyexecutor.StreamResult{Headers: httpResp.Header.Clone(), Chunks: out}, nil
}
