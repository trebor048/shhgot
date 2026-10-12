// Package proxy provides a small, thread-safe rotating pool of outbound HTTP
// and SOCKS proxies for the shared HTTP client.
//
// A pool hands out one proxy per request in round-robin order, skips members
// that have failed too many times in a row, and restores a member as soon as a
// success is reported for it. It holds no network state of its own: callers
// (the transport) report outcomes, which keeps the pool testable without
// dialing anything.
package proxy

import (
	"fmt"
	"net/url"
	"strings"
	"sync"
	"time"
)

// defaultBanAfter is how many consecutive failures bench a member.
const defaultBanAfter = 3

// defaultBanCooldown is how long a benched member stays out before it is
// re-admitted for a probe. Without it, once every member is benched Next never
// hands one out, so ReportSuccess can never fire and the pool silently falls
// back to a direct connection forever.
const defaultBanCooldown = 30 * time.Second

// supportedSchemes are the proxy URL schemes a pool accepts.
var supportedSchemes = map[string]bool{
	"http":    true,
	"https":   true,
	"socks5":  true,
	"socks5h": true,
}

// member is one proxy and its health state. failures counts consecutive
// failures, reset by ReportSuccess; banned is set once failures reaches the
// pool's threshold.
type member struct {
	url      *url.URL
	failures int
	banned   bool
	bannedAt time.Time
}

// Pool is a rotating set of outbound proxies. It is safe for concurrent use.
type Pool struct {
	mu          sync.Mutex
	members     []*member
	next        int
	banAfter    int
	banCooldown time.Duration
}

// New parses urls into a Pool. Each entry must be an http, https, socks5 or
// socks5h URL with a host; the first invalid entry fails the whole call.
func New(urls []string) (*Pool, error) {
	p := &Pool{banAfter: defaultBanAfter, banCooldown: defaultBanCooldown}
	for _, raw := range urls {
		if err := p.Add(raw); err != nil {
			return nil, err
		}
	}
	return p, nil
}

// Add parses and appends one proxy URL to the pool. It reports an error for an
// empty, malformed, hostless or unsupported-scheme URL.
func (p *Pool) Add(raw string) error {
	u, err := parse(raw)
	if err != nil {
		return err
	}
	p.mu.Lock()
	p.members = append(p.members, &member{url: u})
	p.mu.Unlock()
	return nil
}

// SetBanAfter overrides how many consecutive failures bench a member. Values
// below one are ignored so the pool always has a usable threshold.
func (p *Pool) SetBanAfter(n int) {
	if n <= 0 {
		return
	}
	p.mu.Lock()
	p.banAfter = n
	p.mu.Unlock()
}

// Next returns the next healthy proxy in round-robin order. It reports false
// when the pool is empty or every member is benched, which callers treat as
// "connect directly".
func (p *Pool) Next() (*url.URL, bool) {
	p.mu.Lock()
	defer p.mu.Unlock()
	if len(p.members) == 0 {
		return nil, false
	}
	for i := 0; i < len(p.members); i++ {
		idx := (p.next + i) % len(p.members)
		m := p.members[idx]
		if m.banned {
			// Re-admit a benched member after the cooldown so a transient
			// outage cannot permanently disable the proxy and silently send
			// traffic directly. A success clears the ban sooner.
			if p.banCooldown > 0 && time.Since(m.bannedAt) < p.banCooldown {
				continue
			}
			m.banned = false
			m.failures = 0
		}
		p.next = (idx + 1) % len(p.members)
		// Hand out a copy so a caller cannot mutate the pool's URL.
		u := *m.url
		return &u, true
	}
	return nil, false
}

// ReportSuccess clears the member's consecutive-failure count and un-bans it.
// Unknown or nil URLs are ignored.
func (p *Pool) ReportSuccess(u *url.URL) {
	m := p.find(u)
	if m == nil {
		return
	}
	p.mu.Lock()
	m.failures = 0
	m.banned = false
	p.mu.Unlock()
}

// ReportFailure records one consecutive failure for the member, benching it
// once the threshold is reached. Unknown or nil URLs are ignored.
func (p *Pool) ReportFailure(u *url.URL) {
	m := p.find(u)
	if m == nil {
		return
	}
	p.mu.Lock()
	m.failures++
	if m.failures >= p.banAfter {
		m.banned = true
		m.bannedAt = time.Now()
	}
	p.mu.Unlock()
}

// Stats returns the number of healthy members and the total member count.
func (p *Pool) Stats() (healthy, total int) {
	p.mu.Lock()
	defer p.mu.Unlock()
	for _, m := range p.members {
		if !m.banned {
			healthy++
		}
	}
	return healthy, len(p.members)
}

// find returns the member matching u, or nil when u is nil or unknown.
func (p *Pool) find(u *url.URL) *member {
	if u == nil {
		return nil
	}
	key := u.String()
	p.mu.Lock()
	defer p.mu.Unlock()
	for _, m := range p.members {
		if m.url.String() == key {
			return m
		}
	}
	return nil
}

// parse validates one proxy URL.
func parse(raw string) (*url.URL, error) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return nil, fmt.Errorf("proxy: empty proxy URL")
	}
	u, err := url.Parse(raw)
	if err != nil {
		// Quote the redacted form: the raw entry may carry user:password@.
		// The wrapped parse error echoes the raw URL as well, so redact it
		// too rather than chaining %w verbatim.
		return nil, fmt.Errorf("proxy: invalid proxy URL %q: %v", redactRawProxyURL(raw), redactRawProxyURL(err.Error()))
	}
	if !supportedSchemes[u.Scheme] {
		return nil, fmt.Errorf("proxy: unsupported proxy scheme %q", u.Scheme)
	}
	if u.Host == "" {
		return nil, fmt.Errorf("proxy: proxy URL %q has no host", u.Redacted())
	}
	return u, nil
}

// redactRawProxyURL strips any userinfo credentials from a proxy URL string
// for error messages. A proxy_url with user:password@ must never reach logs.
func redactRawProxyURL(raw string) string {
	if i := strings.Index(raw, "@"); i >= 0 {
		if j := strings.LastIndex(raw[:i], "://"); j >= 0 {
			return raw[:j+3] + "***@" + raw[i+1:]
		}
		return "***@" + raw[i+1:]
	}
	return raw
}
