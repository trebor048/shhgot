package core

import (
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/trebor048/shhgot/core/httpclient"
)

// Doer is the minimal HTTP transport shared by the fetch helper and the
// code-search client. It is satisfied by *http.Client and by the pooled
// core/httpclient.Client.
type Doer interface {
	Do(*http.Request) (*http.Response, error)
}

const (
	// defaultMaxFileSize bounds a downloaded file to 2 MiB unless the Fetcher
	// overrides it.
	defaultMaxFileSize = 2 << 20
	// fetchRetryDelay is the fallback wait before retrying a 429/5xx response
	// that advertises no Retry-After.
	fetchRetryDelay = 2 * time.Second
)

var (
	errHostNotAllowed = errors.New("core: download host is not allowlisted")
	errFileTooLarge   = errors.New("core: file exceeds maximum size")
)

// defaultAllowedFetchHosts are the only hosts a Fetcher downloads from when it
// does not supply its own allowlist: GitHub's raw-content, API and gist raw
// hosts.
var defaultAllowedFetchHosts = []string{"raw.githubusercontent.com", "api.github.com", "gist.githubusercontent.com"}

// Fetcher downloads individual files from an allowlisted host with a size cap
// and bounded retries. An off-allowlist host is rejected before any request is
// issued.
type Fetcher struct {
	// Doer performs the HTTP request. Required.
	Doer Doer
	// MaxFileSize caps the accepted body in bytes; <=0 uses defaultMaxFileSize.
	MaxFileSize int64
	// AllowedHosts is the hostname allowlist; empty uses the GitHub raw/API hosts.
	AllowedHosts []string
	// Now and Sleep are injectable seams; nil uses the real clock and sleep.
	Now   func() time.Time
	Sleep func(time.Duration)
	// MaxRetries is the number of additional attempts after the first; a
	// negative value is clamped to zero.
	MaxRetries int
}

// Fetch downloads rawURL and returns its body. The host must be on the
// allowlist, the body is capped at MaxFileSize, and 429/5xx responses are
// retried up to MaxRetries times, honouring Retry-After when present.
//
// The URL must use http(s) and carry no userinfo: credentials in the URL
// would be sent to the server and echoed into error logs. A non-default port
// is accepted only when an allowlist entry names that same host:port, so an
// allowlisted host cannot be steered to an arbitrary service on that host.
//
// Private-IP enforcement is deliberately left to the allowlist itself: every
// non-default entry is an explicit operator opt-in, and self-hosted instances
// on intranet addresses are a legitimate configuration.
func (f *Fetcher) Fetch(rawURL string) ([]byte, error) {
	u, err := url.Parse(strings.TrimSpace(rawURL))
	if err != nil {
		return nil, fmt.Errorf("core: invalid URL: %w", err)
	}
	if u.Scheme != "http" && u.Scheme != "https" {
		return nil, fmt.Errorf("core: unsupported URL scheme %q", u.Scheme)
	}
	if u.User != nil {
		return nil, errors.New("core: URL must not carry userinfo credentials")
	}
	if !hostPortAllowed(u, f.allowedHosts()) {
		return nil, fmt.Errorf("%w: %s", errHostNotAllowed, u.Hostname())
	}
	if f.Doer == nil {
		return nil, errors.New("core: Fetcher.Doer is nil")
	}

	now := f.Now
	if now == nil {
		now = time.Now
	}
	sleep := f.Sleep
	if sleep == nil {
		sleep = time.Sleep
	}
	maxSize := f.MaxFileSize
	if maxSize <= 0 {
		maxSize = defaultMaxFileSize
	}
	retries := f.MaxRetries
	if retries < 0 {
		retries = 0
	}

	req, err := http.NewRequest(http.MethodGet, u.String(), nil)
	if err != nil {
		return nil, fmt.Errorf("core: building request: %w", err)
	}
	req.Header.Set("User-Agent", "shhgit2")
	req.Header.Set("Accept", "application/vnd.github.raw")

	for attempt := 0; ; attempt++ {
		resp, err := f.Doer.Do(req)
		if err != nil {
			if attempt >= retries {
				return nil, fmt.Errorf("core: fetching %s: %w", u.Redacted(), err)
			}
			sleep(fetchRetryDelay)
			continue
		}

		switch {
		case resp.StatusCode == http.StatusOK:
			return readCapped(resp.Body, maxSize)
		case resp.StatusCode == http.StatusTooManyRequests || resp.StatusCode >= 500:
			if attempt >= retries {
				status := resp.StatusCode
				closeResponse(resp)
				return nil, fmt.Errorf("core: fetching %s: server status %d", u.Redacted(), status)
			}
			delay := retryAfterDelay(resp, now(), fetchRetryDelay)
			closeResponse(resp)
			sleep(delay)
		default:
			status := resp.StatusCode
			closeResponse(resp)
			return nil, fmt.Errorf("core: fetching %s: server status %d", u.Redacted(), status)
		}
	}
}

// allowedHosts returns the configured allowlist, or the default GitHub hosts.
func (f *Fetcher) allowedHosts() []string {
	if len(f.AllowedHosts) == 0 {
		return defaultAllowedFetchHosts
	}
	return f.AllowedHosts
}

// hostAllowed reports whether host matches any allowlist entry. Entries may be
// bare hostnames, host:port pairs, or full URLs; the comparison uses the
// lower-cased hostname with any port stripped.
func hostAllowed(host string, allowed []string) bool {
	host = canonicalHost(host)
	if host == "" {
		return false
	}
	for _, entry := range allowed {
		if canonicalHost(entry) == host {
			return true
		}
	}
	return false
}

// hostPortAllowed reports whether u may be fetched under the allowlist. A URL
// with an empty or scheme-default port needs a matching hostname entry; a URL
// with an explicit non-default port additionally requires an entry naming that
// same host:port, so an allowlisted host cannot be steered to an arbitrary
// service on that host. Entries are bare hostnames (default ports only),
// host:port pairs, or full URLs.
func hostPortAllowed(u *url.URL, allowed []string) bool {
	if u == nil {
		return false
	}
	host := canonicalHost(u.Hostname())
	if host == "" {
		return false
	}
	defPort := "443"
	if u.Scheme == "http" {
		defPort = "80"
	}
	if p := u.Port(); p == "" || p == defPort {
		return hostAllowed(host, allowed)
	}
	want := u.Port()
	for _, entry := range allowed {
		eHost, ePort := entryHostPort(entry)
		if eHost == host && ePort == want {
			return true
		}
	}
	return false
}

// entryListed reports whether the exact entry is already allowlisted
// (case-insensitive, surrounding whitespace ignored).
func entryListed(entry string, allowed []string) bool {
	entry = strings.TrimSpace(strings.ToLower(entry))
	for _, h := range allowed {
		if strings.TrimSpace(strings.ToLower(h)) == entry {
			return true
		}
	}
	return false
}

// entryHostPort splits an allowlist entry into its lower-cased hostname and
// explicit port ("" when the entry names no port). It accepts the same forms
// as canonicalHost: bare hostnames, host:port pairs and full URLs.
func entryHostPort(entry string) (host, port string) {
	entry = strings.TrimSpace(strings.ToLower(entry))
	if i := strings.Index(entry, "://"); i >= 0 {
		entry = entry[i+3:]
	}
	if i := strings.IndexByte(entry, '/'); i >= 0 {
		entry = entry[:i]
	}
	if h, p, err := net.SplitHostPort(entry); err == nil {
		return strings.TrimSuffix(h, "."), p
	}
	return strings.TrimSuffix(entry, "."), ""
}

// canonicalHost normalises an allowlist entry or hostname to a lower-cased
// hostname with any scheme, path and port removed.
func canonicalHost(entry string) string {
	entry = strings.TrimSpace(strings.ToLower(entry))
	if i := strings.Index(entry, "://"); i >= 0 {
		entry = entry[i+3:]
	}
	if i := strings.IndexByte(entry, '/'); i >= 0 {
		entry = entry[:i]
	}
	if h, _, err := net.SplitHostPort(entry); err == nil {
		entry = h
	}
	return strings.TrimSuffix(entry, ".")
}

// readCapped reads at most max bytes from body, returning errFileTooLarge when
// the body is longer.
func readCapped(body io.ReadCloser, max int64) ([]byte, error) {
	defer body.Close()
	data, err := io.ReadAll(io.LimitReader(body, max+1))
	if err != nil {
		return nil, fmt.Errorf("core: reading response: %w", err)
	}
	if int64(len(data)) > max {
		return nil, errFileTooLarge
	}
	return data, nil
}

// closeResponse drains and closes a response body so the connection can be
// reused. The drain is bounded so a hostile endpoint cannot stream forever.
func closeResponse(resp *http.Response) {
	if resp == nil || resp.Body == nil {
		return
	}
	_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, 1<<16))
	_ = resp.Body.Close()
}

// retryAfterDelay returns the advertised Retry-After / X-RateLimit-Reset-After
// delay, or fallback when neither header carries a usable value.
func retryAfterDelay(resp *http.Response, now time.Time, fallback time.Duration) time.Duration {
	if d, ok := retryAfterDelayOK(resp, now); ok {
		return d
	}
	return fallback
}

// retryAfterDelayOK is retryAfterDelay's header-presence-aware form: it reports
// whether a usable Retry-After / X-RateLimit-Reset-After header was present.
// Callers use it to retry only a genuine rate-limit answer - a 403 without one
// (a WAF/bot block) must return immediately rather than park on a blind wait.
func retryAfterDelayOK(resp *http.Response, now time.Time) (time.Duration, bool) {
	if resp != nil {
		if d, ok := httpclient.ParseRetryAfter(resp.Header.Get("Retry-After"), now); ok {
			return d, true
		}
		if d, ok := httpclient.ParseRetryAfter(resp.Header.Get("X-RateLimit-Reset-After"), now); ok {
			return d, true
		}
	}
	return 0, false
}
