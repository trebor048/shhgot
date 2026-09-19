package core

import (
	"fmt"
	"net"
	"net/http"
	"time"

	"github.com/didip/tollbooth"
	"github.com/didip/tollbooth/limiter"
)

// RateLimiter wraps tollbooth limiter
type RateLimiter struct {
	limiter *limiter.Limiter
}

// NewRateLimiter creates a new rate limiter
// requestsPerSecond: max requests per second
// burst: max burst requests
func NewRateLimiter(requestsPerSecond float64, burst int) *RateLimiter {
	lim := tollbooth.NewLimiter(requestsPerSecond, &limiter.ExpirableOptions{
		DefaultExpirationTTL: time.Hour,
	})

	// Per-IP rate limiting
	lim.SetIPLookups([]string{"RemoteAddr", "X-Forwarded-For", "X-Real-IP"})
	lim.SetHeader("X-Custom-Header", []string{""})
	lim.SetMessage(`{"error": "You have been rate limited"}`)

	return &RateLimiter{limiter: lim}
}

// Middleware returns an HTTP middleware for rate limiting
func (rl *RateLimiter) Middleware(next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		httpError := tollbooth.LimitByRequest(rl.limiter, w, r)
		if httpError != nil {
			w.Header().Set("Content-Type", "application/json")
			w.Header().Set("X-RateLimit-Limit", "100")
			w.Header().Set("X-RateLimit-Remaining", "0")
			w.Header().Set("X-RateLimit-Reset", fmt.Sprintf("%d", time.Now().Add(time.Minute).Unix()))
			w.WriteHeader(http.StatusTooManyRequests)
			fmt.Fprint(w, `{"error": "rate limited"}`)
			return
		}

		// Add rate limit headers
		w.Header().Set("X-RateLimit-Limit", "100")
		w.Header().Set("X-RateLimit-Remaining", "unlimited")
		w.Header().Set("X-RateLimit-Reset", fmt.Sprintf("%d", time.Now().Add(time.Minute).Unix()))

		next(w, r)
	}
}

// getClientIP extracts the client IP address from the request
func getClientIP(r *http.Request) string {
	// Check X-Forwarded-For header (for reverse proxies)
	if xff := r.Header.Get("X-Forwarded-For"); xff != "" {
		ips := xff
		if i := len(ips) - 1; i >= 0 && ips[i] == ' ' {
			ips = ips[:i]
		}
		return ips
	}

	// Check X-Real-IP header
	if xri := r.Header.Get("X-Real-IP"); xri != "" {
		return xri
	}

	// Use RemoteAddr
	ip, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		return r.RemoteAddr
	}

	return ip
}
