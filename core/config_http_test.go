package core

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/trebor048/shhgot/core/httpclient"
)

// installSharedHTTPClient replaces the process-wide client for one test and
// restores the compiled-in default (and default validator pacing) afterwards.
func installSharedHTTPClient(t *testing.T, cfg httpclient.Config) {
	t.Helper()
	sharedHTTPClientMu.Lock()
	sharedHTTPClient = httpclient.New(cfg)
	sharedHTTPClientMu.Unlock()
	t.Cleanup(func() {
		sharedHTTPClientMu.Lock()
		sharedHTTPClient = httpclient.New((&Config{}).HTTPClientConfig())
		sharedHTTPClientMu.Unlock()
		validatorPacingNanos.Store(int64((&Config{}).ValidatorMinInterval()))
	})
}

// TestHTTPClientConfigDefaultsKeepsHTTPClientDefaults pins the mapping contract:
// an empty config must leave every httpclient.DefaultConfig value in place, and
// the shared client must not pace unless http.pacing_ms opts in.
func TestHTTPClientConfigDefaultsKeepsHTTPClientDefaults(t *testing.T) {
	def := httpclient.DefaultConfig()
	got := (&Config{}).HTTPClientConfig()

	if got.Retries != def.Retries ||
		got.BackoffInitial != def.BackoffInitial ||
		got.BackoffMax != def.BackoffMax ||
		got.Timeout != def.Timeout ||
		got.Jitter != def.Jitter ||
		got.Breaker.FailureThreshold != def.Breaker.FailureThreshold ||
		got.Breaker.Cooldown != def.Breaker.Cooldown ||
		got.Breaker.HalfOpenProbes != def.Breaker.HalfOpenProbes {
		t.Fatalf("empty config drifted from httpclient defaults:\n got %+v\nwant %+v", got, def)
	}
	if got.Pacing != 0 {
		t.Fatalf("pacing = %s, want off when http.pacing_ms is unset", got.Pacing)
	}
}

// TestSharedClientDoesNotRetry401 proves the shared client applies the default
// retry policy but never retries a permanent client error: a 401 validator
// response must reach the server exactly once.
func TestSharedClientDoesNotRetry401(t *testing.T) {
	installSharedHTTPClient(t, (&Config{}).HTTPClientConfig())

	var count int64
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt64(&count, 1)
		w.WriteHeader(http.StatusUnauthorized)
	}))
	defer srv.Close()

	req, _ := http.NewRequest(http.MethodGet, srv.URL, nil)
	resp, err := SharedHTTPClient().Do(req)
	if err != nil {
		t.Fatalf("Do: %v", err)
	}
	resp.Body.Close()

	if got := atomic.LoadInt64(&count); got != 1 {
		t.Fatalf("server observed %d requests, want exactly 1 (401 must not be retried)", got)
	}
}

// TestConfigureSharedHTTPClientAppliesRetries proves the process-wide client is
// rebuilt from the loaded config: with http.retries=1 a persistently failing
// server must see exactly two attempts, not the default four.
func TestConfigureSharedHTTPClientAppliesRetries(t *testing.T) {
	installSharedHTTPClient(t, (&Config{}).HTTPClientConfig())

	retries := 1
	ConfigureSharedHTTPClient(&Config{HTTP: HTTPConfig{Retries: &retries}})

	var count int64
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt64(&count, 1)
		w.WriteHeader(http.StatusInternalServerError)
	}))
	defer srv.Close()

	req, _ := http.NewRequest(http.MethodGet, srv.URL, nil)
	resp, err := SharedHTTPClient().Do(req)
	if err != nil {
		t.Fatalf("Do: %v", err)
	}
	resp.Body.Close()

	if got := atomic.LoadInt64(&count); got != 2 {
		t.Fatalf("server observed %d attempts, want exactly 2 (http.retries=1)", got)
	}
}

// TestValidatorPacingDoesNotLeakToSharedClient proves the fix for the global
// validator pacing: validators.min_interval_ms is applied as a per-call override
// on the credential-validation path only. Two requests through the shared client
// (code search, GitLab, crypto, AI) are not paced at the validator interval,
// while two validation requests through the validator's Doer are.
func TestValidatorPacingDoesNotLeakToSharedClient(t *testing.T) {
	installSharedHTTPClient(t, (&Config{}).HTTPClientConfig())

	intervalMS := 300
	ConfigureSharedHTTPClient(&Config{Validators: ValidatorsConfig{MinIntervalMS: &intervalMS}})

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	do := func(doer interface {
		Do(*http.Request) (*http.Response, error)
	}) time.Duration {
		t.Helper()
		start := time.Now()
		for i := 0; i < 2; i++ {
			req, _ := http.NewRequest(http.MethodGet, srv.URL, nil)
			resp, err := doer.Do(req)
			if err != nil {
				t.Fatalf("Do %d: %v", i, err)
			}
			resp.Body.Close()
		}
		return time.Since(start)
	}

	// Non-validation traffic must not inherit validators.min_interval_ms.
	if elapsed := do(SharedHTTPClient()); elapsed >= time.Duration(intervalMS/2)*time.Millisecond {
		t.Fatalf("two shared-client requests took %s; non-validation traffic must not be validator-paced", elapsed)
	}

	// Validation traffic applies the interval on the second request to the host.
	tv := NewTokenValidator(nil)
	if elapsed := do(tv.httpClient); elapsed < time.Duration(intervalMS/2)*time.Millisecond {
		t.Fatalf("two validation requests took %s, want at least the %dms validator pacing", elapsed, intervalMS)
	}
}

// The standalone Discord/Telegram validators in validators.go used
// SharedHTTPClient directly, so validators.min_interval_ms never paced them even
// though TokenValidator used the paced doer. They must route through the same
// doer so the operator's pacing applies to every credential-validation request.
func TestValidatorFunctionsUsePacedDoer(t *testing.T) {
	installSharedHTTPClient(t, (&Config{}).HTTPClientConfig())

	const intervalMS = 300
	validatorPacingNanos.Store(int64(time.Duration(intervalMS) * time.Millisecond))

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	old := discordAPIBase
	discordAPIBase = srv.URL
	defer func() { discordAPIBase = old }()

	token := "M" + strings.Repeat("a", 23) + ".abc123." + strings.Repeat("A", 26) + "1"
	if len(token) != 59 {
		t.Fatalf("fixture length = %d, want 59", len(token))
	}

	start := time.Now()
	for i := 0; i < 2; i++ {
		ValidateDiscordToken(token)
	}
	if elapsed := time.Since(start); elapsed < time.Duration(intervalMS/2)*time.Millisecond {
		t.Fatalf("two validator requests took %s, want at least %dms of pacing", elapsed, intervalMS)
	}
}

// TestHTTPClientConfigProxyMapping pins the "http:" proxy keys onto the shared
// client's proxy fields: a pool takes precedence over a single proxy, and an
// absent section leaves the compiled-in defaults (no proxy) untouched.
func TestHTTPClientConfigProxyMapping(t *testing.T) {
	ban := 7
	got := (&Config{HTTP: HTTPConfig{
		ProxyURL:      "socks5://127.0.0.1:1080",
		Proxies:       []string{"http://p1:8080", "http://p2:8080"},
		ProxyBanAfter: &ban,
	}}).HTTPClientConfig()
	if got.ProxyURL != "socks5://127.0.0.1:1080" {
		t.Fatalf("ProxyURL = %q, want the configured single proxy", got.ProxyURL)
	}
	if len(got.ProxyPool) != 2 || got.ProxyPool[0] != "http://p1:8080" || got.ProxyPool[1] != "http://p2:8080" {
		t.Fatalf("ProxyPool = %v, want both pool members", got.ProxyPool)
	}
	if got.ProxyBanAfter != 7 {
		t.Fatalf("ProxyBanAfter = %d, want 7", got.ProxyBanAfter)
	}

	empty := (&Config{}).HTTPClientConfig()
	if empty.ProxyURL != "" || len(empty.ProxyPool) != 0 || empty.ProxyBanAfter != 0 {
		t.Fatalf("absent http proxy keys changed defaults: %+v", empty)
	}
}

// TestSharedClientHonorsRetryAfter proves a 429 with Retry-After is retried
// after the advertised delay rather than the (deliberately much larger)
// exponential backoff.
func TestSharedClientHonorsRetryAfter(t *testing.T) {
	cfg := (&Config{}).HTTPClientConfig()
	cfg.Retries = 2
	cfg.BackoffInitial = 5 * time.Second
	cfg.BackoffMax = 5 * time.Second
	cfg.Jitter = false
	cfg.Pacing = 0
	installSharedHTTPClient(t, cfg)

	var count int64
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if atomic.AddInt64(&count, 1) == 1 {
			w.Header().Set("Retry-After", "1")
			w.WriteHeader(http.StatusTooManyRequests)
			return
		}
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	start := time.Now()
	req, _ := http.NewRequest(http.MethodGet, srv.URL, nil)
	resp, err := SharedHTTPClient().Do(req)
	elapsed := time.Since(start)
	if err != nil {
		t.Fatalf("Do: %v", err)
	}
	resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want 200", resp.StatusCode)
	}
	if got := atomic.LoadInt64(&count); got != 2 {
		t.Fatalf("server observed %d requests, want exactly 2", got)
	}
	// Retry-After says 1s; the configured backoff is 5s. Anything above 4s
	// means the advertised value was ignored.
	if elapsed < 900*time.Millisecond || elapsed > 4*time.Second {
		t.Fatalf("retry delay = %s, want ~1s from Retry-After, not the 5s backoff", elapsed)
	}
}
