package httpclient

import (
	"fmt"
	"net"
	"strings"
	"sync"
	"time"
)

// breakerState is the per-host circuit breaker state.
type breakerState int

const (
	stateClosed breakerState = iota
	stateOpen
	stateHalfOpen
)

// maxBreakerHosts bounds the per-host breaker map so a long scan contacting many
// distinct hosts cannot grow it without limit.
const maxBreakerHosts = 10000

func (s breakerState) String() string {
	switch s {
	case stateOpen:
		return "open"
	case stateHalfOpen:
		return "half-open"
	default:
		return "closed"
	}
}

// BreakerOpenError is returned instead of contacting a host whose breaker is
// open. RetryAfter is non-zero when the host is still inside its cooldown.
// State is the breaker state observed when the request was rejected.
type BreakerOpenError struct {
	Host       string
	RetryAfter time.Duration
	State      string
}

func (e *BreakerOpenError) Error() string {
	state := ""
	if e.State != "" {
		state = " (state " + e.State + ")"
	}
	if e.RetryAfter > 0 {
		return fmt.Sprintf("httpclient: circuit breaker open for %s%s (retry after %s)", e.Host, state, e.RetryAfter)
	}
	return fmt.Sprintf("httpclient: circuit breaker open for %s%s", e.Host, state)
}

type breakerHost struct {
	state      breakerState
	failures   int
	openedAt   time.Time
	probesLeft int
}

// breaker tracks one circuit per host. Only network-class failures trip a
// host: a transport error or a 502/503/504 answer. A 500 or 429 (both of
// which are retried) does not trip it, and neither does any response the host
// explicitly returned as healthy.
type breaker struct {
	mu        sync.Mutex
	threshold int
	cooldown  time.Duration
	halfOpen  int
	exempt    []string
	protected []string
	now       func() time.Time
	hosts     map[string]*breakerHost
}

func newBreaker(cfg BreakerConfig, protected []string, now func() time.Time) *breaker {
	halfOpen := cfg.HalfOpenProbes
	if halfOpen < 1 {
		halfOpen = 1
	}
	return &breaker{
		threshold: cfg.FailureThreshold,
		cooldown:  cfg.Cooldown,
		halfOpen:  halfOpen,
		exempt:    cfg.ExemptHosts,
		protected: protected,
		now:       now,
		hosts:     make(map[string]*breakerHost),
	}
}

func (b *breaker) enabled() bool { return b.threshold > 0 }

func (b *breaker) exemptHost(host string) bool {
	return matchesAnyHost(host, b.protected) || matchesAnyHost(host, b.exempt)
}

// allow reports whether a request to host may proceed. It performs the
// OPEN -> HALF_OPEN transition once the cooldown has elapsed and consumes a
// half-open probe slot. A nil return means the request may be sent.
func (b *breaker) allow(host string) error {
	if !b.enabled() || b.exemptHost(host) {
		return nil
	}
	b.mu.Lock()

	entry := b.entry(host)
	now := b.now()
	var retryAfter time.Duration
	rejected := false
	switch entry.state {
	case stateOpen:
		elapsed := now.Sub(entry.openedAt)
		if elapsed < b.cooldown {
			retryAfter = b.cooldown - elapsed
			rejected = true
			break
		}
		entry.state = stateHalfOpen
		entry.probesLeft = b.halfOpen
		fallthrough
	case stateHalfOpen:
		if entry.probesLeft <= 0 {
			rejected = true
			break
		}
		entry.probesLeft--
	}
	b.mu.Unlock()

	if !rejected {
		return nil
	}
	return &BreakerOpenError{Host: host, RetryAfter: retryAfter, State: b.breakerState(host).String()}
}

// record updates the breaker after an attempt. failed must be true only for a
// network-class failure.
func (b *breaker) record(host string, failed bool) {
	if !b.enabled() || b.exemptHost(host) {
		return
	}
	b.mu.Lock()
	defer b.mu.Unlock()

	entry := b.entry(host)
	switch entry.state {
	case stateHalfOpen:
		if failed {
			entry.state = stateOpen
			entry.failures = b.threshold
			entry.openedAt = b.now()
			return
		}
		entry.state = stateClosed
		entry.failures = 0
	case stateClosed:
		if !failed {
			entry.failures = 0
			return
		}
		entry.failures++
		if entry.failures >= b.threshold {
			entry.state = stateOpen
			entry.openedAt = b.now()
		}
	}
}

// breakerState reports the current state for host. allow reads it to annotate
// the BreakerOpenError it returns; tests use it to assert transitions.
func (b *breaker) breakerState(host string) breakerState {
	b.mu.Lock()
	defer b.mu.Unlock()
	if entry, ok := b.hosts[host]; ok {
		return entry.state
	}
	return stateClosed
}

// entry returns host's breaker, creating it CLOSED if absent. The caller must
// hold b.mu.
func (b *breaker) entry(host string) *breakerHost {
	entry, ok := b.hosts[host]
	if !ok {
		// Bound the per-host map so a long scan cannot grow it without limit.
		// Evict a closed entry first; only reset if none is closable.
		if len(b.hosts) >= maxBreakerHosts {
			for h, e := range b.hosts {
				if e.state == stateClosed {
					delete(b.hosts, h)
					break
				}
			}
			if len(b.hosts) >= maxBreakerHosts {
				b.hosts = make(map[string]*breakerHost)
			}
		}
		entry = &breakerHost{state: stateClosed}
		b.hosts[host] = entry
	}
	return entry
}

// matchesAnyHost reports whether host matches a pattern, comparing both the
// host:port form and the bare hostname.
func matchesAnyHost(host string, patterns []string) bool {
	if host == "" || len(patterns) == 0 {
		return false
	}
	hostname := host
	if h, _, err := net.SplitHostPort(host); err == nil {
		hostname = h
	} else {
		hostname = strings.Trim(host, "[]")
	}
	for _, pattern := range patterns {
		pattern = strings.TrimSpace(pattern)
		if pattern == "" {
			continue
		}
		if strings.EqualFold(pattern, host) || strings.EqualFold(strings.Trim(pattern, "[]"), hostname) {
			return true
		}
	}
	return false
}
