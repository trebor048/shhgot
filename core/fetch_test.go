package core

import (
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/trebor048/shhgot/core/httpclient"
)

// seamClock is an injectable clock whose sleep records the delay and advances
// time, so tests that exercise retry or rate-limit pacing never block.
type seamClock struct {
	mu     sync.Mutex
	t      time.Time
	sleeps []time.Duration
}

func newSeamClock() *seamClock {
	return &seamClock{t: time.Unix(1_700_000_000, 0)}
}

func (s *seamClock) now() time.Time {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.t
}

func (s *seamClock) sleep(d time.Duration) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if d <= 0 {
		return
	}
	s.sleeps = append(s.sleeps, d)
	s.t = s.t.Add(d)
}

func (s *seamClock) sleepsCopy() []time.Duration {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make([]time.Duration, len(s.sleeps))
	copy(out, s.sleeps)
	return out
}

func (s *seamClock) totalSleep() time.Duration {
	var total time.Duration
	for _, d := range s.sleepsCopy() {
		total += d
	}
	return total
}

func writeBody(w http.ResponseWriter, body string) {
	_, _ = io.WriteString(w, body)
}

// loopbackEntry returns the srv host:port allowlist entry. httptest servers
// listen on a random non-default port, which hostPortAllowed accepts only via
// an explicit host:port entry - a bare "127.0.0.1" no longer suffices.
func loopbackEntry(srvURL string) string {
	if u, err := url.Parse(srvURL); err == nil && u.Host != "" {
		return u.Host
	}
	return "127.0.0.1"
}

func TestFetchRejectsOffAllowlistHostWithoutRequest(t *testing.T) {
	var hits int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt32(&hits, 1)
		writeBody(w, "secret")
	}))
	defer srv.Close()

	f := &Fetcher{Doer: srv.Client(), AllowedHosts: []string{"raw.githubusercontent.com"}}
	if _, err := f.Fetch(srv.URL + "/leak.txt"); !errors.Is(err, errHostNotAllowed) {
		t.Fatalf("Fetch off-allowlist error = %v, want errHostNotAllowed", err)
	}
	if got := atomic.LoadInt32(&hits); got != 0 {
		t.Fatalf("off-allowlist fetch issued %d requests, want 0", got)
	}
}

func TestFetchRejectsSchemeUserinfoAndOddPorts(t *testing.T) {
	var hits int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt32(&hits, 1)
		writeBody(w, "hello")
	}))
	defer srv.Close()

	counting := &Fetcher{Doer: srv.Client(), AllowedHosts: []string{"raw.githubusercontent.com", loopbackEntry(srv.URL)}}

	// Non-default port on a bare-hostname entry is refused without a request.
	u, _ := url.Parse(srv.URL)
	oddPortURL := "https://" + u.Hostname() + ":8443/x"
	if _, err := counting.Fetch(oddPortURL); !errors.Is(err, errHostNotAllowed) {
		t.Fatalf("odd-port error = %v, want errHostNotAllowed", err)
	}

	// Credentials in the URL are refused, and must not reach the error text.
	evil := "https://user:pass@raw.githubusercontent.com/x"
	if _, err := counting.Fetch(evil); err == nil || strings.Contains(err.Error(), "pass") {
		t.Fatalf("userinfo error = %v, want rejection without credentials", err)
	}

	// Non-http schemes are refused.
	if _, err := counting.Fetch("gopher://raw.githubusercontent.com/x"); err == nil {
		t.Fatal("gopher URL returned nil error, want rejection")
	}
	if got := atomic.LoadInt32(&hits); got != 0 {
		t.Fatalf("rejected fetches issued %d requests, want 0", got)
	}

	// The same host:port named explicitly is accepted.
	explicit := &Fetcher{Doer: srv.Client(), AllowedHosts: []string{u.Host}}
	if _, err := explicit.Fetch(srv.URL + "/ok.txt"); err != nil {
		t.Fatalf("explicit host:port fetch error: %v", err)
	}
}

func TestFetchDownloadsAllowlistedHost(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		writeBody(w, "hello")
	}))
	defer srv.Close()

	f := &Fetcher{Doer: srv.Client(), AllowedHosts: []string{loopbackEntry(srv.URL)}}
	body, err := f.Fetch(srv.URL + "/ok.txt")
	if err != nil {
		t.Fatalf("Fetch returned error: %v", err)
	}
	if string(body) != "hello" {
		t.Fatalf("Fetch body = %q, want %q", string(body), "hello")
	}
}

func TestFetchRefusesCrossHostRedirect(t *testing.T) {
	var internalHits int32
	internal := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt32(&internalHits, 1)
		writeBody(w, "internal-secret")
	}))
	defer internal.Close()

	origin := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, internal.URL+"/secret", http.StatusFound)
	}))
	defer origin.Close()

	doer := httpclient.New(httpclient.Config{Timeout: time.Second})
	f := &Fetcher{Doer: doer, AllowedHosts: []string{loopbackEntry(origin.URL)}}
	body, err := f.Fetch(origin.URL + "/start")
	if err == nil {
		t.Fatalf("Fetch followed a cross-host redirect and returned %q, want error", string(body))
	}
	if body != nil {
		t.Fatalf("Fetch returned body %q alongside error %v, want nil body", string(body), err)
	}
	if got := atomic.LoadInt32(&internalHits); got != 0 {
		t.Fatalf("redirect target received %d requests, want 0", got)
	}
}

func TestFetchEnforcesSizeCap(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		writeBody(w, string(make([]byte, 100)))
	}))
	defer srv.Close()

	f := &Fetcher{Doer: srv.Client(), AllowedHosts: []string{loopbackEntry(srv.URL)}, MaxFileSize: 10}
	if _, err := f.Fetch(srv.URL + "/big.bin"); !errors.Is(err, errFileTooLarge) {
		t.Fatalf("Fetch oversized error = %v, want errFileTooLarge", err)
	}
}

func TestFetchRetriesOnRetryAfter(t *testing.T) {
	var hits int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if atomic.AddInt32(&hits, 1) == 1 {
			w.Header().Set("Retry-After", "2")
			w.WriteHeader(http.StatusServiceUnavailable)
			return
		}
		writeBody(w, "recovered")
	}))
	defer srv.Close()

	clk := newSeamClock()
	f := &Fetcher{
		Doer:         srv.Client(),
		AllowedHosts: []string{loopbackEntry(srv.URL)},
		MaxRetries:   1,
		Now:          clk.now,
		Sleep:        clk.sleep,
	}
	body, err := f.Fetch(srv.URL + "/x")
	if err != nil {
		t.Fatalf("Fetch returned error: %v", err)
	}
	if string(body) != "recovered" {
		t.Fatalf("Fetch body = %q, want %q", string(body), "recovered")
	}
	if got := atomic.LoadInt32(&hits); got != 2 {
		t.Fatalf("server observed %d requests, want 2", got)
	}
	if sleeps := clk.sleepsCopy(); len(sleeps) != 1 || sleeps[0] != 2*time.Second {
		t.Fatalf("recorded sleeps = %v, want [2s]", sleeps)
	}
}

func TestFetchDoesNotRetryPermanentStatus(t *testing.T) {
	var hits int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt32(&hits, 1)
		w.WriteHeader(http.StatusNotFound)
	}))
	defer srv.Close()

	clk := newSeamClock()
	f := &Fetcher{
		Doer:         srv.Client(),
		AllowedHosts: []string{loopbackEntry(srv.URL)},
		MaxRetries:   3,
		Now:          clk.now,
		Sleep:        clk.sleep,
	}
	if _, err := f.Fetch(srv.URL + "/missing"); err == nil {
		t.Fatal("Fetch 404 returned nil error, want error")
	}
	if got := atomic.LoadInt32(&hits); got != 1 {
		t.Fatalf("server observed %d requests, want 1", got)
	}
	if sleeps := clk.sleepsCopy(); len(sleeps) != 0 {
		t.Fatalf("recorded sleeps = %v, want none", sleeps)
	}
}

func TestDefaultFetchAllowlistIncludesGist(t *testing.T) {
	if !hostAllowed("gist.githubusercontent.com", defaultAllowedFetchHosts) {
		t.Fatal("defaultAllowedFetchHosts must include gist.githubusercontent.com")
	}
}

func TestCanonicalHostStripsPathAndScheme(t *testing.T) {
	tests := []struct {
		input    string
		expected string
	}{
		{"https://example.com/path", "example.com"},
		{"http://example.com:8080/path", "example.com"},
		{"example.com", "example.com"},
		{"example.com.", "example.com"},
		{"https://api.github.com/repos/owner/repo", "api.github.com"},
		{"gist.githubusercontent.com/user/gist", "gist.githubusercontent.com"},
	}
	for _, tc := range tests {
		got := canonicalHost(tc.input)
		if got != tc.expected {
			t.Errorf("canonicalHost(%q) = %q, want %q", tc.input, got, tc.expected)
		}
	}
}
