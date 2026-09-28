package tps

import (
	"context"
	"strings"
	"time"

	coreusage "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/usage"
)

func init() {
	coreusage.RegisterPlugin(&trackerPlugin{})
}

// trackerPlugin feeds the process-wide tracker from the usage record bus.
// It follows the same registration and flag-resolution shape as the usage
// queue plugin in internal/redisqueue.
type trackerPlugin struct{}

func (p *trackerPlugin) HandleUsage(ctx context.Context, record coreusage.Record) {
	if p == nil {
		return
	}
	observe(ctx, Default(), record, time.Now())
}

// observe records one usage record as a sample when it represents real
// generation: generation enabled, not failed, a known model, and at least one
// output token over a positive latency.
func observe(ctx context.Context, tracker *Tracker, record coreusage.Record, now time.Time) {
	if tracker == nil || !coreusage.GenerateEnabled(record.Generate) || record.Failed {
		return
	}
	if strings.TrimSpace(record.Model) == "" || record.Detail.OutputTokens <= 0 || record.Latency <= 0 {
		return
	}
	// Records published by direct SDK callers may leave Stream unset on the
	// struct; the executor records it in the request context.
	stream := record.Stream
	if !stream {
		stream = coreusage.StreamFromContext(ctx)
	}
	observedAt := record.RequestedAt
	if observedAt.IsZero() {
		observedAt = now
	}
	tracker.Record(Sample{
		ObservedAt:   observedAt,
		Model:        strings.TrimSpace(record.Model),
		Alias:        strings.TrimSpace(record.Alias),
		Provider:     strings.TrimSpace(record.Provider),
		Latency:      record.Latency,
		TTFT:         record.TTFT,
		InputTokens:  record.Detail.InputTokens,
		OutputTokens: record.Detail.OutputTokens,
		Stream:       stream,
	})
}
