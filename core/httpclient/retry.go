package httpclient

import (
	"math"
	"net/http"
	"strconv"
	"strings"
	"time"
)

// retryableStatuses are the transient HTTP statuses worth another attempt.
// Permanent client errors (notably 400, 401, 403 and 404) are deliberately
// absent and are never retried.
var retryableStatuses = map[int]bool{
	http.StatusRequestTimeout:      true, // 408
	http.StatusTooManyRequests:     true, // 429
	http.StatusInternalServerError: true, // 500
	http.StatusBadGateway:          true, // 502
	http.StatusServiceUnavailable:  true, // 503
	http.StatusGatewayTimeout:      true, // 504
}

func isRetryableStatus(code int) bool { return retryableStatuses[code] }

// breakerFailureStatuses are the received-statuses that indicate the host
// itself is unhealthy (as opposed to a request the host rejected).
var breakerFailureStatuses = map[int]bool{
	http.StatusBadGateway:         true, // 502
	http.StatusServiceUnavailable: true, // 503
	http.StatusGatewayTimeout:     true, // 504
}

func isBreakerFailureStatus(code int) bool { return breakerFailureStatuses[code] }

// maxRetryAfter caps a server-advertised Retry-After. A hostile or
// misconfigured endpoint could otherwise stall the scanner for an arbitrary
// duration (for example Retry-After: 86400); anything longer is clamped to
// this ceiling.
const maxRetryAfter = 5 * time.Minute

// ParseRetryAfter parses a Retry-After style header value and returns how long
// the caller should wait. Both forms the HTTP specification allows are
// accepted: delay-seconds (integer or, for X-RateLimit-Reset-After, a
// fractional number) and an HTTP-date, which is measured against now. An
// empty, malformed or non-positive value returns ok=false so the caller can
// fall back to exponential backoff. The returned delay is never longer than
// maxRetryAfter.
func ParseRetryAfter(value string, now time.Time) (time.Duration, bool) {
	value = strings.TrimSpace(value)
	if value == "" {
		return 0, false
	}
	if seconds, err := strconv.Atoi(value); err == nil {
		if seconds <= 0 {
			return 0, false
		}
		// Bound before the multiply: a huge value would overflow the
		// time.Duration conversion into a negative delay.
		if seconds > int(maxRetryAfter/time.Second) {
			return maxRetryAfter, true
		}
		return time.Duration(seconds) * time.Second, true
	}
	if seconds, err := strconv.ParseFloat(value, 64); err == nil {
		if seconds <= 0 || math.IsNaN(seconds) {
			return 0, false
		}
		if seconds > maxRetryAfter.Seconds() {
			return maxRetryAfter, true
		}
		return time.Duration(seconds * float64(time.Second)), true
	}
	if when, err := http.ParseTime(value); err == nil {
		if delay := when.Sub(now); delay > 0 {
			return clampRetryAfter(delay), true
		}
	}
	return 0, false
}

// clampRetryAfter bounds an advertised delay to maxRetryAfter.
func clampRetryAfter(d time.Duration) time.Duration {
	if d > maxRetryAfter {
		return maxRetryAfter
	}
	return d
}
