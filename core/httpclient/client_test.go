package httpclient

import (
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strconv"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

// fakeClock is an injectable time source. sleep records the delay and advances
// the clock, so no test ever blocks on real time.
type fakeClock struct {
	mu     sync.Mutex
	t      time.Time
	sleeps []time.Duration
}

func newFakeClock() *fakeClock {
	return &fakeClock{t: time.Unix(1_700_000_000, 0)}
}

func (f *fakeClock) now() time.Time {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.t
}

func (f *fakeClock) sleep(d time.Duration) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.sleeps = append(f.sleeps, d)
	f.t = f.t.Add(d)
}

func (f *fakeClock) advance(d time.Duration) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.t = f.t.Add(d)
}

func (f *fakeClock) sleepsCopy() []time.Duration {
	f.mu.Lock()
	defer f.mu.Unlock()
	out := make([]time.Duration, len(f.sleeps))
	copy(out, f.sleeps)
	return out
}

func noJitter() float64 { return 0 }

func hostOf(raw string) string {
	u, err := url.Parse(raw)
	if err != nil {
		panic(err)
	}
	return u.Host
}

func newTestClient(t *testing.T, clock *fakeClock, cfg Config) *Client {
	t.Helper()
	return newClient(cfg, clock.now, clock.sleep, noJitter)
}

func TestRetryAfterHonoredOn429(t *testing.T) {
	var count int64
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if atomic.AddInt64(&count, 1) == 1 {
			w.Header().Set("Retry-After", "2")
			w.WriteHeader(http.StatusTooManyRequests)
			return
		}
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	clock := newFakeClock()
	c := newTestClient(t, clock, Config{
		Retries:        2,
		BackoffInitial: time.Millisecond,
		BackoffMax:     time.Second,
		Timeout:        time.Second,
	})

	req, _ := http.NewRequest(http.MethodGet, srv.URL, nil)
	resp, err := c.Do(req)
	if err != nil {
		t.Fatalf("Do: %v", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want 200", resp.StatusCode)
	}
	if got := atomic.LoadInt64(&count); got != 2 {
		t.Fatalf("server requests = %d, want exactly 2", got)
	}
	sleeps := clock.sleepsCopy()
	if len(sleeps) != 1 {
		t.Fatalf("sleeps = %v, want exactly one", sleeps)
	}
	if sleeps[0] != 2*time.Second {
		t.Fatalf("sleep = %s, want the advertised 2s", sleeps[0])
	}
}

func TestXRateLimitResetAfterFallback(t *testing.T) {
	var count int64
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if atomic.AddInt64(&count, 1) == 1 {
			w.Header().Set("X-RateLimit-Reset-After", "1.5")
			w.WriteHeader(http.StatusTooManyRequests)
			return
		}
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	clock := newFakeClock()
	c := newTestClient(t, clock, Config{Retries: 1, BackoffInitial: time.Millisecond, Timeout: time.Second})

	req, _ := http.NewRequest(http.MethodGet, srv.URL, nil)
	resp, err := c.Do(req)
	if err != nil {
		t.Fatalf("Do: %v", err)
	}
	defer resp.Body.Close()

	sleeps := clock.sleepsCopy()
	if len(sleeps) != 1 || sleeps[0] != 1500*time.Millisecond {
		t.Fatalf("sleeps = %v, want [1.5s]", sleeps)
	}
}

func TestPermanentStatusesAreNotRetried(t *testing.T) {
	for _, code := range []int{400, 401, 403, 404} {
		t.Run(strconv.Itoa(code), func(t *testing.T) {
			var count int64
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				atomic.AddInt64(&count, 1)
				w.WriteHeader(code)
			}))
			defer srv.Close()

			clock := newFakeClock()
			c := newTestClient(t, clock, Config{Retries: 3, BackoffInitial: time.Millisecond, Timeout: time.Second})

			req, _ := http.NewRequest(http.MethodGet, srv.URL, nil)
			resp, err := c.Do(req)
			if err != nil {
				t.Fatalf("Do: %v", err)
			}
			resp.Body.Close()

			if resp.StatusCode != code {
				t.Fatalf("status = %d, want %d", resp.StatusCode, code)
			}
			if got := atomic.LoadInt64(&count); got != 1 {
				t.Fatalf("server requests = %d, want exactly 1", got)
			}
			if sleeps := clock.sleepsCopy(); len(sleeps) != 0 {
				t.Fatalf("unexpected retry sleeps: %v", sleeps)
			}
		})
	}
}

func TestRetryableStatusesAreRetried(t *testing.T) {
	for _, code := range []int{408, 429, 500, 502, 503, 504} {
		t.Run(strconv.Itoa(code), func(t *testing.T) {
			var count int64
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if atomic.AddInt64(&count, 1) == 1 {
					w.WriteHeader(code)
					return
				}
				w.WriteHeader(http.StatusOK)
			}))
			defer srv.Close()

			clock := newFakeClock()
			c := newTestClient(t, clock, Config{
				Retries:        1,
				BackoffInitial: 50 * time.Millisecond,
				BackoffMax:     time.Second,
				Timeout:        time.Second,
			})

			req, _ := http.NewRequest(http.MethodGet, srv.URL, nil)
			resp, err := c.Do(req)
			if err != nil {
				t.Fatalf("Do: %v", err)
			}
			resp.Body.Close()

			if resp.StatusCode != http.StatusOK {
				t.Fatalf("status = %d, want 200", resp.StatusCode)
			}
			if got := atomic.LoadInt64(&count); got != 2 {
				t.Fatalf("server requests = %d, want exactly 2", got)
			}
		})
	}
}

func TestParseRetryAfter(t *testing.T) {
	now := time.Date(2026, 1, 2, 3, 4, 5, 0, time.UTC)
	tests := []struct {
		name  string
		value string
		want  time.Duration
		ok    bool
	}{
		{"seconds", "5", 5 * time.Second, true},
		{"fractional", "1.5", 1500 * time.Millisecond, true},
		{"http-date", now.Add(90 * time.Second).Format(http.TimeFormat), 90 * time.Second, true},
		{"past-date", now.Add(-time.Hour).Format(http.TimeFormat), 0, false},
		{"zero", "0", 0, false},
		{"negative", "-3", 0, false},
		{"empty", "", 0, false},
		{"garbage", "soon", 0, false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, ok := ParseRetryAfter(tt.value, now)
			if ok != tt.ok || got != tt.want {
				t.Fatalf("ParseRetryAfter(%q) = (%s, %v), want (%s, %v)", tt.value, got, ok, tt.want, tt.ok)
			}
		})
	}
}

func TestParseRetryAfterClampsLargeValue(t *testing.T) {
	now := time.Unix(1_700_000_000, 0)
	got, ok := ParseRetryAfter("86400", now)
	if !ok {
		t.Fatalf("ParseRetryAfter(86400) ok = false, want true")
	}
	if got != 5*time.Minute {
		t.Fatalf("ParseRetryAfter(86400) = %s, want the 5m cap", got)
	}
}

func TestClientRefusesCrossHostRedirect(t *testing.T) {
	var internalHits, originHits int64
	internal := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt64(&internalHits, 1)
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("internal-body"))
	}))
	defer internal.Close()

	origin := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt64(&originHits, 1)
		http.Redirect(w, r, internal.URL+"/secret", http.StatusFound)
	}))
	defer origin.Close()

	clock := newFakeClock()
	c := newTestClient(t, clock, Config{
		Retries:        3,
		BackoffInitial: time.Millisecond,
		Timeout:        time.Second,
		Breaker:        BreakerConfig{FailureThreshold: 1, Cooldown: time.Hour, HalfOpenProbes: 1},
	})

	req, _ := http.NewRequest(http.MethodGet, origin.URL+"/start", nil)
	resp, err := c.Do(req)
	if err == nil {
		if resp != nil {
			resp.Body.Close()
		}
		t.Fatal("Do followed a cross-host redirect, want an error")
	}
	if got := atomic.LoadInt64(&internalHits); got != 0 {
		t.Fatalf("redirect target received %d requests, want 0", got)
	}
	// The refusal is deterministic: it must not be retried, and it must not
	// charge the host breaker. Retrying it burned the backoff budget and a
	// burst of refusals knocked a healthy host into cooldown.
	if got := atomic.LoadInt64(&originHits); got != 1 {
		t.Fatalf("origin received %d requests, want 1 (refusal must not be retried)", got)
	}
	if state := c.breaker.breakerState(hostOf(origin.URL)); state != stateClosed {
		t.Fatalf("breaker state = %s, want closed (refusal must not trip the breaker)", state)
	}
}

func TestClientFollowsSameOriginRedirect(t *testing.T) {
	origin := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/final" {
			_, _ = w.Write([]byte("same-origin-body"))
			return
		}
		http.Redirect(w, r, "/final", http.StatusFound)
	}))
	defer origin.Close()

	clock := newFakeClock()
	c := newTestClient(t, clock, Config{Timeout: time.Second})

	req, _ := http.NewRequest(http.MethodGet, origin.URL+"/start", nil)
	resp, err := c.Do(req)
	if err != nil {
		t.Fatalf("Do: %v", err)
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatalf("read body: %v", err)
	}
	if string(body) != "same-origin-body" {
		t.Fatalf("body = %q, want %q", string(body), "same-origin-body")
	}
}

func TestBackoffAndJitter(t *testing.T) {
	clock := newFakeClock()

	noJitterClient := newClient(Config{BackoffInitial: time.Second, BackoffMax: 3 * time.Second}, clock.now, clock.sleep, noJitter)
	if got := noJitterClient.backoff(0); got != time.Second {
		t.Fatalf("backoff(0) = %s, want 1s", got)
	}
	if got := noJitterClient.backoff(1); got != 2*time.Second {
		t.Fatalf("backoff(1) = %s, want 2s", got)
	}
	if got := noJitterClient.backoff(9); got != 3*time.Second {
		t.Fatalf("backoff(9) = %s, want the 3s cap", got)
	}

	low := newClient(Config{BackoffInitial: time.Second, BackoffMax: time.Minute, Jitter: true}, clock.now, clock.sleep, func() float64 { return 0 })
	if got := low.backoff(0); got != 500*time.Millisecond {
		t.Fatalf("jittered backoff(low) = %s, want 500ms", got)
	}
	high := newClient(Config{BackoffInitial: time.Second, BackoffMax: time.Minute, Jitter: true}, clock.now, clock.sleep, func() float64 { return 0.999 })
	if got := high.backoff(0); got < 1499*time.Millisecond || got > 1500*time.Millisecond {
		t.Fatalf("jittered backoff(high) = %s, want within [1.499s, 1.5s]", got)
	}
}

func TestPerCallPacingOverride(t *testing.T) {
	var count int64
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt64(&count, 1)
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	do := func(c *Client) {
		t.Helper()
		req, _ := http.NewRequest(http.MethodGet, srv.URL, nil)
		resp, err := c.Do(req)
		if err != nil {
			t.Fatalf("Do: %v", err)
		}
		resp.Body.Close()
	}

	paced := newFakeClock()
	pacedClient := newTestClient(t, paced, Config{Timeout: time.Second, Pacing: time.Second})
	do(pacedClient)
	do(pacedClient)
	if sleeps := paced.sleepsCopy(); len(sleeps) != 1 || sleeps[0] != time.Second {
		t.Fatalf("paced sleeps = %v, want [1s]", sleeps)
	}

	unpaced := newFakeClock()
	unpacedClient := newTestClient(t, unpaced, Config{Timeout: time.Second, Pacing: time.Second})
	pacingOff := false
	for i := 0; i < 2; i++ {
		req, _ := http.NewRequest(http.MethodGet, srv.URL, nil)
		resp, err := unpacedClient.DoWithOptions(req, Options{Pacing: &pacingOff})
		if err != nil {
			t.Fatalf("DoWithOptions: %v", err)
		}
		resp.Body.Close()
	}
	if sleeps := unpaced.sleepsCopy(); len(sleeps) != 0 {
		t.Fatalf("unpaced sleeps = %v, want none", sleeps)
	}
}

func TestBreakerTripsOnNetworkClassFailuresAndExemptsProtectedHost(t *testing.T) {
	var failing, protected int64
	failSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt64(&failing, 1)
		w.WriteHeader(http.StatusServiceUnavailable)
	}))
	defer failSrv.Close()
	protSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt64(&protected, 1)
		w.WriteHeader(http.StatusOK)
	}))
	defer protSrv.Close()

	clock := newFakeClock()
	c := newTestClient(t, clock, Config{
		Retries:        0,
		Timeout:        time.Second,
		Breaker:        BreakerConfig{FailureThreshold: 3, Cooldown: time.Hour, HalfOpenProbes: 1},
		ProtectedHosts: []string{hostOf(protSrv.URL)},
	})

	get := func(raw string) (*http.Response, error) {
		req, _ := http.NewRequest(http.MethodGet, raw, nil)
		return c.Do(req)
	}

	for i := 0; i < 3; i++ {
		resp, err := get(failSrv.URL)
		if err != nil {
			t.Fatalf("attempt %d: %v", i, err)
		}
		resp.Body.Close()
	}
	if got := atomic.LoadInt64(&failing); got != 3 {
		t.Fatalf("failing host requests = %d, want exactly 3", got)
	}
	if state := c.breaker.breakerState(hostOf(failSrv.URL)); state != stateOpen {
		t.Fatalf("breaker state = %s, want open", state)
	}

	// While open, the host must not be contacted at all.
	_, err := get(failSrv.URL)
	var openErr *BreakerOpenError
	if !errors.As(err, &openErr) {
		t.Fatalf("err = %v, want *BreakerOpenError", err)
	}
	if got := atomic.LoadInt64(&failing); got != 3 {
		t.Fatalf("failing host requests while open = %d, want still 3", got)
	}

	// A protected host still receives requests while another host is open.
	resp, err := get(protSrv.URL)
	if err != nil {
		t.Fatalf("protected host: %v", err)
	}
	resp.Body.Close()
	if got := atomic.LoadInt64(&protected); got != 1 {
		t.Fatalf("protected host requests = %d, want exactly 1", got)
	}

	// Past the cooldown exactly one half-open probe is admitted, then the
	// failing host reopens.
	clock.advance(time.Hour + time.Minute)
	resp, err = get(failSrv.URL)
	if err != nil {
		t.Fatalf("half-open probe: %v", err)
	}
	resp.Body.Close()
	if got := atomic.LoadInt64(&failing); got != 4 {
		t.Fatalf("failing host requests after cooldown = %d, want exactly 4", got)
	}
	if state := c.breaker.breakerState(hostOf(failSrv.URL)); state != stateOpen {
		t.Fatalf("breaker state after failed probe = %s, want open", state)
	}
}

func TestBreakerIgnoresNonNetworkFailures(t *testing.T) {
	var count int64
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt64(&count, 1)
		w.WriteHeader(http.StatusInternalServerError)
	}))
	defer srv.Close()

	clock := newFakeClock()
	c := newTestClient(t, clock, Config{
		Retries: 0,
		Timeout: time.Second,
		Breaker: BreakerConfig{FailureThreshold: 1, Cooldown: time.Hour, HalfOpenProbes: 1},
	})

	for i := 0; i < 3; i++ {
		req, _ := http.NewRequest(http.MethodGet, srv.URL, nil)
		resp, err := c.Do(req)
		if err != nil {
			t.Fatalf("attempt %d: %v", i, err)
		}
		resp.Body.Close()
	}
	if got := atomic.LoadInt64(&count); got != 3 {
		t.Fatalf("server requests = %d, want exactly 3 (500 must not trip the breaker)", got)
	}
	if state := c.breaker.breakerState(hostOf(srv.URL)); state != stateClosed {
		t.Fatalf("breaker state = %s, want closed", state)
	}
}
