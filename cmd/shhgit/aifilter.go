package main

import (
	"context"
	"encoding/json"
	"log"
	"net/http"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/trebor048/shhgot/aiproviders"
	"github.com/trebor048/shhgot/core"
	"github.com/trebor048/shhgot/core/aifilter"
)

// The AI false-positive pre-filter is opt-in: it does nothing unless
// ai_filter.enabled is explicitly true. There are two stages:
//
//   - the deterministic quick filter (core/aifilter.QuickFilter), which runs
//     inline on the publisher's goroutine and makes no network call, and
//   - the LLM classifier (core/aifilter.Classifier), which only runs when a
//     provider is configured and executes on a small, bounded worker pool so a
//     slow backend cannot tie up the scan's own workers.
//
// Both stages fail open. A timeout, a full queue, a classifier error or an
// unparseable model reply all keep the finding, so the filter can only ever
// remove a finding it is confident is a placeholder.
const (
	// defaultFilterWorkers bounds how many classify calls run at once. Keeping
	// it small means a slow backend degrades the filter, never the scan.
	defaultFilterWorkers = 2
	// filterQueueFactor sizes the job queue relative to the worker count.
	filterQueueFactor = 4
	// defaultFilterConsultTimeout bounds how long publish() waits for a verdict
	// before keeping the finding. It is deliberately short: the point of the
	// pool is that the scan is not stalled by a hung model.
	defaultFilterConsultTimeout = 15 * time.Second
	// defaultFilterCallTimeout bounds one model call even if publish() has
	// already given up, so the worker returns and the slot is freed.
	defaultFilterCallTimeout = 45 * time.Second
	// defaultFilterCacheEntries / defaultFilterCacheTTL bound the classifier's
	// verdict cache. The cache is what stops the same fixture being asked about
	// once per occurrence.
	defaultFilterCacheEntries = 1000
	defaultFilterCacheTTL     = time.Hour
	// defaultSuppressRing bounds how many suppressed findings are retained for
	// display. Older entries are evicted and counted as overflow.
	defaultSuppressRing = 500
	// defaultDegradedAfter is how many classify errors in a row trip the
	// degraded flag. A single success resets the streak.
	defaultDegradedAfter = 5
)

// aiFilterPtr is the process-wide filter. It holds nil unless the feature is
// enabled; every consumer must tolerate that. It is an atomic pointer so the
// scanner can install the filter (setAIFilter) while a background source may
// already be calling publish (getAIFilter) without racing.
var aiFilterPtr atomic.Pointer[findingFilter]

// getAIFilter returns the process-wide filter, or nil when disabled.
func getAIFilter() *findingFilter { return aiFilterPtr.Load() }

// setAIFilter installs the process-wide filter.
func setAIFilter(f *findingFilter) { aiFilterPtr.Store(f) }

// filterVerdict is the outcome of consulting the filter for one finding.
type filterVerdict struct {
	Suppress bool
	Reason   string // e.g. "placeholder-pattern" or "llm:PLACEHOLDER"
	Stage    string // "quick" or "llm"
	Decision aifilter.Decision
}

// suppression is one entry in the bounded suppression ring. It carries enough
// of the finding to make the /api/suppressed listing useful without re-reading
// the file, plus the reason the filter removed it.
type suppression struct {
	Timestamp  time.Time `json:"timestamp"`
	Source     string    `json:"source"`
	URL        string    `json:"url"`
	File       string    `json:"file"`
	Signature  string    `json:"signature"`
	Secret     string    `json:"secret,omitempty"`
	Matches    []string  `json:"matches,omitempty"`
	Reason     string    `json:"reason"`
	Stage      string    `json:"stage"`
	Confidence string    `json:"confidence,omitempty"`
	KeyType    string    `json:"key_type,omitempty"`
	Provider   string    `json:"provider,omitempty"`
	Reasoning  string    `json:"reasoning,omitempty"`
}

// filterJob is one queued classify request.
type filterJob struct {
	candidate string
	context   string
	file      string
	reply     chan aifilter.Decision
}

// findingFilter holds the configured stages plus the state the API exposes.
type findingFilter struct {
	quick    bool
	classify *aifilter.Classifier

	jobs           chan *filterJob
	quit           chan struct{}
	wg             sync.WaitGroup
	consultTimeout time.Duration
	maxRing        int

	mu       sync.Mutex
	ring     []suppression
	overflow int
	total    int
	failures int
	degraded bool
}

// newFindingFilter builds and starts the filter. It returns nil when the
// feature is disabled, so callers can assign it unconditionally. A nil client
// means "quick filter only": no provider is configured, so the LLM stage is
// skipped entirely.
func newFindingFilter(cfg core.AIFilterConfig, client aiproviders.Client) *findingFilter {
	if !filterBool(cfg.Enabled, false) {
		return nil
	}

	workers := filterInt(cfg.Workers, defaultFilterWorkers)
	if workers < 1 {
		workers = 1
	}

	f := &findingFilter{
		quick:          filterBool(cfg.QuickFilter, true),
		consultTimeout: defaultFilterConsultTimeout,
		maxRing:        defaultSuppressRing,
		jobs:           make(chan *filterJob, workers*filterQueueFactor),
		quit:           make(chan struct{}),
	}

	if client != nil {
		cacheEntries := filterInt(cfg.CacheEntries, defaultFilterCacheEntries)
		cacheTTL := time.Duration(filterInt(cfg.CacheTTLSecs, int(defaultFilterCacheTTL/time.Second))) * time.Second
		generate := func(prompt string) (string, error) {
			ctx, cancel := context.WithTimeout(context.Background(), defaultFilterCallTimeout)
			defer cancel()
			return client.Chat(ctx, []aiproviders.Message{{Role: aiproviders.RoleUser, Content: prompt}})
		}
		f.classify = aifilter.NewClassifier(aifilter.LLMConfig{
			Enabled:      true,
			CacheEntries: cacheEntries,
			CacheTTL:     cacheTTL,
			Now:          time.Now,
		}, generate)
	}

	f.wg.Add(workers)
	for i := 0; i < workers; i++ {
		go f.run()
	}
	return f
}

// run is one pool worker. It never blocks the publisher: the reply channel is
// buffered, so a classify result that arrives after publish() timed out is
// dropped rather than deadlocking the worker.
func (f *findingFilter) run() {
	defer f.wg.Done()
	for {
		select {
		case job := <-f.jobs:
			f.process(job)
		case <-f.quit:
			return
		}
	}
}

func (f *findingFilter) process(job *filterJob) {
	d := f.classify.Classify(job.candidate, job.context, job.file)
	if d.Err != nil {
		f.recordFailure()
	} else {
		f.recordSuccess()
	}
	select {
	case job.reply <- d:
	default:
	}
}

// Consider runs the quick filter and, when enabled, waits a bounded time for the
// classifier. It never suppresses on a failure: an empty reply, a full queue or
// a timeout all keep the finding.
func (f *findingFilter) Consider(event *MatchEvent, contextText string) filterVerdict {
	if f == nil {
		return filterVerdict{}
	}
	candidate, context := filterInputs(event, contextText)
	if candidate == "" {
		return filterVerdict{}
	}

	if f.quick {
		if qr := aifilter.QuickFilter(candidate, context, event.File); qr.Suppress {
			return filterVerdict{Suppress: true, Reason: qr.Reason, Stage: "quick"}
		}
	}
	if f.classify == nil {
		return filterVerdict{}
	}

	job := &filterJob{
		candidate: candidate,
		context:   context,
		file:      event.File,
		reply:     make(chan aifilter.Decision, 1),
	}

	select {
	case <-f.quit:
		return filterVerdict{}
	case f.jobs <- job:
	default:
		// Queue full: the filter is falling behind, so keep the finding rather
		// than stall the scan.
		return filterVerdict{}
	}

	timer := time.NewTimer(f.consultTimeout)
	defer timer.Stop()
	select {
	case d := <-job.reply:
		if d.Suppress {
			return filterVerdict{
				Suppress: true,
				Reason:   "llm:" + strings.ToUpper(d.KeyType),
				Stage:    "llm",
				Decision: d,
			}
		}
		return filterVerdict{}
	case <-timer.C:
		return filterVerdict{}
	case <-f.quit:
		return filterVerdict{}
	}
}

// Record appends a suppressed finding to the bounded ring. It must only be
// called after Consider returned Suppress=true.
func (f *findingFilter) Record(event *MatchEvent, v filterVerdict) {
	if f == nil || !v.Suppress {
		return
	}
	entry := suppression{
		Timestamp:  time.Now(),
		Source:     sourceName(event.Source),
		URL:        event.Url,
		File:       event.File,
		Signature:  event.Signature,
		Secret:     event.Secret,
		Matches:    append([]string(nil), event.Matches...),
		Reason:     v.Reason,
		Stage:      v.Stage,
		Confidence: v.Decision.Confidence,
		KeyType:    v.Decision.KeyType,
		Provider:   v.Decision.Provider,
		Reasoning:  v.Decision.Reasoning,
	}

	f.mu.Lock()
	f.total++
	f.ring = append(f.ring, entry)
	if len(f.ring) > f.maxRing {
		drop := len(f.ring) - f.maxRing
		f.ring = append(f.ring[:0], f.ring[drop:]...)
		f.overflow += drop
	}
	f.mu.Unlock()
}

func (f *findingFilter) recordFailure() {
	f.mu.Lock()
	f.failures++
	if f.failures >= defaultDegradedAfter {
		f.degraded = true
	}
	f.mu.Unlock()
}

func (f *findingFilter) recordSuccess() {
	f.mu.Lock()
	f.failures = 0
	f.mu.Unlock()
}

// Suppressed returns a copy of the ring, oldest first.
func (f *findingFilter) Suppressed() []suppression {
	if f == nil {
		return []suppression{}
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	out := make([]suppression, len(f.ring))
	copy(out, f.ring)
	return out
}

// Degraded reports whether the classifier has failed enough times in a row that
// its verdicts can no longer be trusted. It is sticky once tripped.
func (f *findingFilter) Degraded() bool {
	if f == nil {
		return false
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.degraded
}

// filterStatus is the filter's self-reported state for the health endpoint.
type filterStatus struct {
	Enabled            bool `json:"enabled"`
	Quick              bool `json:"quick_filter"`
	LLM                bool `json:"llm"`
	Degraded           bool `json:"degraded"`
	SuppressedTotal    int  `json:"suppressed_total"`
	SuppressedOverflow int  `json:"suppressed_overflow"`
}

// Status returns a snapshot of the filter's state. A nil filter (not enabled)
// reports every field zero.
func (f *findingFilter) Status() filterStatus {
	if f == nil {
		return filterStatus{}
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	return filterStatus{
		Enabled:            true,
		Quick:              f.quick,
		LLM:                f.classify != nil,
		Degraded:           f.degraded,
		SuppressedTotal:    f.total,
		SuppressedOverflow: f.overflow,
	}
}

// Close stops the worker pool. It is safe to call on a nil filter and is used by
// tests; the process-wide filter lives until the process exits.
func (f *findingFilter) Close() {
	if f == nil {
		return
	}
	select {
	case <-f.quit:
		return
	default:
	}
	close(f.quit)
	f.wg.Wait()
}

// initAIFilter resolves the classifier's provider and builds the process-wide
// filter. It returns nil when the feature is disabled or no provider can be
// resolved (in which case only the quick filter would run).
func initAIFilter(cfg *core.Config) *findingFilter {
	if cfg == nil || !filterBool(cfg.AIFilter.Enabled, false) {
		return nil
	}

	var client aiproviders.Client
	if settings, ok := resolveFilterSettings(cfg.AIFilter, aiSettingsStoreForFilter(cfg)); ok {
		c, err := aiproviders.New(settings)
		if err != nil {
			log.Printf("[ai-filter] LLM classifier disabled: %v", err)
		} else {
			client = c
		}
	}

	return newFindingFilter(cfg.AIFilter, client)
}

// resolveFilterSettings picks the provider for the classifier. Explicit
// ai_filter.provider/model/base_url values win; anything left blank is filled
// from the saved AI settings (which themselves fall back to the environment and
// the built-in provider defaults). ok is false when no provider is named, so
// the quick filter runs alone.
func resolveFilterSettings(cfg core.AIFilterConfig, store *aiproviders.Store) (aiproviders.Settings, bool) {
	var saved aiproviders.Settings
	if store != nil {
		if got, err := store.Get(); err == nil {
			saved = got
		}
	}

	provider := strings.TrimSpace(cfg.Provider)
	if provider == "" {
		provider = strings.TrimSpace(saved.Provider)
	}
	if provider == "" {
		return aiproviders.Settings{}, false
	}

	s := aiproviders.Settings{Provider: provider, APIKey: saved.APIKey}
	if strings.EqualFold(provider, saved.Provider) {
		s.BaseURL = saved.BaseURL
		s.Model = saved.Model
	} else {
		// A provider switch must not inherit the previous provider's endpoint or
		// model, exactly as the dashboard's settings merge treats it.
		d := aiproviders.Defaults(provider)
		s.BaseURL = d.BaseURL
		s.Model = d.Model
	}
	if m := strings.TrimSpace(cfg.Model); m != "" {
		s.Model = m
	}
	if u := strings.TrimSpace(cfg.BaseURL); u != "" {
		s.BaseURL = u
	}
	return aiproviders.WithEnvFallback(s), true
}

// aiSettingsStoreForFilter returns the settings store initAIReview created, or
// a read-only store at the conventional location when the review engine has not
// been initialised (plain terminal mode). Reading a missing file is not an
// error: the store then reports the environment/defaults.
func aiSettingsStoreForFilter(cfg *core.Config) *aiproviders.Store {
	if aiSettingsStore != nil {
		return aiSettingsStore
	}
	dir := "ai_review"
	if cfg != nil {
		if s := strings.TrimSpace(cfg.AIReview.ReviewDir); s != "" {
			dir = s
		}
	}
	return aiproviders.NewStore(filepath.Join(dir, "settings.json"))
}

// filterInputs selects the candidate secret and the context the filter reasons
// about. The whole file is never used as context: a marker anywhere in it would
// otherwise suppress unrelated findings. When the scan did not capture a window,
// a single line around the secret is derived cheaply for the filter alone.
func filterInputs(event *MatchEvent, contextText string) (candidate, context string) {
	candidate = strings.TrimSpace(event.Secret)
	if candidate == "" && len(event.Matches) > 0 {
		candidate = strings.TrimSpace(event.Matches[0])
	}
	context = contextText
	if context == "" && event.FileContent != "" && event.SecretLine > 0 {
		context = core.ExtractContext([]byte(event.FileContent), event.SecretLine, 1, maxStoreFileBytes)
	}
	return candidate, context
}

// filterBool / filterInt mirror core's absent-key semantics: a nil pointer (or a
// non-positive int) keeps the documented default.
func filterBool(v *bool, def bool) bool {
	if v != nil {
		return *v
	}
	return def
}

func filterInt(v *int, def int) int {
	if v != nil && *v > 0 {
		return *v
	}
	return def
}

// aiFilterDegraded is the process-wide degraded flag, for the health and stats
// endpoints. It is false when the filter is not enabled.
func aiFilterDegraded() bool {
	return getAIFilter().Degraded()
}

// aiFilterStatus is the process-wide filter status, for the health endpoint.
func aiFilterStatus() filterStatus {
	return getAIFilter().Status()
}

// suppressedListing is the GET /api/suppressed response: the bounded ring
// plus the totals the panel's overflow banner reads. Key names match
// filterStatus so both endpoints speak the same vocabulary.
type suppressedListing struct {
	Suppressed         []suppression `json:"suppressed"`
	SuppressedTotal    int           `json:"suppressed_total"`
	SuppressedOverflow int           `json:"suppressed_overflow"`
}

// getSuppressed serves GET /api/suppressed: the bounded ring of findings the
// filter removed this run, each with the reason it was removed. The envelope
// is always present, with an empty ring when nothing has been suppressed.
func getSuppressed(w http.ResponseWriter, r *http.Request) {
	f := getAIFilter()
	st := f.Status()
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(suppressedListing{
		Suppressed:         f.Suppressed(),
		SuppressedTotal:    st.SuppressedTotal,
		SuppressedOverflow: st.SuppressedOverflow,
	})
}
