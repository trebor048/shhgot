package httpclient

import (
	"errors"
	"fmt"
	"net"
	"net/url"
	"strings"
	"sync"
	"time"
)

// lookupIP is the resolver SafeURL uses. It is a variable so tests can install
// a deterministic resolver and never touch the network.
var lookupIP = net.LookupIP

// DNS results are cached briefly because a scanner resolves the same host many
// times: ExtractBaseURL ranks every URL candidate in a file, and the AI and
// validator paths re-check endpoints repeatedly. Each miss is a blocking
// net.LookupIP on the publish path, so a small TTL-bounded cache removes most
// of that work while still re-validating a host that changes address.
const (
	resolveCacheTTL = time.Minute
	resolveCacheMax = 256
)

type resolveEntry struct {
	ips     []net.IP
	expires time.Time
}

// resolveCache is a bounded, TTL-expiring cache of hostname resolutions. Its
// zero value is not usable; the package-level safeURLCache is the only
// instance.
type resolveCache struct {
	mu      sync.Mutex
	entries map[string]resolveEntry
	max     int
	ttl     time.Duration
}

var safeURLCache = &resolveCache{
	entries: make(map[string]resolveEntry),
	max:     resolveCacheMax,
	ttl:     resolveCacheTTL,
}

// lookupHost resolves host through the cache, falling back to lookupIP on a
// miss and caching only successful, non-empty results.
func lookupHost(host string) ([]net.IP, error) {
	now := time.Now()
	if ips, ok := safeURLCache.get(host, now); ok {
		return ips, nil
	}
	ips, err := lookupIP(host)
	if err != nil {
		return nil, err
	}
	safeURLCache.set(host, ips, now)
	return ips, nil
}

func (c *resolveCache) get(host string, now time.Time) ([]net.IP, bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	e, ok := c.entries[host]
	if !ok {
		return nil, false
	}
	if now.After(e.expires) {
		delete(c.entries, host)
		return nil, false
	}
	return e.ips, true
}

func (c *resolveCache) set(host string, ips []net.IP, now time.Time) {
	if len(ips) == 0 {
		return
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	if len(c.entries) >= c.max {
		// Reclaim expired entries first; if none expired the cache is at its
		// bound and this result is left uncached rather than growing forever.
		for h, e := range c.entries {
			if now.After(e.expires) {
				delete(c.entries, h)
			}
		}
		if len(c.entries) >= c.max {
			return
		}
	}
	c.entries[host] = resolveEntry{ips: ips, expires: now.Add(c.ttl)}
}

var (
	errURLScheme    = errors.New("httpclient: only http and https URLs are allowed")
	errURLUserInfo  = errors.New("httpclient: URLs with embedded credentials are not allowed")
	errURLPlainHTTP = errors.New("httpclient: plain http is only allowed for loopback hosts")
	errURLHost      = errors.New("httpclient: URL must have a host")
	errURLNonPublic = errors.New("httpclient: URL resolves to a non-public address")
	errURLInternal  = errors.New("httpclient: URL uses an internal hostname suffix")
	errURLResolve   = errors.New("httpclient: URL host could not be resolved")
)

// internalSuffixes are hostname suffixes reserved for private networks; the
// public DNS hierarchy never resolves them, so they are always refused.
var internalSuffixes = []string{".local", ".internal", ".corp", ".lan", ".home"}

// SafeURL reports whether raw is safe for a scanner to fetch. It accepts only
// http(s) with no embedded credentials, refuses internal hostname suffixes,
// and requires every resolved address to be public. `localhost` is allowed for
// development, and plain http is permitted only for loopback hosts.
func SafeURL(raw string) error {
	u, err := url.Parse(strings.TrimSpace(raw))
	if err != nil {
		return fmt.Errorf("httpclient: invalid URL: %w", err)
	}
	if u.Scheme != "http" && u.Scheme != "https" {
		return errURLScheme
	}
	if u.User != nil {
		return errURLUserInfo
	}
	host := strings.ToLower(u.Hostname())
	// A trailing root dot ("db.internal.") must not bypass the suffix denylist
	// or the localhost comparison.
	host = strings.TrimSuffix(host, ".")
	if host == "" {
		return errURLHost
	}
	if host == "localhost" {
		return nil
	}
	if hasInternalSuffix(host) {
		return errURLInternal
	}

	if ip := net.ParseIP(host); ip != nil {
		if ip.IsLoopback() {
			// Loopback is reachable only over plain http, never https.
			if u.Scheme == "http" {
				return nil
			}
			return errURLNonPublic
		}
		if !isPublicIP(ip) {
			return errURLNonPublic
		}
		if u.Scheme == "http" {
			return errURLPlainHTTP
		}
		return nil
	}

	if u.Scheme == "http" {
		return errURLPlainHTTP
	}

	ips, err := lookupHost(host)
	if err != nil {
		return fmt.Errorf("%w: %v", errURLResolve, err)
	}
	if len(ips) == 0 {
		return errURLResolve
	}
	for _, ip := range ips {
		if !isPublicIP(ip) {
			return errURLNonPublic
		}
	}
	return nil
}

func hasInternalSuffix(host string) bool {
	for _, suffix := range internalSuffixes {
		if strings.HasSuffix(host, suffix) {
			return true
		}
	}
	return false
}

// isPublicIP reports whether ip is a globally routable address. Loopback,
// private, link-local, multicast, unspecified, 0.0.0.0/8 and the IANA
// special-use ranges (CGNAT, 192.0.0.0/24, 198.18.0.0/15 and 240.0.0.0/4) are
// not public.
//
// IPv6 transition ranges that embed an IPv4 address are checked against that
// embedded address: 6to4 (2002::/16) can hide 127.0.0.1 or RFC1918 behind a
// public-looking prefix, as can NAT64 (64:ff9b::/96 and 64:ff9b:1::/48) and the
// deprecated IPv4-compatible ::/96 form. Teredo (2001:0000::/32) is a
// transition mechanism that is never a normal public endpoint, so the whole
// block is refused.
func isPublicIP(ip net.IP) bool {
	if ip == nil {
		return false
	}
	if v4 := ip.To4(); v4 != nil {
		return isPublicIPv4(v4)
	}
	if ip.IsLoopback() || ip.IsPrivate() || ip.IsUnspecified() ||
		ip.IsMulticast() || ip.IsLinkLocalUnicast() || ip.IsLinkLocalMulticast() ||
		ip.IsInterfaceLocalMulticast() {
		return false
	}
	if v6 := ip.To16(); v6 != nil {
		// Deprecated IPv6 site-local fec0::/10 is not globally routable and is
		// not covered by IsPrivate/IsLinkLocal.
		if v6[0] == 0xfe && (v6[1]&0xc0) == 0xc0 {
			return false
		}
		switch {
		case v6[0] == 0x20 && v6[1] == 0x02:
			// 6to4: 2002:<embedded IPv4>::/48.
			return isPublicIPv4(net.IP(v6[2:6]))
		case v6[0] == 0x20 && v6[1] == 0x01 && v6[2] == 0x00 && v6[3] == 0x00:
			// Teredo: 2001:0000::/32.
			return false
		case embedsIPv4(v6):
			// NAT64 and IPv4-compatible transition addresses carry an IPv4
			// address in the low four bytes; judge that address instead of the
			// public-looking wrapper, so 64:ff9b::7f00:1 cannot reach loopback.
			return isPublicIPv4(net.IP(v6[12:16]))
		}
	}
	return true
}

// embedsIPv4 reports whether v6 is a transition address whose low four bytes
// hold an IPv4 address: the NAT64 well-known 64:ff9b::/96 and local-use
// 64:ff9b:1::/48 prefixes, and the deprecated IPv4-compatible ::/96 form.
func embedsIPv4(v6 net.IP) bool {
	if len(v6) != 16 {
		return false
	}
	if v6[0] == 0x00 && v6[1] == 0x64 && v6[2] == 0xff && v6[3] == 0x9b &&
		v6[4] == 0x00 && (v6[5] == 0x00 || v6[5] == 0x01) {
		return true
	}
	for i := 0; i < 12; i++ {
		if v6[i] != 0 {
			return false
		}
	}
	return true
}

// isPublicIPv4 reports whether the four-byte address is globally routable. It
// runs the full IPv4 policy, including loopback and private, because it is also
// applied to an IPv4 address embedded inside an IPv6 transition prefix.
func isPublicIPv4(v4 net.IP) bool {
	if len(v4) != 4 {
		return false
	}
	ip := net.IP(v4)
	if ip.IsLoopback() || ip.IsPrivate() || ip.IsUnspecified() ||
		ip.IsMulticast() || ip.IsLinkLocalUnicast() || ip.IsLinkLocalMulticast() ||
		ip.IsInterfaceLocalMulticast() {
		return false
	}
	switch {
	case v4[0] == 0:
		return false // 0.0.0.0/8 "this network"
	case v4[0] == 100 && v4[1] >= 64 && v4[1] <= 127:
		return false // 100.64.0.0/10 CGNAT
	case v4[0] == 192 && v4[1] == 0 && v4[2] == 0:
		return false // 192.0.0.0/24
	case v4[0] == 198 && (v4[1] == 18 || v4[1] == 19):
		return false // 198.18.0.0/15 benchmarking
	case v4[0] >= 240:
		return false // 240.0.0.0/4 reserved
	}
	return true
}
