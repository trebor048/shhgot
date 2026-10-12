package httpclient

import (
	"net"
	"testing"
	"time"
)

// TestParseRetryAfterLargeValuesAreCappedNotNegative verifies a hostile or
// misconfigured Retry-After cannot overflow into a negative (instant) delay.
func TestParseRetryAfterLargeValuesAreCappedNotNegative(t *testing.T) {
	now := time.Now()
	for _, v := range []string{"10000000000", "99999999999999999999", "1e300"} {
		d, ok := ParseRetryAfter(v, now)
		if !ok {
			t.Fatalf("ParseRetryAfter(%q) ok = false, want true", v)
		}
		if d != maxRetryAfter {
			t.Fatalf("ParseRetryAfter(%q) = %s, want %s", v, d, maxRetryAfter)
		}
	}
}

// TestBackoffDoesNotOverflowWithoutMax verifies the doubling is bounded even
// when BackoffMax is left at its zero value.
func TestBackoffDoesNotOverflowWithoutMax(t *testing.T) {
	c := New(Config{BackoffInitial: time.Second})
	if d := c.backoff(200); d <= 0 {
		t.Fatalf("backoff(200) = %s, want a positive delay", d)
	}
}

// TestSafeURLRejectsTrailingDotInternalSuffix verifies a trailing root dot does
// not bypass the internal-hostname denylist.
func TestSafeURLRejectsTrailingDotInternalSuffix(t *testing.T) {
	if err := SafeURL("https://db.internal./"); err == nil {
		t.Fatal("SafeURL accepted an internal suffix with a trailing dot")
	}
	if err := SafeURL("http://localhost./"); err != nil {
		t.Fatalf("SafeURL(localhost.) = %v, want nil", err)
	}
}

// TestIsPublicIPRejectsSiteLocalIPv6 verifies deprecated fec0::/10 is not
// treated as globally routable.
func TestIsPublicIPRejectsSiteLocalIPv6(t *testing.T) {
	if isPublicIP(net.ParseIP("fec0::1")) {
		t.Fatal("fec0::/10 site-local classified as public")
	}
}
