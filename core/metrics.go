package core

import (
	"math"
	"sync"
	"sync/atomic"
	"time"
)

// Latency histograms use fixed log-spaced buckets so a bounded, O(1) Observe
// can cover the 1ms..60s range where validation, scanning, webhook delivery
// and code search latencies actually live. Sixteen buckets per decade keep the
// worst-case interpolation error near 7% of the reported value without letting
// the bucket array grow with traffic.
const (
	histogramMinLatency = time.Millisecond
	histogramMaxLatency = time.Minute
	histogramPerDecade  = 16
)

// Subsystem names pre-registered by NewMetrics. Callers may use any other
// non-empty name too; it is registered lazily on first use.
const (
	SubsystemValidation = "validation"
	SubsystemScan       = "scan"
	SubsystemWebhook    = "webhook"
	SubsystemSearch     = "search"
)

var (
	// histogramBounds holds the n+1 bucket edges; bucket i is the half-open
	// range [bounds[i], bounds[i+1]). The first edge is the minimum and the
	// last is the maximum, so values below or above are clamped into the end
	// buckets instead of being dropped.
	histogramBounds = buildLogBuckets(histogramMinLatency, histogramMaxLatency, histogramPerDecade)
	histogramRatio  = math.Pow(10, 1.0/float64(histogramPerDecade))
)

// buildLogBuckets returns the bucket edges spanning [min, max] with the given
// number of buckets per decade of magnitude. It returns a two-edge slice when
// the arguments are unusable so callers always have at least one bucket.
func buildLogBuckets(min, max time.Duration, perDecade int) []time.Duration {
	if min <= 0 || max <= min || perDecade <= 0 {
		return []time.Duration{0, max}
	}
	ratio := math.Pow(10, 1.0/float64(perDecade))
	buckets := int(math.Ceil(math.Log10(float64(max)/float64(min)) * float64(perDecade)))
	if buckets < 1 {
		buckets = 1
	}
	bounds := make([]time.Duration, 0, buckets+1)
	bounds = append(bounds, min)
	v := float64(min)
	for i := 1; i < buckets; i++ {
		v *= ratio
		bounds = append(bounds, time.Duration(math.Round(v)))
	}
	bounds = append(bounds, max)
	for i := 1; i < len(bounds); i++ {
		if bounds[i] <= bounds[i-1] {
			bounds[i] = bounds[i-1] + 1
		}
	}
	if last := len(bounds) - 1; bounds[last] > max {
		bounds[last] = max
	}
	return bounds
}

// histogramBucketIndex returns the bucket holding d. The log-spaced edges let
// the common case resolve in one logarithm; the two short loops correct for
// floating-point drift around an edge.
func histogramBucketIndex(d time.Duration) int {
	n := len(histogramBounds) - 1
	if d <= histogramBounds[0] {
		return 0
	}
	if d >= histogramBounds[n] {
		return n - 1
	}
	idx := int(math.Log(float64(d)/float64(histogramBounds[0])) / math.Log(histogramRatio))
	if idx < 0 {
		idx = 0
	}
	if idx >= n {
		idx = n - 1
	}
	for idx > 0 && d < histogramBounds[idx] {
		idx--
	}
	for idx < n-1 && d >= histogramBounds[idx+1] {
		idx++
	}
	return idx
}

// histogramLogLerp estimates a value a fraction of the way through a bucket on
// the logarithmic axis, matching how the edges are spaced and how latency data
// tends to spread.
func histogramLogLerp(lower, upper time.Duration, frac float64) time.Duration {
	if lower <= 0 || upper <= lower {
		return lower
	}
	return time.Duration(math.Round(float64(lower) * math.Pow(float64(upper)/float64(lower), frac)))
}

// Histogram is a thread-safe fixed-bucket latency histogram. Observe is O(1)
// and memory is bounded by the bucket count regardless of how many samples are
// recorded. The zero value is usable.
type Histogram struct {
	mu     sync.Mutex
	counts []int64
	count  int64
	sum    time.Duration
	min    time.Duration
	max    time.Duration
}

// NewHistogram returns an empty histogram.
func NewHistogram() *Histogram { return &Histogram{} }

// Observe records one duration, clamped at zero.
func (h *Histogram) Observe(d time.Duration) {
	if d < 0 {
		d = 0
	}
	idx := histogramBucketIndex(d)
	h.mu.Lock()
	if h.counts == nil {
		h.counts = make([]int64, len(histogramBounds)-1)
	}
	h.counts[idx]++
	if h.count == 0 {
		h.min = d
		h.max = d
	} else {
		if d < h.min {
			h.min = d
		}
		if d > h.max {
			h.max = d
		}
	}
	h.count++
	h.sum += d
	h.mu.Unlock()
}

// Percentile returns the p-th percentile (p in [0,1]) with the value
// interpolated on the log axis inside the bucket that contains the rank.
func (h *Histogram) Percentile(p float64) time.Duration {
	if p < 0 {
		p = 0
	}
	if p > 1 {
		p = 1
	}
	h.mu.Lock()
	defer h.mu.Unlock()
	if h.count == 0 {
		return 0
	}
	if p <= 0 {
		return h.min
	}
	if p >= 1 {
		return h.max
	}
	n := len(h.counts)
	if n == 0 {
		return h.max
	}
	target := p * float64(h.count)
	cum := 0.0
	for i := 0; i < n; i++ {
		c := float64(h.counts[i])
		if c == 0 {
			continue
		}
		if cum+c >= target {
			within := (target - cum) / c
			if within < 0 {
				within = 0
			}
			if within > 1 {
				within = 1
			}
			est := histogramLogLerp(histogramBounds[i], histogramBounds[i+1], within)
			if est < h.min {
				est = h.min
			}
			if est > h.max {
				est = h.max
			}
			return est
		}
		cum += c
	}
	return h.max
}

// Count returns the number of observations.
func (h *Histogram) Count() int64 {
	h.mu.Lock()
	defer h.mu.Unlock()
	return h.count
}

// Sum returns the total of all observed durations.
func (h *Histogram) Sum() time.Duration {
	h.mu.Lock()
	defer h.mu.Unlock()
	return h.sum
}

// Min returns the smallest observation, or zero when empty.
func (h *Histogram) Min() time.Duration {
	h.mu.Lock()
	defer h.mu.Unlock()
	return h.min
}

// Max returns the largest observation, or zero when empty.
func (h *Histogram) Max() time.Duration {
	h.mu.Lock()
	defer h.mu.Unlock()
	return h.max
}

// Mean returns the arithmetic mean observation, or zero when empty.
func (h *Histogram) Mean() time.Duration {
	h.mu.Lock()
	defer h.mu.Unlock()
	if h.count == 0 {
		return 0
	}
	return h.sum / time.Duration(h.count)
}

// Reset discards all observations and statistics.
func (h *Histogram) Reset() {
	h.mu.Lock()
	defer h.mu.Unlock()
	for i := range h.counts {
		h.counts[i] = 0
	}
	h.count = 0
	h.sum = 0
	h.min = 0
	h.max = 0
}

// LatencyStats is a point-in-time summary for one subsystem.
type LatencyStats struct {
	Count          int64
	P50, P95, P99  time.Duration
	Min, Max, Mean time.Duration
	Ops            int64
	Errors         int64
	// Sum is the total observed latency. It is included in the snapshot so
	// consumers (e.g. Prometheus exposition) can read a consistent set of
	// quantiles, counters and the sum from one captured instant.
	Sum time.Duration
}

// subsystemMetrics bundles the latency distribution and the throughput/error
// counters for a single subsystem.
type subsystemMetrics struct {
	latency *Histogram
	ops     atomic.Int64
	errors  atomic.Int64
}

func newSubsystemMetrics() *subsystemMetrics {
	return &subsystemMetrics{latency: NewHistogram()}
}

func (s *subsystemMetrics) stats() LatencyStats {
	return LatencyStats{
		Count:  s.latency.Count(),
		P50:    s.latency.Percentile(0.50),
		P95:    s.latency.Percentile(0.95),
		P99:    s.latency.Percentile(0.99),
		Min:    s.latency.Min(),
		Max:    s.latency.Max(),
		Mean:   s.latency.Mean(),
		Ops:    s.ops.Load(),
		Errors: s.errors.Load(),
		Sum:    s.latency.Sum(),
	}
}

// Metrics collects per-subsystem latency histograms and operation/error
// counters. It is safe for concurrent use.
type Metrics struct {
	mu         sync.RWMutex
	subsystems map[string]*subsystemMetrics
}

// DefaultMetrics is the process-wide collector, mirroring
// core.GlobalScanProgress so callers need no plumbing.
var DefaultMetrics = NewMetrics()

// NewMetrics returns a collector with the known subsystems pre-registered, so
// Snapshot always exposes them even before any traffic.
func NewMetrics() *Metrics {
	m := &Metrics{subsystems: make(map[string]*subsystemMetrics)}
	for _, name := range []string{SubsystemValidation, SubsystemScan, SubsystemWebhook, SubsystemSearch} {
		m.subsystems[name] = newSubsystemMetrics()
	}
	return m
}

func (m *Metrics) getOrCreate(name string) *subsystemMetrics {
	m.mu.RLock()
	s := m.subsystems[name]
	m.mu.RUnlock()
	if s != nil {
		return s
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	if s = m.subsystems[name]; s == nil {
		s = newSubsystemMetrics()
		m.subsystems[name] = s
	}
	return s
}

func (m *Metrics) lookup(name string) *subsystemMetrics {
	m.mu.RLock()
	defer m.mu.RUnlock()
	return m.subsystems[name]
}

// ObserveLatency records one latency sample for a subsystem.
func (m *Metrics) ObserveLatency(subsystem string, d time.Duration) {
	m.getOrCreate(subsystem).latency.Observe(d)
}

// IncError records one error for a subsystem.
func (m *Metrics) IncError(subsystem string) {
	m.getOrCreate(subsystem).errors.Add(1)
}

// IncOperations records one completed operation for a subsystem.
func (m *Metrics) IncOperations(subsystem string) {
	m.getOrCreate(subsystem).ops.Add(1)
}

// LatencyStats returns the current summary for a subsystem, or the zero value
// when it has never been used.
func (m *Metrics) LatencyStats(subsystem string) LatencyStats {
	s := m.lookup(subsystem)
	if s == nil {
		return LatencyStats{}
	}
	return s.stats()
}

// Snapshot returns a summary per registered subsystem.
func (m *Metrics) Snapshot() map[string]LatencyStats {
	m.mu.RLock()
	names := make([]string, 0, len(m.subsystems))
	for name := range m.subsystems {
		names = append(names, name)
	}
	m.mu.RUnlock()

	out := make(map[string]LatencyStats, len(names))
	for _, name := range names {
		if s := m.lookup(name); s != nil {
			out[name] = s.stats()
		}
	}
	return out
}

// Reset clears every histogram and counter while keeping the subsystem set.
func (m *Metrics) Reset() {
	m.mu.RLock()
	subs := make([]*subsystemMetrics, 0, len(m.subsystems))
	for _, s := range m.subsystems {
		subs = append(subs, s)
	}
	m.mu.RUnlock()

	for _, s := range subs {
		s.latency.Reset()
		s.ops.Store(0)
		s.errors.Store(0)
	}
}
