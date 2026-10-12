package core

import (
	"math"
	"sort"
	"sync"
	"testing"
	"time"
)

func absDuration(d time.Duration) time.Duration {
	if d < 0 {
		return -d
	}
	return d
}

func TestMetricsHistogramPercentileAccuracy(t *testing.T) {
	h := NewHistogram()

	const n = 20000
	min := float64(histogramMinLatency)
	max := float64(histogramMaxLatency)
	ratio := math.Pow(max/min, 1.0/(n-1))

	vals := make([]time.Duration, n)
	v := min
	for i := 0; i < n; i++ {
		vals[i] = time.Duration(math.Round(v))
		v *= ratio
	}
	for _, d := range vals {
		h.Observe(d)
	}
	sort.Slice(vals, func(i, j int) bool { return vals[i] < vals[j] })

	reference := func(p float64) time.Duration {
		if p <= 0 {
			return vals[0]
		}
		if p >= 1 {
			return vals[len(vals)-1]
		}
		idx := p * float64(len(vals)-1)
		lo := int(math.Floor(idx))
		if lo+1 >= len(vals) {
			return vals[lo]
		}
		frac := idx - float64(lo)
		return time.Duration(float64(vals[lo]) + (float64(vals[lo+1])-float64(vals[lo]))*frac)
	}

	for _, p := range []float64{0.50, 0.90, 0.95, 0.99} {
		got := h.Percentile(p)
		want := reference(p)
		tol := want / 8
		if tol < 2*time.Millisecond {
			tol = 2 * time.Millisecond
		}
		if diff := absDuration(got - want); diff > tol {
			t.Errorf("Percentile(%.2f) = %v, want %v (tolerance %v)", p, got, want, tol)
		}
	}

	var wantSum time.Duration
	for _, d := range vals {
		wantSum += d
	}
	if h.Count() != n {
		t.Errorf("Count() = %d, want %d", h.Count(), n)
	}
	if h.Sum() != wantSum {
		t.Errorf("Sum() = %v, want %v", h.Sum(), wantSum)
	}
	if got, want := h.Mean(), wantSum/time.Duration(n); got != want {
		t.Errorf("Mean() = %v, want %v", got, want)
	}
	if h.Min() != vals[0] {
		t.Errorf("Min() = %v, want %v", h.Min(), vals[0])
	}
	if h.Max() != vals[n-1] {
		t.Errorf("Max() = %v, want %v", h.Max(), vals[n-1])
	}
}

func TestMetricsHistogramBucketBoundaries(t *testing.T) {
	n := len(histogramBounds) - 1
	if n < 2 {
		t.Fatalf("need at least 2 buckets, got %d", n)
	}
	if histogramBounds[0] != histogramMinLatency {
		t.Errorf("first bound = %v, want %v", histogramBounds[0], histogramMinLatency)
	}
	if histogramBounds[n] != histogramMaxLatency {
		t.Errorf("last bound = %v, want %v", histogramBounds[n], histogramMaxLatency)
	}
	for i := 0; i < n; i++ {
		if histogramBounds[i] >= histogramBounds[i+1] {
			t.Fatalf("bounds not increasing at %d: %v >= %v", i, histogramBounds[i], histogramBounds[i+1])
		}
	}

	for i := 1; i < n; i++ {
		if got := histogramBucketIndex(histogramBounds[i]); got != i {
			t.Errorf("bucketIndex(%v) = %d, want %d", histogramBounds[i], got, i)
		}
		if got := histogramBucketIndex(histogramBounds[i] - 1); got != i-1 {
			t.Errorf("bucketIndex(%v-1) = %d, want %d", histogramBounds[i], got, i-1)
		}
	}

	if got := histogramBucketIndex(0); got != 0 {
		t.Errorf("bucketIndex(0) = %d, want 0", got)
	}
	if got := histogramBucketIndex(-5 * time.Millisecond); got != 0 {
		t.Errorf("bucketIndex(-5ms) = %d, want 0", got)
	}
	if got := histogramBucketIndex(histogramMinLatency); got != 0 {
		t.Errorf("bucketIndex(min) = %d, want 0", got)
	}
	if got := histogramBucketIndex(histogramMaxLatency); got != n-1 {
		t.Errorf("bucketIndex(max) = %d, want %d", got, n-1)
	}
	if got := histogramBucketIndex(24 * time.Hour); got != n-1 {
		t.Errorf("bucketIndex(24h) = %d, want %d (clamped)", got, n-1)
	}

	prev := -1
	for d := time.Duration(0); d <= histogramMaxLatency; d += 250 * time.Microsecond {
		idx := histogramBucketIndex(d)
		if idx < prev {
			t.Fatalf("bucketIndex decreased at %v: %d < %d", d, idx, prev)
		}
		prev = idx
	}

	h := NewHistogram()
	sample := 123 * time.Millisecond
	h.Observe(sample)
	if idx := histogramBucketIndex(sample); h.counts[idx] != 1 {
		t.Errorf("counts[%d] = %d, want 1", idx, h.counts[idx])
	}
	if h.Count() != 1 || h.Sum() != sample || h.Min() != sample || h.Max() != sample {
		t.Errorf("single observation stats = count %d sum %v min %v max %v, want 1 %v %v %v",
			h.Count(), h.Sum(), h.Min(), h.Max(), sample, sample, sample)
	}
	if got := h.Percentile(0.5); got != sample {
		t.Errorf("Percentile(0.5) = %v, want %v", got, sample)
	}
}

func TestMetricsHistogramEmptyAndReset(t *testing.T) {
	h := NewHistogram()
	if h.Count() != 0 || h.Sum() != 0 || h.Min() != 0 || h.Max() != 0 || h.Mean() != 0 {
		t.Fatalf("empty histogram not zero: count %d sum %v", h.Count(), h.Sum())
	}
	if got := h.Percentile(0.95); got != 0 {
		t.Fatalf("Percentile on empty = %v, want 0", got)
	}

	h.Observe(5 * time.Millisecond)
	h.Observe(20 * time.Millisecond)
	h.Reset()
	if h.Count() != 0 || h.Sum() != 0 || h.Min() != 0 || h.Max() != 0 || h.Mean() != 0 {
		t.Fatalf("reset histogram not zero: count %d sum %v min %v max %v", h.Count(), h.Sum(), h.Min(), h.Max())
	}
	if got := h.Percentile(0.5); got != 0 {
		t.Fatalf("Percentile after Reset = %v, want 0", got)
	}
}

func TestMetricsConcurrentObserveAndErrors(t *testing.T) {
	m := NewMetrics()

	const goroutines = 32
	const perGoroutine = 500
	want := int64(goroutines * perGoroutine)

	stop := make(chan struct{})
	snapshotterDone := make(chan struct{})
	go func() {
		defer close(snapshotterDone)
		for {
			select {
			case <-stop:
				return
			default:
				_ = m.Snapshot()
			}
		}
	}()

	var wg sync.WaitGroup
	for g := 0; g < goroutines; g++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for i := 0; i < perGoroutine; i++ {
				m.ObserveLatency(SubsystemScan, time.Duration(i)*time.Millisecond)
				m.IncOperations(SubsystemScan)
				m.IncError(SubsystemScan)
			}
		}()
	}
	for g := 0; g < goroutines; g++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for i := 0; i < perGoroutine; i++ {
				m.ObserveLatency(SubsystemWebhook, time.Duration(i)*time.Microsecond)
				m.IncOperations(SubsystemWebhook)
			}
		}()
	}
	wg.Wait()
	close(stop)
	<-snapshotterDone

	st := m.LatencyStats(SubsystemScan)
	if st.Count != want {
		t.Errorf("scan Count = %d, want %d", st.Count, want)
	}
	if st.Ops != want {
		t.Errorf("scan Ops = %d, want %d", st.Ops, want)
	}
	if st.Errors != want {
		t.Errorf("scan Errors = %d, want %d", st.Errors, want)
	}

	wh := m.LatencyStats(SubsystemWebhook)
	if wh.Count != want || wh.Ops != want || wh.Errors != 0 {
		t.Errorf("webhook stats = count %d ops %d errors %d, want %d %d 0",
			wh.Count, wh.Ops, wh.Errors, want, want)
	}
}

func TestMetricsResetClears(t *testing.T) {
	m := NewMetrics()
	m.ObserveLatency(SubsystemScan, 5*time.Millisecond)
	m.ObserveLatency(SubsystemScan, 40*time.Millisecond)
	m.IncOperations(SubsystemScan)
	m.IncError(SubsystemScan)
	m.ObserveLatency(SubsystemSearch, 12*time.Millisecond)

	m.Reset()

	for _, name := range []string{SubsystemScan, SubsystemSearch, SubsystemValidation, SubsystemWebhook} {
		if st := m.LatencyStats(name); st != (LatencyStats{}) {
			t.Errorf("stats for %s after Reset = %+v, want zero", name, st)
		}
	}
}

func TestMetricsSnapshotShape(t *testing.T) {
	if DefaultMetrics == nil {
		t.Fatal("DefaultMetrics is nil")
	}

	m := NewMetrics()
	snap := m.Snapshot()
	wantNames := []string{SubsystemValidation, SubsystemScan, SubsystemWebhook, SubsystemSearch}
	if len(snap) != len(wantNames) {
		t.Fatalf("Snapshot has %d subsystems, want %d", len(snap), len(wantNames))
	}
	for _, name := range wantNames {
		st, ok := snap[name]
		if !ok {
			t.Fatalf("Snapshot missing subsystem %q", name)
		}
		if st != (LatencyStats{}) {
			t.Errorf("fresh stats for %s = %+v, want zero", name, st)
		}
	}

	m.ObserveLatency(SubsystemWebhook, 20*time.Millisecond)
	m.IncOperations(SubsystemWebhook)
	m.IncError(SubsystemWebhook)
	m.ObserveLatency("custom", 3*time.Millisecond)

	snap = m.Snapshot()
	wh := snap[SubsystemWebhook]
	if wh.Count != 1 || wh.Ops != 1 || wh.Errors != 1 {
		t.Errorf("webhook stats = count %d ops %d errors %d, want 1 1 1", wh.Count, wh.Ops, wh.Errors)
	}
	if wh.Min != 20*time.Millisecond || wh.Max != 20*time.Millisecond || wh.P50 != 20*time.Millisecond {
		t.Errorf("webhook latency = min %v max %v p50 %v, want 20ms each", wh.Min, wh.Max, wh.P50)
	}
	if _, ok := snap["custom"]; !ok {
		t.Error("Snapshot missing lazily-registered subsystem \"custom\"")
	}
}
