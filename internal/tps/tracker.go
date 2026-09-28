// Package tps tracks per-model generation throughput (tokens per second) for
// the requests the proxy runtime has already served.
//
// Records arrive on the usage plugin bus (see plugin.go) and only the most
// recent generation per model is retained, so a polling client can read the
// throughput of a model's last request without the endpoint doing any work per
// upstream call.
package tps

import (
	"strings"
	"sync"
	"time"
)

// maxModels bounds how many distinct models keep a sample in memory. Each
// model holds a single fixed-size record, so the package costs at most
// maxModels*sizeof(Sample) bytes.
const maxModels = 256

// Sample is one completed generation request, reduced to what throughput
// reporting needs. Failed and non-generating requests never become samples.
type Sample struct {
	// ObservedAt is the request completion time. The newest sample per model
	// wins, so this is also what a polling client reads as "the last request".
	ObservedAt time.Time
	// Model is the upstream model that served the request.
	Model string
	// Alias is the client-facing model name when one was used.
	Alias    string
	Provider string
	// Latency is the total request latency.
	Latency time.Duration
	// TTFT is the time to first token; zero when the upstream reported none.
	TTFT        time.Duration
	InputTokens int64
	// OutputTokens is the completion token count, the numerator of TPS.
	OutputTokens int64
	Stream       bool
}

// GenerationWindow is the time during which tokens were produced. TTFT is
// queueing plus prefill rather than generation, so it is subtracted whenever
// the upstream reported one. Requests without a TTFT fall back to full
// latency.
func (s Sample) GenerationWindow() time.Duration {
	if s.TTFT > 0 && s.TTFT < s.Latency {
		return s.Latency - s.TTFT
	}
	return s.Latency
}

// TPS is generation throughput: output tokens per second of generation.
func (s Sample) TPS() float64 {
	window := s.GenerationWindow()
	if window <= 0 || s.OutputTokens <= 0 {
		return 0
	}
	return float64(s.OutputTokens) / window.Seconds()
}

// Reset clears all retained samples. It exists for tests, which share the
// process-wide tracker and would otherwise leak samples across cases.
func (t *Tracker) Reset() {
	if t == nil {
		return
	}
	t.mu.Lock()
	t.latest = make(map[string]Sample)
	t.mu.Unlock()
}

// Tracker is a concurrency-safe map of each model's most recent generation.
type Tracker struct {
	mu sync.Mutex
	// latest is keyed by alias+model so one model can be looked up by either
	// name; see cutKey.
	latest map[string]Sample
}

// New returns an empty tracker.
func New() *Tracker {
	return &Tracker{latest: make(map[string]Sample)}
}

// defaultTracker backs the process-wide tracker fed by the usage plugin and
// read by the HTTP endpoint.
var defaultTracker = New()

// Default returns the process-wide tracker.
func Default() *Tracker { return defaultTracker }

// Record keeps the sample as this model's last request when it is newer than
// what is already stored. An out-of-order record for the same model is
// dropped, so a late arrival can never regress the reported request. New
// models are admitted until maxModels is reached, after which the model whose
// newest sample is oldest is evicted.
func (t *Tracker) Record(sample Sample) {
	if t == nil || sample.ObservedAt.IsZero() || strings.TrimSpace(sample.Model) == "" {
		return
	}
	key := historyKey(sample)

	t.mu.Lock()
	defer t.mu.Unlock()
	if existing, ok := t.latest[key]; ok {
		if !sample.ObservedAt.After(existing.ObservedAt) {
			return
		}
	} else if len(t.latest) >= maxModels {
		t.evictOldestLocked()
	}
	t.latest[key] = sample
}

// evictOldestLocked drops the model whose newest sample is oldest, so recent
// activity is what survives the cap. Callers hold t.mu.
func (t *Tracker) evictOldestLocked() {
	var (
		victimKey string
		victimAt  time.Time
		found     bool
	)
	for key, sample := range t.latest {
		if !found || sample.ObservedAt.Before(victimAt) {
			victimKey, victimAt, found = key, sample.ObservedAt, true
		}
	}
	if found {
		delete(t.latest, victimKey)
	}
}

// historyKey namespaces a sample by the alias the client requested, so the
// same model served under two client-facing names keeps one sample each.
func historyKey(sample Sample) string {
	alias := strings.TrimSpace(sample.Alias)
	if alias == "" {
		return sample.Model
	}
	return alias + "\x00" + sample.Model
}

// cutKey splits a history key back into its alias and model names.
func cutKey(key string) (alias, model string, hasAlias bool) {
	idx := strings.IndexByte(key, 0)
	if idx < 0 {
		return "", key, false
	}
	return key[:idx], key[idx+1:], true
}

// servesKey reports whether a stored sample answers a query for model. A
// client may ask with the upstream model name or the alias it requested,
// since the alias is what a GUI knows as the selected model.
func servesKey(key, model string) bool {
	if key == model {
		return true
	}
	alias, name, hasAlias := cutKey(key)
	return name == model || (hasAlias && alias == model)
}

// Latest returns the most recent generation for model, or the most recent
// across every model when model is empty. The bool reports whether any
// matching request has been served at all.
func (t *Tracker) Latest(model string) (Sample, bool) {
	if t == nil {
		return Sample{}, false
	}
	model = strings.TrimSpace(model)

	t.mu.Lock()
	defer t.mu.Unlock()

	var (
		newest Sample
		found  bool
	)
	for key, sample := range t.latest {
		if model != "" && !servesKey(key, model) {
			continue
		}
		if !found || sample.ObservedAt.After(newest.ObservedAt) {
			newest, found = sample, true
		}
	}
	return newest, found
}
