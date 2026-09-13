package executor

import (
	"bufio"
	"bytes"
	"context"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	museauth "github.com/router-for-me/CLIProxyAPI/v7/internal/auth/muse"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/config"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/runtime/executor/helps"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/thinking"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/util"
	cliproxyauth "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/auth"
	cliproxyexecutor "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/executor"
	sdktranslator "github.com/router-for-me/CLIProxyAPI/v7/sdk/translator"
	log "github.com/sirupsen/logrus"
	"github.com/tidwall/gjson"
	"github.com/tidwall/sjson"
)

// MuseExecutor is a stateless executor for Muse Code subscriptions (Meta muse-spark models).
// It speaks the Meta Model API (OpenAI-compatible) with the subscription-minted API key
// plus the mandatory x-api-version header.
type MuseExecutor struct {
	cfg *config.Config
}

// NewMuseExecutor creates a new Muse executor.
func NewMuseExecutor(cfg *config.Config) *MuseExecutor {
	return &MuseExecutor{cfg: cfg}
}

// Identifier returns the executor identifier.
func (e *MuseExecutor) Identifier() string { return "muse" }

// RequestToFormat reports the upstream request format used after auth selection.
// Muse always sends OpenAI-format payloads (chat completions or responses).
func (e *MuseExecutor) RequestToFormat(_ cliproxyexecutor.Request, opts cliproxyexecutor.Options) sdktranslator.Format {
	if opts.SourceFormat == sdktranslator.FormatOpenAIResponse {
		return sdktranslator.FormatOpenAIResponse
	}
	return sdktranslator.FormatOpenAI
}

// PrepareRequest injects Muse credentials into the outgoing HTTP request.
func (e *MuseExecutor) PrepareRequest(req *http.Request, auth *cliproxyauth.Auth) error {
	if req == nil {
		return nil
	}
	token := museCreds(auth)
	if strings.TrimSpace(token) != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	req.Header.Set("x-api-version", museauth.MuseAPIVersion)
	var attrs map[string]string
	if auth != nil {
		attrs = auth.Attributes
	}
	util.ApplyCustomHeadersFromAttrs(req, attrs)
	// Cloak last so the official-client fingerprint wins; the version header
	// is reasserted inside and can never be dropped by custom expansion.
	// Note: this path carries no downstream headers, so auto mode always
	// cloaks here — Execute paths thread opts.Headers through for native
	// Muse detection instead.
	applyMuseCloakHeaders(req, museCloakForRequest(e.cfg, auth, nil))
	return nil
}

// HttpRequest injects Muse credentials into the request and executes it.
func (e *MuseExecutor) HttpRequest(ctx context.Context, auth *cliproxyauth.Auth, req *http.Request) (*http.Response, error) {
	if req == nil {
		return nil, fmt.Errorf("muse executor: request is nil")
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

// Execute performs a non-streaming request to Muse.
func (e *MuseExecutor) Execute(ctx context.Context, auth *cliproxyauth.Auth, req cliproxyexecutor.Request, opts cliproxyexecutor.Options) (resp cliproxyexecutor.Response, err error) {
	from := opts.SourceFormat
	if from == sdktranslator.FormatOpenAIResponse {
		return e.executeResponses(ctx, auth, req, opts)
	}
	responseFormat := cliproxyexecutor.ResponseFormatOrSource(opts)

	baseModel := thinking.ParseSuffix(req.Model).ModelName
	token := museCreds(auth)
	if strings.TrimSpace(token) == "" {
		return resp, statusErr{code: http.StatusUnauthorized, msg: "muse executor: missing subscription key"}
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
		return resp, fmt.Errorf("muse executor: failed to set model in payload: %w", err)
	}

	body, err = helps.ApplyRequestThinking(body, req, opts, from.String(), "muse", e.Identifier())
	if err != nil {
		return resp, err
	}

	requestedModel := helps.PayloadRequestedModel(opts, req.Model)
	requestPath := helps.PayloadRequestPath(opts)
	body = helps.ApplyPayloadConfigWithRequest(e.cfg, baseModel, to.String(), from.String(), "", body, originalTranslated, requestedModel, requestPath, opts.Headers)
	body = normalizeMuseTools(body)
	// Meta rejects tool names over 64 chars; remap those to short aliases and
	// restore originals on the way back so the harness keeps working.
	body, toolNames := remapMuseToolNames(body)
	reporter.SetTranslatedReasoningEffort(body, e.Identifier())

	url := museBaseURL(auth) + "/chat/completions"
	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(body))
	if err != nil {
		return resp, err
	}
	applyMuseHeaders(httpReq, token, false)
	var attrs map[string]string
	if auth != nil {
		attrs = auth.Attributes
	}
	util.ApplyCustomHeadersFromAttrs(httpReq, attrs)
	applyMuseCloakHeaders(httpReq, museCloakForRequest(e.cfg, auth, opts.Headers))
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
			log.Errorf("muse executor: close response body error: %v", errClose)
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
	out = restoreMuseToolNamesInJSON(out, responseFormat, toolNames)
	if responseFormat == sdktranslator.FormatOpenAIResponse {
		out = helps.EnsureResponsesUsageDetails(out)
	}
	resp = cliproxyexecutor.Response{Payload: out, Headers: httpResp.Header.Clone()}
	return resp, nil
}

// ExecuteStream performs a streaming request to Muse.
func (e *MuseExecutor) ExecuteStream(ctx context.Context, auth *cliproxyauth.Auth, req cliproxyexecutor.Request, opts cliproxyexecutor.Options) (_ *cliproxyexecutor.StreamResult, err error) {
	from := opts.SourceFormat
	if from == sdktranslator.FormatOpenAIResponse {
		return e.executeResponsesStream(ctx, auth, req, opts)
	}
	responseFormat := cliproxyexecutor.ResponseFormatOrSource(opts)

	baseModel := thinking.ParseSuffix(req.Model).ModelName
	token := museCreds(auth)
	if strings.TrimSpace(token) == "" {
		return nil, statusErr{code: http.StatusUnauthorized, msg: "muse executor: missing subscription key"}
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
		return nil, fmt.Errorf("muse executor: failed to set model in payload: %w", err)
	}

	body, err = helps.ApplyRequestThinking(body, req, opts, from.String(), "muse", e.Identifier())
	if err != nil {
		return nil, err
	}

	body, err = sjson.SetBytes(body, "stream_options.include_usage", true)
	if err != nil {
		return nil, fmt.Errorf("muse executor: failed to set stream_options in payload: %w", err)
	}
	requestedModel := helps.PayloadRequestedModel(opts, req.Model)
	requestPath := helps.PayloadRequestPath(opts)
	body = helps.ApplyPayloadConfigWithRequest(e.cfg, baseModel, to.String(), from.String(), "", body, originalTranslated, requestedModel, requestPath, opts.Headers)
	body = normalizeMuseTools(body)
	// See Execute: remap overlong tool names for Meta, restore on the way back.
	body, toolNames := remapMuseToolNames(body)
	reporter.SetTranslatedReasoningEffort(body, e.Identifier())

	url := museBaseURL(auth) + "/chat/completions"
	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(body))
	if err != nil {
		return nil, err
	}
	applyMuseHeaders(httpReq, token, true)
	var attrs map[string]string
	if auth != nil {
		attrs = auth.Attributes
	}
	util.ApplyCustomHeadersFromAttrs(httpReq, attrs)
	applyMuseCloakHeaders(httpReq, museCloakForRequest(e.cfg, auth, opts.Headers))
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
			log.Errorf("muse executor: close response body error: %v", errClose)
		}
		err = statusErr{code: httpResp.StatusCode, msg: string(b)}
		return nil, err
	}
	out := make(chan cliproxyexecutor.StreamChunk)
	go func() {
		defer close(out)
		defer func() {
			if errClose := httpResp.Body.Close(); errClose != nil {
				log.Errorf("muse executor: close response body error: %v", errClose)
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
				chunks[i] = restoreMuseToolNamesInStreamLine(chunks[i], responseFormat, toolNames)
				select {
				case out <- cliproxyexecutor.StreamChunk{Payload: chunks[i]}:
				case <-ctx.Done():
					return
				}
			}
		}
		doneChunks := helps.TranslateStreamWithClaudeInputTokens(ctx, to, responseFormat, req.Model, opts.OriginalRequest, body, []byte("[DONE]"), &param, claudeInputTokens)
		for i := range doneChunks {
			doneChunks[i] = restoreMuseToolNamesInStreamLine(doneChunks[i], responseFormat, toolNames)
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

func (e *MuseExecutor) executeResponses(ctx context.Context, auth *cliproxyauth.Auth, req cliproxyexecutor.Request, opts cliproxyexecutor.Options) (resp cliproxyexecutor.Response, err error) {
	responseFormat := cliproxyexecutor.ResponseFormatOrSource(opts)
	baseModel := thinking.ParseSuffix(req.Model).ModelName
	token := museCreds(auth)
	if strings.TrimSpace(token) == "" {
		return resp, statusErr{code: http.StatusUnauthorized, msg: "muse executor: missing subscription key"}
	}

	reporter := helps.NewExecutorUsageReporter(ctx, e, baseModel, auth)
	defer reporter.TrackFailure(ctx, &err)

	body := bytes.Clone(req.Payload)
	var errSet error
	body, errSet = sjson.SetBytes(body, "model", baseModel)
	if errSet != nil {
		return resp, fmt.Errorf("muse executor: failed to set model in payload: %w", errSet)
	}

	body = helps.SetBoolIfDifferent(body, "stream", false)

	var errThinking error
	body, errThinking = helps.ApplyRequestThinking(body, req, opts, opts.SourceFormat.String(), sdktranslator.FormatCodex.String(), e.Identifier())
	if errThinking != nil {
		return resp, errThinking
	}

	requestedModel := helps.PayloadRequestedModel(opts, req.Model)
	requestPath := helps.PayloadRequestPath(opts)
	body = helps.ApplyPayloadConfigWithRequest(e.cfg, baseModel, "openai-response", opts.SourceFormat.String(), "", body, req.Payload, requestedModel, requestPath, opts.Headers)
	body = normalizeMuseTools(body)
	body, toolNames := remapMuseToolNames(body)
	reporter.SetTranslatedReasoningEffort(body, e.Identifier())

	url := museBaseURL(auth) + "/responses"
	httpReq, errNewRequest := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(body))
	if errNewRequest != nil {
		return resp, errNewRequest
	}
	applyMuseHeaders(httpReq, token, false)
	var attrs map[string]string
	if auth != nil {
		attrs = auth.Attributes
	}
	util.ApplyCustomHeadersFromAttrs(httpReq, attrs)
	applyMuseCloakHeaders(httpReq, museCloakForRequest(e.cfg, auth, opts.Headers))

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
			log.Errorf("muse executor: close response body error: %v", errClose)
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
	out = restoreMuseToolNamesInJSON(out, responseFormat, toolNames)
	resp = cliproxyexecutor.Response{Payload: out, Headers: httpResp.Header.Clone()}
	return resp, nil
}

func (e *MuseExecutor) executeResponsesStream(ctx context.Context, auth *cliproxyauth.Auth, req cliproxyexecutor.Request, opts cliproxyexecutor.Options) (_ *cliproxyexecutor.StreamResult, err error) {
	responseFormat := cliproxyexecutor.ResponseFormatOrSource(opts)
	baseModel := thinking.ParseSuffix(req.Model).ModelName
	token := museCreds(auth)
	if strings.TrimSpace(token) == "" {
		return nil, statusErr{code: http.StatusUnauthorized, msg: "muse executor: missing subscription key"}
	}

	reporter := helps.NewExecutorUsageReporter(ctx, e, baseModel, auth)
	defer reporter.TrackFailure(ctx, &err)

	body := bytes.Clone(req.Payload)
	var errSet error
	body, errSet = sjson.SetBytes(body, "model", baseModel)
	if errSet != nil {
		return nil, fmt.Errorf("muse executor: failed to set model in payload: %w", errSet)
	}

	body = helps.SetBoolIfDifferent(body, "stream", true)

	var errThinking error
	body, errThinking = helps.ApplyRequestThinking(body, req, opts, opts.SourceFormat.String(), sdktranslator.FormatCodex.String(), e.Identifier())
	if errThinking != nil {
		return nil, errThinking
	}

	requestedModel := helps.PayloadRequestedModel(opts, req.Model)
	requestPath := helps.PayloadRequestPath(opts)
	body = helps.ApplyPayloadConfigWithRequest(e.cfg, baseModel, "openai-response", opts.SourceFormat.String(), "", body, req.Payload, requestedModel, requestPath, opts.Headers)
	body = normalizeMuseTools(body)
	body, toolNames := remapMuseToolNames(body)
	reporter.SetTranslatedReasoningEffort(body, e.Identifier())

	url := museBaseURL(auth) + "/responses"
	httpReq, errNewRequest := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(body))
	if errNewRequest != nil {
		return nil, errNewRequest
	}
	applyMuseHeaders(httpReq, token, true)
	var attrs map[string]string
	if auth != nil {
		attrs = auth.Attributes
	}
	util.ApplyCustomHeadersFromAttrs(httpReq, attrs)
	applyMuseCloakHeaders(httpReq, museCloakForRequest(e.cfg, auth, opts.Headers))

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
			log.Errorf("muse executor: close response body error: %v", errClose)
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
				log.Errorf("muse executor: close response body error: %v", errClose)
			}
		}()

		scanner := bufio.NewScanner(httpResp.Body)
		scanner.Buffer(nil, 52_428_800)
		var param any

		emitTranslatedLine := func(line []byte) bool {
			if responseFormat == sdktranslator.FormatOpenAIResponse {
				chunkPayload := restoreMuseToolNamesInStreamLine(append(bytes.Clone(line), '\n'), responseFormat, toolNames)
				select {
				case out <- cliproxyexecutor.StreamChunk{Payload: chunkPayload}:
					return true
				case <-ctx.Done():
					return false
				}
			}
			chunks := sdktranslator.TranslateStream(ctx, sdktranslator.FormatOpenAIResponse, responseFormat, req.Model, opts.OriginalRequest, body, line, &param)
			for i := range chunks {
				chunks[i] = restoreMuseToolNamesInStreamLine(chunks[i], responseFormat, toolNames)
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

// CountTokens estimates token count for Muse requests.
func (e *MuseExecutor) CountTokens(ctx context.Context, auth *cliproxyauth.Auth, req cliproxyexecutor.Request, opts cliproxyexecutor.Options) (cliproxyexecutor.Response, error) {
	baseModel := thinking.ParseSuffix(req.Model).ModelName

	from := opts.SourceFormat
	responseFormat := cliproxyexecutor.ResponseFormatOrSource(opts)
	to := sdktranslator.FromString("openai")
	isCompat := helps.APIKeyModelIsCompat(req)
	translated := helps.TranslateRequestWithAPIKeyModelCompatibility(ctx, opts.Headers, e.cfg, from, to, baseModel, req.Payload, false, isCompat)

	translated, err := helps.ApplyRequestThinking(translated, req, opts, from.String(), to.String(), e.Identifier())
	if err != nil {
		return cliproxyexecutor.Response{}, err
	}

	enc, err := helps.TokenizerForModel(baseModel)
	if err != nil {
		return cliproxyexecutor.Response{}, fmt.Errorf("muse executor: tokenizer init failed: %w", err)
	}

	count, err := helps.CountOpenAIChatTokens(enc, translated)
	if err != nil {
		return cliproxyexecutor.Response{}, fmt.Errorf("muse executor: token counting failed: %w", err)
	}

	usageJSON := helps.BuildOpenAIUsageJSON(count)
	translatedUsage := sdktranslator.TranslateTokenCount(ctx, to, responseFormat, count, usageJSON)
	return cliproxyexecutor.Response{Payload: translatedUsage}, nil
}

// Refresh reuses the already-minted subscription key without another key call.
// Meta's key endpoint is aggressively rate-limited and returns the same api_key
// per account, so a refresh that already carries one must not burn another call.
// Only a credential without a minted key attempts to mint one.
func (e *MuseExecutor) Refresh(ctx context.Context, auth *cliproxyauth.Auth) (*cliproxyauth.Auth, error) {
	log.Debugf("muse executor: refresh called")
	if refreshed, handled, err := helps.RefreshAuthViaHome(ctx, e.cfg, auth); handled {
		return refreshed, err
	}
	if auth == nil {
		return nil, fmt.Errorf("muse executor: auth is nil")
	}
	if strings.TrimSpace(museCreds(auth)) != "" {
		return auth, nil
	}
	oauthToken := ""
	if auth.Metadata != nil {
		oauthToken = museauth.ResolveMuseOAuthToken(auth.Metadata, auth.Attributes)
	}
	if strings.TrimSpace(oauthToken) == "" {
		return auth, nil
	}
	client := museauth.NewMuseAuthWithProxyURL(e.cfg, auth.ProxyURL)
	key, err := client.RequestKey(ctx, oauthToken, false)
	if err != nil {
		return nil, err
	}
	if err = museauth.EnsureSubscriptionActive(key); err != nil {
		return nil, err
	}
	apiKey := strings.TrimSpace(key.APIKey)
	if apiKey == "" {
		return nil, fmt.Errorf("muse executor: key response missing api_key")
	}
	if auth.Metadata == nil {
		auth.Metadata = make(map[string]any)
	}
	auth.Metadata["muse_api_key"] = apiKey
	auth.Metadata["type"] = "muse"
	auth.Metadata["auth_kind"] = "oauth"
	if email := strings.ToLower(strings.TrimSpace(key.UserEmail)); email != "" {
		auth.Metadata["email"] = email
	}
	if userID := strings.TrimSpace(key.UserID); userID != "" {
		auth.Metadata["user_id"] = userID
	}
	now := time.Now().Format(time.RFC3339)
	auth.Metadata["last_refresh"] = now
	if storage, ok := auth.Storage.(*museauth.TokenStorage); ok && storage != nil {
		storage.MuseAPIKey = apiKey
		storage.LastRefresh = now
	}
	return auth, nil
}

func applyMuseHeaders(r *http.Request, token string, stream bool) {
	r.Header.Set("Content-Type", "application/json")
	if strings.TrimSpace(token) != "" {
		r.Header.Set("Authorization", "Bearer "+token)
	}
	r.Header.Set("x-api-version", museauth.MuseAPIVersion)
	if stream {
		r.Header.Set("Accept", "text/event-stream")
		return
	}
	r.Header.Set("Accept", "application/json")
}

func museBaseURL(auth *cliproxyauth.Auth) string {
	if auth != nil && auth.Attributes != nil {
		if raw := strings.TrimRight(strings.TrimSpace(auth.Attributes["base_url"]), "/"); raw != "" {
			if strings.HasSuffix(strings.ToLower(raw), "/v1") {
				return raw
			}
			return raw + "/v1"
		}
	}
	return museauth.MuseAPIBaseURL
}

// museCreds extracts the minted subscription Model API key from auth.
func museCreds(a *cliproxyauth.Auth) string {
	if a == nil {
		return ""
	}
	if key := museauth.ResolveMuseAPIKey(a.Metadata, a.Attributes); strings.TrimSpace(key) != "" {
		return strings.TrimSpace(key)
	}
	if a.Metadata != nil {
		if v, ok := a.Metadata["muse_api_key"].(string); ok && strings.TrimSpace(v) != "" {
			return strings.TrimSpace(v)
		}
	}
	return ""
}

// normalizeMuseTools converts `custom` tools to `function` tools.
// Meta rejects `custom` tools on the Model API endpoint, so apply-patch style
// custom declarations must stay function tools here.
func normalizeMuseTools(body []byte) []byte {
	if len(body) == 0 || !gjson.ValidBytes(body) {
		return body
	}
	for _, path := range []string{"tools", "functions"} {
		items := gjson.GetBytes(body, path)
		if !items.Exists() || !items.IsArray() || len(items.Array()) == 0 {
			continue
		}
		changed := false
		updatedItems := make([]string, 0, len(items.Array()))
		for _, item := range items.Array() {
			raw := item.Raw
			toolType := strings.TrimSpace(item.Get("type").String())
			if strings.EqualFold(toolType, "custom") {
				if updated, err := sjson.SetBytes([]byte(raw), "type", "function"); err == nil {
					raw = string(updated)
					changed = true
				}
			}
			// Ensure function parameter schemas declare an explicit object type.
			paramPath := ""
			if item.Get("function.parameters").Exists() {
				paramPath = "function.parameters"
			} else if item.Get("parameters").Exists() && item.Get("parameters").IsObject() {
				paramPath = "parameters"
			}
			if paramPath != "" {
				paramsRaw := gjson.GetBytes([]byte(raw), paramPath).Raw
				if paramsRaw != "" && gjson.Valid(paramsRaw) {
					inlined := util.InlineLocalRefs(paramsRaw)
					paramBytes := []byte(inlined)
					if gjson.GetBytes(paramBytes, "$defs").Exists() {
						paramBytes, _ = sjson.DeleteBytes(paramBytes, "$defs")
					}
					if gjson.GetBytes(paramBytes, "definitions").Exists() {
						paramBytes, _ = sjson.DeleteBytes(paramBytes, "definitions")
					}
					if !gjson.GetBytes(paramBytes, "type").Exists() {
						paramBytes, _ = sjson.SetBytes(paramBytes, "type", "object")
					}
					if string(paramBytes) != paramsRaw {
						if updated, err := sjson.SetRawBytes([]byte(raw), paramPath, paramBytes); err == nil {
							raw = string(updated)
							changed = true
						}
					}
				}
			}
			updatedItems = append(updatedItems, raw)
		}
		if changed {
			if updated, err := sjson.SetRawBytes(body, path, helps.JoinRawJSONStrings(updatedItems)); err == nil {
				body = updated
			}
		}
	}
	return body
}
