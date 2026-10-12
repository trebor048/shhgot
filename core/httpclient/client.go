// Package httpclient provides a shared, resilient outbound HTTP client for
// shhgit2's verifiers, AI providers, search and webhook paths.
//
// The package is deliberately core-free: it imports only the standard library
// so packages that must not depend on shhgit2/core (for example
// core/verifiers) can use it without creating an import cycle. It owns retry,
// backoff, per-host pacing and a per-host circuit breaker, and it exposes its
// own Config so callers map their configuration onto it.
package httpclient

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"math/rand/v2"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"

	"github.com/trebor048/shhgot/core/proxy"
	xnetproxy "golang.org/x/net/proxy"
)

// BreakerConfig configures the per-host circuit breaker.
type BreakerConfig struct {
	// FailureThreshold is the number of network-class failures that trips a
	// host from CLOSED to OPEN. Zero disables the breaker entirely.
	FailureThreshold int
	// Cooldown is how long an OPEN host rejects requests before admitting
	// half-open probes.
	Cooldown time.Duration
	// HalfOpenProbes is the number of probes admitted once the cooldown
	// elapses. Values below one are normalised to one.
	HalfOpenProbes int
	// ExemptHosts never trip the breaker, for example webhook endpoints that
	// own their own retry policy.
	ExemptHosts []string
}

// Config configures a Client.
//
// Note that the zero value differs from DefaultConfig: a zero Config disables
// retries, backoff, pacing and the breaker and applies no request timeout.
// That is the correct shape for a caller such as the webhook queue that owns
// its own retry and pacing policy.
type Config struct {
	// Retries is the number of additional transport attempts after the first
	// failure. The total number of attempts is Retries+1.
	Retries int
	// BackoffInitial is the first backoff delay; it doubles on each retry up
	// to BackoffMax.
	BackoffInitial time.Duration
	// BackoffMax caps the exponential backoff.
	BackoffMax time.Duration
	// Timeout is the overall per-attempt request timeout.
	Timeout time.Duration
	// Pacing is the minimum spacing between requests to the same host. Zero
	// disables pacing.
	Pacing time.Duration
	// Jitter applies +/-50% jitter to exponential backoff (never to an
	// advertised Retry-After value).
	Jitter bool
	// Breaker configures the per-host circuit breaker.
	Breaker BreakerConfig
	// ProtectedHosts are hosts that must always receive requests even while
	// other hosts have an open breaker (for example AI endpoints).
	ProtectedHosts []string
	// ProxyURL is a single outbound proxy used for every request. It accepts
	// http, https, socks5 and socks5h URLs. ProxyPool takes precedence when
	// both are set.
	ProxyURL string
	// ProxyPool is a list of outbound proxies rotated one per request. A
	// member that fails ProxyBanAfter times consecutively is benched until a
	// success resets it. When the whole pool is benched the client falls back
	// to a direct connection.
	ProxyPool []string
	// ProxyBanAfter overrides the number of consecutive failures after which
	// a ProxyPool member is benched. Zero uses the pool default (3).
	ProxyBanAfter int
}

// DefaultConfig returns conservative defaults for a shared scanner client.
func DefaultConfig() Config {
	return Config{
		Retries:        3,
		BackoffInitial: 500 * time.Millisecond,
		BackoffMax:     30 * time.Second,
		Timeout:        15 * time.Second,
		Pacing:         0,
		Jitter:         true,
		Breaker: BreakerConfig{
			FailureThreshold: 5,
			Cooldown:         30 * time.Second,
			HalfOpenProbes:   1,
		},
	}
}

// Options overrides selected behaviours for a single call. A nil field keeps
// the client-level configuration.
type Options struct {
	// Retries overrides Config.Retries for this call.
	Retries *int
	// Pacing, when non-nil, enables or disables per-host pacing for this call.
	// A nil or true value keeps the client-level Pacing; false disables it.
	Pacing *bool
	// PacingInterval, when non-nil, overrides the client-level Pacing for this
	// call only. It lets one caller (for example credential validation) pace at
	// its own interval while every other caller keeps Config.Pacing.
	PacingInterval *time.Duration
}

// Client is a pooled HTTP client with retries, per-host pacing and a per-host
// circuit breaker. It is safe for concurrent use.
type Client struct {
	cfg     Config
	http    *http.Client
	breaker *breaker
	pacer   *pacer
	pool    *proxy.Pool
	initErr error

	now    func() time.Time
	sleep  func(time.Duration)
	jitter func() float64
}

// New returns a Client using the real time and jitter sources.
func New(cfg Config) *Client {
	return newClient(cfg, time.Now, time.Sleep, rand.Float64)
}

// newClient builds a Client with injectable clock, sleep and jitter seams so
// tests never block or depend on wall-clock randomness.
func newClient(cfg Config, now func() time.Time, sleep func(time.Duration), jitter func() float64) *Client {
	c := &Client{
		cfg:    cfg,
		now:    now,
		sleep:  sleep,
		jitter: jitter,
	}
	c.breaker = newBreaker(cfg.Breaker, cfg.ProtectedHosts, now)
	c.pacer = newPacer(now, sleep)

	tr := &http.Transport{
		MaxIdleConns:          100,
		MaxIdleConnsPerHost:   10,
		IdleConnTimeout:       30 * time.Second,
		ForceAttemptHTTP2:     true,
		TLSHandshakeTimeout:   10 * time.Second,
		ExpectContinueTimeout: time.Second,
	}
	c.pool, c.initErr = configureProxy(tr, cfg)
	c.http = &http.Client{
		Timeout:       cfg.Timeout,
		Transport:     tr,
		CheckRedirect: sameOriginRedirect,
	}
	return c
}

// proxyChoiceKey is the context key under which configureProxy's Proxy func
// records the proxy it selected, so DoWithOptions can report the outcome back
// to the pool.
type proxyChoiceKey struct{}

// proxyChoice carries the URL the transport's Proxy func selected for one
// attempt. It is stored per request in the request context.
type proxyChoice struct {
	used *url.URL
}

// configureProxy installs the proxy policy on tr and returns the rotating pool
// when one is configured. A pool takes precedence over a single ProxyURL; a
// socks5/socks5h proxy is dialed through golang.org/x/net/proxy instead of the
// transport's HTTP CONNECT path. With no proxy configured the environment
// proxy settings are kept.
func configureProxy(tr *http.Transport, cfg Config) (*proxy.Pool, error) {
	if len(cfg.ProxyPool) > 0 {
		pool, err := proxy.New(cfg.ProxyPool)
		if err != nil {
			return nil, err
		}
		if cfg.ProxyBanAfter > 0 {
			pool.SetBanAfter(cfg.ProxyBanAfter)
		}
		tr.Proxy = func(req *http.Request) (*url.URL, error) {
			u, ok := pool.Next()
			if !ok {
				// Every member is benched: fall back to a direct connection.
				return nil, nil
			}
			if choice, _ := req.Context().Value(proxyChoiceKey{}).(*proxyChoice); choice != nil {
				choice.used = u
			}
			return u, nil
		}
		return pool, nil
	}

	if cfg.ProxyURL == "" {
		tr.Proxy = http.ProxyFromEnvironment
		return nil, nil
	}

	u, err := url.Parse(strings.TrimSpace(cfg.ProxyURL))
	if err != nil {
		return nil, fmt.Errorf("httpclient: invalid proxy URL: %w", err)
	}
	if u.Scheme == "socks5" || u.Scheme == "socks5h" {
		dialer, err := newSocksDialer(u)
		if err != nil {
			return nil, err
		}
		// The SOCKS dialer owns the connection to the proxy, so the transport
		// must not also apply an HTTP proxy.
		tr.Proxy = nil
		tr.DialContext = dialer.DialContext
		return nil, nil
	}
	if u.Scheme != "http" && u.Scheme != "https" {
		return nil, fmt.Errorf("httpclient: unsupported proxy scheme %q", u.Scheme)
	}
	tr.Proxy = http.ProxyURL(u)
	return nil, nil
}

// newSocksDialer builds the SOCKS5 dialer for a socks5/socks5h proxy URL. It is
// a variable so tests can observe the selection without opening a connection.
var newSocksDialer = func(u *url.URL) (xnetproxy.ContextDialer, error) {
	d, err := xnetproxy.FromURL(u, xnetproxy.Direct)
	if err != nil {
		return nil, fmt.Errorf("httpclient: socks proxy %q: %w", u.Host, err)
	}
	cd, ok := d.(xnetproxy.ContextDialer)
	if !ok {
		return nil, fmt.Errorf("httpclient: socks proxy %q has no context dialer", u.Host)
	}
	return cd, nil
}

// errRedirectRefused marks a redirect the client policy rejected. It is a
// sentinel so DoWithOptions can recognise the refusal as deterministic (see
// isNonRetryableError) instead of retrying it and charging the host breaker.
var errRedirectRefused = errors.New("httpclient: refusing cross-origin redirect")

// sameOriginRedirect rejects a redirect whose target scheme or host differs
// from the original request. Go's default follows up to ten redirects to any
// host, which lets an allowlisted server bounce a scanner request onto an
// internal address (SSRF). A same-origin redirect is still validated by
// SafeURL so a hostname that now resolves to a private address is refused as
// well. Returning an error fails the request; the 3xx is never surfaced and
// the redirect target is never contacted.
func sameOriginRedirect(req *http.Request, via []*http.Request) error {
	if len(via) == 0 {
		return nil
	}
	origin := via[0].URL
	if req.URL.Scheme != origin.Scheme || req.URL.Host != origin.Host {
		return fmt.Errorf("%w: from %s://%s to %s://%s",
			errRedirectRefused, origin.Scheme, origin.Host, req.URL.Scheme, req.URL.Host)
	}
	if err := SafeURL(req.URL.String()); err != nil {
		return fmt.Errorf("httpclient: refusing redirect to %s: %w", req.URL, err)
	}
	return nil
}

// Do performs req using the client-wide retry and pacing settings.
func (c *Client) Do(req *http.Request) (*http.Response, error) {
	return c.DoWithOptions(req, Options{})
}

// DoWithOptions performs req, allowing the retry count and pacing to be
// overridden for this call only.
func (c *Client) DoWithOptions(req *http.Request, opts Options) (*http.Response, error) {
	if c.initErr != nil {
		return nil, c.initErr
	}
	retries := c.cfg.Retries
	if opts.Retries != nil {
		retries = *opts.Retries
	}
	if retries < 0 {
		retries = 0
	}
	pacing := c.cfg.Pacing
	if opts.PacingInterval != nil {
		pacing = *opts.PacingInterval
	}
	if opts.Pacing != nil && !*opts.Pacing {
		pacing = 0
	}

	host := req.URL.Host
	// A request body is consumed by the first attempt, so when retries are
	// possible buffer it and install a replayable GetBody.
	if err := prepareBody(req, retries); err != nil {
		return nil, err
	}

	attempts := retries + 1
	// Clamp so a MaxInt retry count cannot wrap attempts negative and skip the
	// loop entirely.
	if retries > maxRetries {
		retries = maxRetries
		attempts = retries + 1
	}
	for attempt := 0; attempt < attempts; attempt++ {
		if attempt > 0 && req.GetBody != nil {
			body, err := req.GetBody()
			if err != nil {
				return nil, err
			}
			req.Body = body
		}

		// Fail fast before pacing or contacting a host whose breaker is open.
		if err := c.breaker.allow(host); err != nil {
			return nil, err
		}
		c.pacer.wait(host, pacing)

		// In pool mode the transport's Proxy func records the member it picked
		// in the request context, so its outcome can be reported below.
		var choice *proxyChoice
		if c.pool != nil {
			choice = &proxyChoice{}
			req = req.WithContext(context.WithValue(req.Context(), proxyChoiceKey{}, choice))
		}

		resp, err := c.http.Do(req)
		if c.pool != nil {
			if err != nil {
				c.pool.ReportFailure(choice.used)
			} else {
				c.pool.ReportSuccess(choice.used)
			}
		}
		if err != nil {
			// A cancelled/expired context or a redirect-policy refusal is
			// deterministic, not a transient transport failure. Retrying it
			// burns the backoff budget, and charging it to the host breaker
			// used to trip the breaker for a healthy host after a burst of
			// cancellations.
			if isNonRetryableError(err) {
				return nil, err
			}
			c.breaker.record(host, true)
			if attempt+1 >= attempts {
				return nil, err
			}
			if req.Context() != nil && req.Context().Err() != nil {
				return nil, err
			}
			c.sleep(c.backoff(attempt))
			continue
		}

		// A received response means the host is reachable; only gateway-class
		// statuses count against the breaker.
		c.breaker.record(host, isBreakerFailureStatus(resp.StatusCode))

		if !isRetryableStatus(resp.StatusCode) || attempt+1 >= attempts {
			return resp, nil
		}

		delay := c.retryDelay(resp, attempt)
		drainAndClose(resp.Body)
		c.sleep(delay)
	}
	// The loop returns on its final attempt, so this is unreachable; return a
	// defensive error rather than a nil response with a nil error.
	return nil, errors.New("httpclient: retry loop exited without a response")
}

// prepareBody buffers req.Body when the request may be retried and the caller
// did not already supply a replayable GetBody.
func prepareBody(req *http.Request, retries int) error {
	if retries <= 0 || req.Body == nil || req.GetBody != nil {
		return nil
	}
	body, err := io.ReadAll(req.Body)
	closeErr := req.Body.Close()
	if err == nil {
		err = closeErr
	}
	if err != nil {
		return err
	}
	req.Body = io.NopCloser(bytes.NewReader(body))
	req.GetBody = func() (io.ReadCloser, error) {
		return io.NopCloser(bytes.NewReader(body)), nil
	}
	if req.ContentLength <= 0 {
		req.ContentLength = int64(len(body))
	}
	return nil
}

// retryDelay returns the wait before the next attempt: an advertised
// Retry-After or X-RateLimit-Reset-After if present, otherwise exponential
// backoff.
func (c *Client) retryDelay(resp *http.Response, attempt int) time.Duration {
	if resp != nil {
		if d, ok := ParseRetryAfter(resp.Header.Get("Retry-After"), c.now()); ok {
			return d
		}
		if d, ok := ParseRetryAfter(resp.Header.Get("X-RateLimit-Reset-After"), c.now()); ok {
			return d
		}
	}
	return c.backoff(attempt)
}

// maxRetries bounds an explicit retry override so retries+1 cannot overflow.
const maxRetries = 100

// maxPacerHosts bounds the per-host pacing map so a long scan contacting many
// distinct hosts cannot grow it without limit.
const maxPacerHosts = 10000

// isNonRetryableError reports whether err is deterministic for this request
// rather than a transient transport failure: a cancelled/expired context or a
// redirect-policy refusal.
func isNonRetryableError(err error) bool {
	if err == nil {
		return false
	}
	// A redirect the policy rejected (cross-origin, or a target SafeURL deems
	// private/plain-http) is deterministic: re-issuing the same request cannot
	// change the outcome, so it must not burn the retry budget or charge the
	// host breaker. errURLResolve is deliberately excluded - a DNS failure can
	// be transient.
	return errors.Is(err, context.Canceled) ||
		errors.Is(err, context.DeadlineExceeded) ||
		errors.Is(err, http.ErrUseLastResponse) ||
		errors.Is(err, errRedirectRefused) ||
		errors.Is(err, errURLScheme) ||
		errors.Is(err, errURLUserInfo) ||
		errors.Is(err, errURLPlainHTTP) ||
		errors.Is(err, errURLHost) ||
		errors.Is(err, errURLNonPublic) ||
		errors.Is(err, errURLInternal)
}

// backoff returns min(initial*2^attempt, max) with optional +/-50% jitter.
func (c *Client) backoff(attempt int) time.Duration {
	initial := c.cfg.BackoffInitial
	if initial <= 0 {
		return 0
	}
	max := c.cfg.BackoffMax
	if max <= 0 {
		// Without a configured ceiling, still bound the doubling so it cannot
		// overflow int64 into a negative delay (which slept 0 and silently
		// disabled backoff).
		max = 1 << 62
	}
	delay := initial
	for i := 0; i < attempt && delay < max; i++ {
		if delay > max/2 {
			delay = max
			break
		}
		delay *= 2
	}
	if delay > max {
		delay = max
	}
	return c.applyJitter(delay)
}

// applyJitter scales d by a factor in [0.5, 1.5) when jitter is enabled.
func (c *Client) applyJitter(d time.Duration) time.Duration {
	if !c.cfg.Jitter || d <= 0 {
		return d
	}
	factor := 0.5 + c.jitter()
	return time.Duration(float64(d) * factor)
}

// drainAndClose empties a response body so the connection can be reused, then
// closes it. The read is bounded so a hostile endpoint cannot stream forever.
func drainAndClose(body io.ReadCloser) {
	if body == nil {
		return
	}
	_, _ = io.Copy(io.Discard, io.LimitReader(body, 1<<16))
	_ = body.Close()
}

// pacer serialises requests to a host so consecutive calls are at least
// pacing apart, without holding a lock while sleeping.
type pacer struct {
	mu    sync.Mutex
	last  map[string]time.Time
	now   func() time.Time
	sleep func(time.Duration)
}

func newPacer(now func() time.Time, sleep func(time.Duration)) *pacer {
	return &pacer{last: make(map[string]time.Time), now: now, sleep: sleep}
}

func (p *pacer) wait(host string, pacing time.Duration) {
	if pacing <= 0 || host == "" {
		return
	}
	p.mu.Lock()
	now := p.now()
	// Bound the per-host pacing map: a long scan contacts many distinct hosts
	// and the map was never pruned.
	if len(p.last) >= maxPacerHosts {
		for h, t := range p.last {
			if now.Sub(t) > pacing {
				delete(p.last, h)
			}
		}
		if len(p.last) >= maxPacerHosts {
			p.last = make(map[string]time.Time)
		}
	}
	var delay time.Duration
	if last, ok := p.last[host]; ok && !last.IsZero() {
		if elapsed := now.Sub(last); elapsed < pacing {
			delay = pacing - elapsed
		}
	}
	p.last[host] = now.Add(delay)
	p.mu.Unlock()
	if delay > 0 {
		p.sleep(delay)
	}
}
