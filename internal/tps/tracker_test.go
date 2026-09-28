package tps

import (
	"math"
	"strconv"
	"testing"
	"time"

	coreusage "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/usage"
)

var base = time.Date(2026, 9, 28, 12, 0, 0, 0, time.UTC)

func sample(model string, at time.Time, latency, ttft time.Duration, output int64) Sample {
	return Sample{
		ObservedAt:   at,
		Model:        model,
		Latency:      latency,
		TTFT:         ttft,
		OutputTokens: output,
		Stream:       true,
	}
}

// TestSampleTPSExcludesTimeToFirstToken locks the definition clients display:
// 100 output tokens over 1s of generation after a 400ms TTFT is 100 TPS, not
// 71.4 TPS measured against the full request duration.
func TestSampleTPSExcludesTimeToFirstToken(t *testing.T) {
	s := sample("m", base, 1400*time.Millisecond, 400*time.Millisecond, 100)
	if got := s.GenerationWindow(); got != time.Second {
		t.Fatalf("GenerationWindow() = %v, want 1s", got)
	}
	if got := s.TPS(); got != 100 {
		t.Fatalf("TPS() = %v, want 100", got)
	}
}

// TestSampleTPSFallsBackToLatencyWithoutTTFT keeps non-streaming requests
// measurable when the upstream reports no time to first token.
func TestSampleTPSFallsBackToLatencyWithoutTTFT(t *testing.T) {
	s := sample("m", base, 2*time.Second, 0, 250)
	if got := s.GenerationWindow(); got != 2*time.Second {
		t.Fatalf("GenerationWindow() = %v, want 2s", got)
	}
	if got := s.TPS(); got != 125 {
		t.Fatalf("TPS() = %v, want 125", got)
	}
}

// TestSampleTPSIgnoresTTFTAtOrBeyondLatency guards the degenerate reports
// where TTFT was never set and still holds its zero value, or was measured as
// the whole request. Those must not produce a negative or infinite rate.
func TestSampleTPSIgnoresTTFTAtOrBeyondLatency(t *testing.T) {
	for _, ttft := range []time.Duration{0, 2 * time.Second} {
		s := sample("m", base, 2*time.Second, ttft, 100)
		if got := s.GenerationWindow(); got != 2*time.Second {
			t.Fatalf("TTFT %v: GenerationWindow() = %v, want 2s", ttft, got)
		}
		if got := s.TPS(); got != 50 {
			t.Fatalf("TTFT %v: TPS() = %v, want 50", ttft, got)
		}
	}
}

func TestSampleTPSZeroWithoutGeneration(t *testing.T) {
	// Zero output tokens and zero latency both have no rate to report.
	if got := sample("m", base, time.Second, 0, 0).TPS(); got != 0 {
		t.Fatalf("no output TPS() = %v, want 0", got)
	}
	if got := sample("m", base, 0, 0, 100).TPS(); got != 0 {
		t.Fatalf("no latency TPS() = %v, want 0", got)
	}
}

// TestLatestReturnsNewestRequestForModel is the base case the GUI polls for:
// the most recent request that model served, ignoring other models.
func TestLatestReturnsNewestRequestForModel(t *testing.T) {
	tracker := New()
	tracker.Record(sample("fast", base, time.Second, 0, 100))
	tracker.Record(sample("other", base.Add(time.Minute), 4*time.Second, 0, 40))
	newest := sample("fast", base.Add(2*time.Minute), 2*time.Second, 0, 200)
	tracker.Record(newest)

	got, ok := tracker.Latest("fast")
	if !ok {
		t.Fatal("Latest(fast) not found, want the newest fast request")
	}
	if !got.ObservedAt.Equal(newest.ObservedAt) || got.OutputTokens != 200 {
		t.Fatalf("Latest(fast) = %+v, want the 200-token request at %v", got, newest.ObservedAt)
	}
}

// TestLatestTracksSuccessiveRequests locks the polling behavior: each new
// request for the model becomes the reported one.
func TestLatestTracksSuccessiveRequests(t *testing.T) {
	tracker := New()
	tracker.Record(sample("m", base, time.Second, 0, 100))
	if got, _ := tracker.Latest("m"); got.OutputTokens != 100 {
		t.Fatalf("after first request = %d tokens, want 100", got.OutputTokens)
	}
	tracker.Record(sample("m", base.Add(time.Minute), 2*time.Second, 0, 200))
	got, ok := tracker.Latest("m")
	if !ok || got.OutputTokens != 200 {
		t.Fatalf("Latest = %+v, want the second request", got)
	}
}

// TestRecordDropsOutOfOrderSample keeps a late-arriving older record from
// regressing the reported request, which a single-slot-per-model store would
// otherwise do.
func TestRecordDropsOutOfOrderSample(t *testing.T) {
	tracker := New()
	tracker.Record(sample("m", base.Add(2*time.Minute), 2*time.Second, 0, 200))
	tracker.Record(sample("m", base, 10*time.Second, 0, 100))

	got, ok := tracker.Latest("m")
	if !ok || got.OutputTokens != 200 {
		t.Fatalf("Latest = %+v, want the newer request to survive the late arrival", got)
	}
	// A record at the same instant is not newer, so the stored one stands.
	tracker.Record(sample("m", base.Add(2*time.Minute), time.Second, 0, 999))
	if got, _ := tracker.Latest("m"); got.OutputTokens != 200 {
		t.Fatalf("Latest = %+v, want an equal-timestamp record ignored", got)
	}
}

// TestLatestMatchesAliasAndModelName covers how a client names the model: the
// upstream model id, or the alias the client actually requested.
func TestLatestMatchesAliasAndModelName(t *testing.T) {
	tracker := New()
	aliased := sample("claude-sonnet-4-5", base, time.Second, 0, 100)
	aliased.Alias = "fast"
	tracker.Record(aliased)

	for _, model := range []string{"fast", "claude-sonnet-4-5"} {
		got, ok := tracker.Latest(model)
		if !ok {
			t.Fatalf("model %q: not found, want the aliased request", model)
		}
		if got.Model != "claude-sonnet-4-5" || got.Alias != "fast" {
			t.Fatalf("model %q: Latest = %+v", model, got)
		}
	}
}

// TestLatestWithoutModelSpansEveryModel keeps the unfiltered reading useful
// for a dashboard that shows all models at once.
func TestLatestWithoutModelSpansEveryModel(t *testing.T) {
	tracker := New()
	tracker.Record(sample("a", base, time.Second, 0, 100))
	tracker.Record(sample("b", base.Add(time.Minute), time.Second, 0, 300))

	got, ok := tracker.Latest("")
	if !ok || got.Model != "b" {
		t.Fatalf("Latest(\"\") = %+v, want the newest across models", got)
	}
}

// TestLatestUnknownModelIsEmpty is the pre-first-request state: a model nobody
// has called reports nothing rather than a fabricated rate.
func TestLatestUnknownModelIsEmpty(t *testing.T) {
	tracker := New()
	tracker.Record(sample("known", base, time.Second, 0, 100))

	if got, ok := tracker.Latest("never-called"); ok {
		t.Fatalf("Latest = %+v, want no match", got)
	}
}

// TestRecordEvictsOldestBeyondModelCap keeps the in-memory footprint bounded
// when clients churn through many distinct models.
func TestRecordEvictsOldestBeyondModelCap(t *testing.T) {
	tracker := New()
	for i := range maxModels + 10 {
		tracker.Record(sample("model-"+strconv.Itoa(i), base.Add(time.Duration(i)*time.Second), time.Second, 0, 100))
	}
	tracker.mu.Lock()
	count := len(tracker.latest)
	tracker.mu.Unlock()
	if count > maxModels {
		t.Fatalf("stored models = %d, want at most %d", count, maxModels)
	}
}

// TestRecordEvictsTheOldestFirst keeps the cap biased toward recent activity,
// so a model in use is not the one dropped.
func TestRecordEvictsTheOldestFirst(t *testing.T) {
	tracker := New()
	for i := range maxModels {
		tracker.Record(sample("model-"+strconv.Itoa(i), base.Add(time.Duration(i)*time.Second), time.Second, 0, 100))
	}
	// One more model forces an eviction; the oldest observed model goes.
	tracker.Record(sample("newcomer", base.Add(time.Hour), time.Second, 0, 100))

	if _, ok := tracker.Latest("model-0"); ok {
		t.Fatal("the oldest model survived eviction, want it dropped")
	}
	if _, ok := tracker.Latest("newcomer"); !ok {
		t.Fatal("the newest model was evicted, want it retained")
	}
}

// TestRecordIgnoresIncompleteSamples keeps unusable records out of history.
func TestRecordIgnoresIncompleteSamples(t *testing.T) {
	tracker := New()
	tracker.Record(Sample{ObservedAt: base, Latency: time.Second, OutputTokens: 100}) // no model
	tracker.Record(Sample{Model: "m", Latency: time.Second, OutputTokens: 100})       // no time
	if _, ok := tracker.Latest("m"); ok {
		t.Fatal("Latest found a sample, want nothing recorded")
	}
}

// TestObserveRecordsGeneratingRequests pins which usage records become
// samples: real generations only, with the record timestamp preserved and the
// stream flag resolved from the request context when the struct omits it.
func TestObserveRecordsGeneratingRequests(t *testing.T) {
	tracker := New()
	at := base.Add(90 * time.Second)
	streamCtx := coreusage.WithStream(t.Context(), true)

	observe(streamCtx, tracker, coreusage.Record{
		Model:       "m",
		Provider:    "codex",
		RequestedAt: at,
		Latency:     3 * time.Second,
		TTFT:        time.Second,
		Detail:      coreusage.Detail{InputTokens: 10, OutputTokens: 200},
	}, base)

	got, ok := tracker.Latest("m")
	if !ok {
		t.Fatal("Latest not found, want the recorded request")
	}
	if !got.ObservedAt.Equal(at) {
		t.Fatalf("ObservedAt = %v, want the record timestamp %v", got.ObservedAt, at)
	}
	if !got.Stream {
		t.Fatal("Stream = false, want the context stream flag")
	}
	if math.Abs(got.TPS()-100) > 1e-9 {
		t.Fatalf("TPS = %v, want 100", got.TPS())
	}
	if got.Provider != "codex" || got.InputTokens != 10 {
		t.Fatalf("sample = %+v, want provider and input tokens preserved", got)
	}
}

// TestObserveRejectsNonGeneratingAndFailedRecords keeps failed, disabled and
// token-free requests out of the throughput report, so the numbers a user
// reads always describe tokens that were actually produced.
func TestObserveRejectsNonGeneratingAndFailedRecords(t *testing.T) {
	ctx := t.Context()
	generating := coreusage.Record{
		Model: "m", Latency: time.Second, RequestedAt: base,
		Detail: coreusage.Detail{OutputTokens: 100},
	}

	cases := []struct {
		name   string
		mutate func(*coreusage.Record)
	}{
		{"failed", func(r *coreusage.Record) { r.Failed = true }},
		{"generate disabled", func(r *coreusage.Record) { r.Generate = coreusage.GenerateFlag(false) }},
		{"no output tokens", func(r *coreusage.Record) { r.Detail.OutputTokens = 0 }},
		{"no latency", func(r *coreusage.Record) { r.Latency = 0 }},
		{"no model", func(r *coreusage.Record) { r.Model = "" }},
	}
	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			tracker := New()
			record := generating
			testCase.mutate(&record)
			observe(ctx, tracker, record, base)
			if got, ok := tracker.Latest("m"); ok {
				t.Fatalf("Latest = %+v, want nothing recorded", got)
			}
		})
	}
}

// TestObserveDefaultsObservedAtToNow keeps a sample timeable when the record
// carries no request timestamp.
func TestObserveDefaultsObservedAtToNow(t *testing.T) {
	tracker := New()
	now := base.Add(5 * time.Minute)
	observe(t.Context(), tracker, coreusage.Record{
		Model: "m", Latency: time.Second, Detail: coreusage.Detail{OutputTokens: 100},
	}, now)

	got, ok := tracker.Latest("m")
	if !ok || !got.ObservedAt.Equal(now) {
		t.Fatalf("Latest = %+v, want the injected now", got)
	}
}
