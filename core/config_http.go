package core

import (
	"net/http"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/trebor048/shhgot/core/httpclient"
)

// defaultValidatorMinIntervalMS is the per-host pacing the token validator
// applies to credential-validation requests when validators.min_interval_ms is
// not configured. It preserves the historical TokenValidator spacing (~50
// validation requests per minute). Only the validator's Doer applies it: the
// shared client itself defaults to http.pacing_ms (off), so code search,
// GitLab, crypto and AI traffic are not paced at the validator rate.
const defaultValidatorMinIntervalMS = 1200

// HTTPClientConfig maps the "http:" section of config.yaml onto an
// httpclient.Config. Every absent key keeps the value from
// httpclient.DefaultConfig(), so an empty (or nil) Config leaves the shared
// client on its compiled-in defaults. Pacing comes from http.pacing_ms alone
// (off when unset); validators.min_interval_ms is applied per call by the token
// validator's Doer, not by the shared client.
func (c *Config) HTTPClientConfig() httpclient.Config {
	cfg := httpclient.DefaultConfig()
	if c == nil {
		return cfg
	}
	h := c.HTTP
	// retries: 0 is meaningful (disable retries); intOr would treat the explicit
	// zero as "absent" and keep the default 3.
	if h.Retries != nil {
		cfg.Retries = *h.Retries
		if cfg.Retries < 0 {
			cfg.Retries = 0
		}
	}
	cfg.BackoffInitial = msOr(h.BackoffInitialMS, cfg.BackoffInitial)
	cfg.BackoffMax = msOr(h.BackoffMaxMS, cfg.BackoffMax)
	cfg.Jitter = boolOr(h.Jitter, cfg.Jitter)
	cfg.Timeout = secondsOr(h.TimeoutSeconds, cfg.Timeout)
	cfg.Breaker.FailureThreshold = intOr(h.BreakerFailureThreshold, cfg.Breaker.FailureThreshold)
	cfg.Breaker.Cooldown = secondsOr(h.BreakerCooldownSeconds, cfg.Breaker.Cooldown)
	cfg.Breaker.HalfOpenProbes = intOr(h.BreakerHalfOpenProbes, cfg.Breaker.HalfOpenProbes)
	if len(h.ProtectedHosts) > 0 {
		cfg.ProtectedHosts = h.ProtectedHosts
	}
	cfg.Pacing = msOr(h.PacingMS, cfg.Pacing)

	// Outbound proxy: a pool (rotated per request) takes precedence over a
	// single proxy, mirroring httpclient's own precedence.
	if strings.TrimSpace(h.ProxyURL) != "" {
		cfg.ProxyURL = strings.TrimSpace(h.ProxyURL)
	}
	if len(h.Proxies) > 0 {
		cfg.ProxyPool = h.Proxies
	}
	if n := intOr(h.ProxyBanAfter, 0); n > 0 {
		cfg.ProxyBanAfter = n
	}
	return cfg
}

// msOr returns the configured milliseconds, or def when the key is absent or
// <= 0.
func msOr(v *int, def time.Duration) time.Duration {
	return time.Duration(intOr(v, int(def/time.Millisecond))) * time.Millisecond
}

// secondsOr returns the configured seconds, or def when the key is absent or
// <= 0.
func secondsOr(v *int, def time.Duration) time.Duration {
	return time.Duration(intOr(v, int(def/time.Second))) * time.Second
}

var (
	sharedHTTPClientMu sync.Mutex
	sharedHTTPClient   *httpclient.Client
)

// validatorPacingNanos holds the per-host pacing applied to
// credential-validation requests, from validators.min_interval_ms. It is stored
// as nanoseconds in an atomic so a validation running on the worker pool reads
// it without a lock while startup configures it.
var validatorPacingNanos atomic.Int64

func init() {
	validatorPacingNanos.Store(int64(time.Duration(defaultValidatorMinIntervalMS) * time.Millisecond))
}

// validatorDoer is the verifiers.Doer handed to TokenValidator. It layers
// validators.min_interval_ms onto the shared client as a per-call pacing
// override, so only credential validation is paced. Code search, GitLab, crypto
// and AI traffic keep the shared client's http.pacing_ms (off unless
// configured).
type validatorDoer struct {
	client *httpclient.Client
}

func (d validatorDoer) Do(req *http.Request) (*http.Response, error) {
	interval := time.Duration(validatorPacingNanos.Load())
	opts := httpclient.Options{PacingInterval: &interval}
	if !metricsEnabled.Load() {
		return d.client.DoWithOptions(req, opts)
	}
	start := time.Now()
	resp, err := d.client.DoWithOptions(req, opts)
	DefaultMetrics.ObserveLatency(SubsystemValidation, time.Since(start))
	DefaultMetrics.IncOperations(SubsystemValidation)
	if err != nil {
		DefaultMetrics.IncError(SubsystemValidation)
	}
	return resp, err
}

// validatorHTTPDoer returns the paced doer used for credential-validation
// requests. It layers validators.min_interval_ms onto the shared client as a
// per-call pacing override, exactly as NewTokenValidator does for
// TokenValidator. The standalone Discord/Telegram validators in validators.go
// use it so the operator's pacing applies to their requests too.
func validatorHTTPDoer() validatorDoer {
	return validatorDoer{client: SharedHTTPClient()}
}

// SharedHTTPClient returns the process-wide outbound HTTP client used by the
// validators and the crypto-balance checker. Sharing one client means retries,
// backoff, per-host pacing and the circuit breaker are configured in exactly one
// place.
func SharedHTTPClient() *httpclient.Client {
	sharedHTTPClientMu.Lock()
	defer sharedHTTPClientMu.Unlock()
	if sharedHTTPClient == nil {
		sharedHTTPClient = httpclient.New((&Config{}).HTTPClientConfig())
	}
	return sharedHTTPClient
}

// ConfigureSharedHTTPClient rebuilds the process-wide outbound client from the
// loaded config. It replaces the lazily built default so the operator's http.*
// tuning - retries, backoff, jitter, timeout, pacing, breaker and protected
// hosts - actually reaches the client used by the validators, search, GitLab
// and the crypto-balance checker. It must run once at startup, after config
// load and before any worker or source starts. The webhook queue keeps its own
// retries-0/pacing-off client and is deliberately untouched.
//
// A nil *Config is allowed and leaves the compiled-in client defaults in place
// (HTTPClientConfig handles the nil receiver).
func ConfigureSharedHTTPClient(c *Config) {
	sharedHTTPClientMu.Lock()
	sharedHTTPClient = httpclient.New(c.HTTPClientConfig())
	sharedHTTPClientMu.Unlock()

	validatorPacingNanos.Store(int64(c.ValidatorMinInterval()))
	// Fold the operator's metrics gate into the process-wide collector toggle.
	// c may be nil (HTTPClientConfig tolerates it), and MetricsEnabled is
	// nil-safe: an absent key leaves metrics on.
	SetMetricsEnabled(c.MetricsEnabled())
}
