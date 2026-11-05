package handlers

import (
	"context"
	"fmt"
	"net/http"
	"strings"

	"github.com/MoeForge/moego/ojson"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/registry"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/websearch"
	sdktranslator "github.com/router-for-me/CLIProxyAPI/v8/sdk/translator"
)

// routeSearchKey marks a context already running a route search, so the
// nested search request can never start another one.
type routeSearchKey struct{}

// antigravityRouteSearch answers proxy search calls with Google Search
// grounding on the route's own Antigravity accounts.
//
// Antigravity grounds only requests whose sole tool is web search, and
// rejects googleSearch next to function declarations. So each query the
// model issues runs as its own search-only request, the same request
// Claude Code's WebSearch tool sends, through the normal execution path:
// auth selection, cooldowns, retries, translation and usage all apply.
// It returns nil when the route cannot ground the model, which leaves the
// configured provider chain to answer.
func (h *BaseAPIHandler) antigravityRouteSearch(ctx context.Context, providers []string, model string) websearch.RouteSearcher {
	if ctx != nil && ctx.Value(routeSearchKey{}) != nil {
		return nil
	}
	if !routesTo(providers, "antigravity") || registry.AntigravityWebSearchModelFor(model) == "" {
		return nil
	}
	return func(searchCtx context.Context, query string) (websearch.SearchResponse, error) {
		body := ojson.NewDocString(`{"model":"","max_tokens":4096,"messages":[{"role":"user","content":""}],"tools":[{"type":"web_search_20250305","name":"web_search","max_uses":5}]}`)
		_ = body.Set("model", model)
		_ = body.Set("messages.0.content", query)
		searchCtx = context.WithValue(searchCtx, routeSearchKey{}, true)
		resp, errMsg := h.ExecuteModel(searchCtx, ModelExecutionRequest{
			EntryProtocol:  sdktranslator.FormatClaude.String(),
			ExitProtocol:   sdktranslator.FormatClaude.String(),
			Model:          model,
			Body:           body.Bytes(),
			ForcedProvider: "antigravity",
		})
		if errMsg != nil {
			return websearch.SearchResponse{}, routeSearchError(errMsg.StatusCode, errMsg.Error)
		}
		response := websearch.ParseClaudeSearchResponse(resp.Body, model)
		response.Provider = "antigravity"
		return response, nil
	}
}

func routeSearchError(status int, err error) error {
	if err == nil {
		return fmt.Errorf("antigravity grounded search: %s", http.StatusText(status))
	}
	return fmt.Errorf("antigravity grounded search: %w", err)
}

// routesTo reports whether the request's provider list includes provider.
func routesTo(providers []string, provider string) bool {
	for _, candidate := range providers {
		if strings.EqualFold(strings.TrimSpace(candidate), provider) {
			return true
		}
	}
	return false
}
