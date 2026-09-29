package api

import (
	"net/http"
	"strings"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/thinking"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/tps"
)

// lastRequestStatsResponse is the body of GET /v1/last-request-stats: a single
// flat object describing the most recent generation request, with no nesting.
//
// Compatibility policy (this endpoint is consumed by external integrations):
//   - The route and the optional "model" query parameter are stable under
//     /v1. Changes are additive only: new optional fields may appear, and
//     existing fields do not change type. `tps` is the meaning that changed
//     after it shipped: its denominator is total latency, not the first-byte
//     tail. That tail was the transfer on a buffered body.
//   - There is no "no data" representation inside the body: when no request
//     has been served the endpoint answers 404 with the standard error
//     envelope, so a client has exactly two states to handle — 200 with this
//     object, or 404. A model that has not run, or an idle session, is the
//     404 case.
//   - TPS is output tokens per second of total request latency. It used to
//     exclude TTFT. That exclusion measured the first-byte tail, not decode
//     time, once TTFT was the first response byte of a buffered body.
//   - Errors are {"error": string} with HTTP semantics (401 bad key,
//     404 no matching request), matching the v1 authentication middleware
//     and the inference quota endpoint envelope on this group.
//   - The machine-readable contract lives in api/v1-last-request-stats.schema.json
//     and is enforced by TestLastRequestStatsSchemaParity.
//
// The route was named /v1/last-request-tps in v8.0.901 and renamed to
// /v1/last-request-stats in v8.0.902 with the body unchanged, because it
// reports more than throughput. The old path is not kept: no client had
// consumed it yet.
type lastRequestStatsResponse struct {
	// Model is the model that served the request.
	Model string `json:"model"`
	// Alias is the client-requested model name, when the request used one.
	Alias    string `json:"alias,omitempty"`
	Provider string `json:"provider,omitempty"`
	// At is the request completion time.
	At time.Time `json:"at"`
	// DurationMs is the total request latency.
	DurationMs int64 `json:"duration_ms"`
	// TTFTMs is the first response byte, zero when none was observed.
	TTFTMs int64 `json:"ttft_ms"`
	// GenerationMs is DurationMs minus TTFTMs when TTFT falls inside the
	// request, otherwise the full duration. TTFT is the first response
	// byte, so this interval is the transfer when the upstream buffers.
	GenerationMs int64 `json:"generation_ms"`
	InputTokens  int64 `json:"input_tokens"`
	OutputTokens int64 `json:"output_tokens"`
	// TPS is OutputTokens per second of DurationMs.
	TPS    float64 `json:"tps"`
	Stream bool    `json:"stream"`
}

// handleLastRequestStats reports throughput and token/timing stats of the last
// generation request this proxy served for a model.
//
// It is authenticated with the inference API key (v1 group middleware) and
// reads the in-process tracker only, so it never calls upstream and stays fast
// enough for a client UI to poll on an interval (e.g. 30s) while visible.
// Omitting the model parameter matches the most recent request across all
// models. A 404 means nothing matched yet, which is a normal idle state rather
// than a failure.
func (s *Server) handleLastRequestStats(c *gin.Context) {
	model := strings.TrimSpace(c.Query("model"))
	if model == "" {
		model = strings.TrimSpace(c.Query("model_id"))
	}
	if model != "" {
		if parsed := thinking.ParseSuffix(model); strings.TrimSpace(parsed.ModelName) != "" {
			model = strings.TrimSpace(parsed.ModelName)
		}
	}

	sample, ok := tps.Default().Latest(model)
	if !ok {
		if model == "" {
			c.JSON(http.StatusNotFound, gin.H{"error": "no request served yet"})
			return
		}
		c.JSON(http.StatusNotFound, gin.H{"error": "no request served for model " + model})
		return
	}
	c.JSON(http.StatusOK, lastRequestStatsResponse{
		Model:        sample.Model,
		Alias:        sample.Alias,
		Provider:     sample.Provider,
		At:           sample.ObservedAt.UTC(),
		DurationMs:   sample.Latency.Milliseconds(),
		TTFTMs:       sample.TTFT.Milliseconds(),
		GenerationMs: sample.GenerationWindow().Milliseconds(),
		InputTokens:  sample.InputTokens,
		OutputTokens: sample.OutputTokens,
		TPS:          sample.TPS(),
		Stream:       sample.Stream,
	})
}
