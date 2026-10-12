package core

import (
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

// sourcegraphMatchesFrame is a well-formed matches frame carrying two lines of
// one content match.
const sourcegraphMatchesFrame = "event: matches\n" +
	"data: [{\"type\":\"content\",\"repository\":\"github.com/owner/repo\",\"path\":\"a/b.py\"," +
	"\"lineMatches\":[{\"preview\":\"OPENAI_API_KEY=sk-abc\",\"lineNumber\":12,\"offsetAndLengths\":[[0,14]]}," +
	"{\"preview\":\"AWS_SECRET=xyz\",\"lineNumber\":42,\"offsetAndLengths\":[[0,10]]}]}]\n\n"

func TestSourcegraphSearchParsesSSEFrames(t *testing.T) {
	var mu sync.Mutex
	var rawQuery, accept, auth, ua string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		rawQuery = r.URL.RawQuery
		accept = r.Header.Get("Accept")
		auth = r.Header.Get("Authorization")
		ua = r.Header.Get("User-Agent")
		mu.Unlock()
		w.Header().Set("Content-Type", "text/event-stream")
		writeBody(w, "event: progress\ndata: {\"done\":false}\n\n")
		writeBody(w, sourcegraphMatchesFrame)
		writeBody(w, "event: done\ndata: \n\n")
	}))
	defer srv.Close()

	c := NewSourcegraphClient(srv.Client(), srv.URL)
	got, err := c.Search("OPENAI_API_KEY", 10)
	if err != nil {
		t.Fatalf("Search error: %v", err)
	}
	if len(got) != 2 {
		t.Fatalf("got %d matches, want 2: %+v", len(got), got)
	}
	want0 := SourcegraphMatch{Repository: "github.com/owner/repo", Path: "a/b.py", Preview: "OPENAI_API_KEY=sk-abc", Line: 12}
	if got[0] != want0 {
		t.Fatalf("got[0] = %+v, want %+v", got[0], want0)
	}
	want1 := SourcegraphMatch{Repository: "github.com/owner/repo", Path: "a/b.py", Preview: "AWS_SECRET=xyz", Line: 42}
	if got[1] != want1 {
		t.Fatalf("got[1] = %+v, want %+v", got[1], want1)
	}

	mu.Lock()
	q, _ := url.ParseQuery(rawQuery)
	gotAccept, gotAuth, gotUA := accept, auth, ua
	mu.Unlock()
	if q.Get("q") != "OPENAI_API_KEY" {
		t.Fatalf("q = %q, want OPENAI_API_KEY", q.Get("q"))
	}
	if q.Get("v") != "V3" {
		t.Fatalf("v = %q, want V3", q.Get("v"))
	}
	if q.Get("t") != "standard" {
		t.Fatalf("t = %q, want standard", q.Get("t"))
	}
	if q.Get("display") != "10" {
		t.Fatalf("display = %q, want 10", q.Get("display"))
	}
	if gotAccept != "text/event-stream" {
		t.Fatalf("Accept = %q, want text/event-stream", gotAccept)
	}
	if gotUA != "shhgit2" {
		t.Fatalf("User-Agent = %q, want shhgit2", gotUA)
	}
	if gotAuth != "" {
		t.Fatalf("Authorization = %q, want empty without a token", gotAuth)
	}
}

func TestSourcegraphSearchSendsToken(t *testing.T) {
	var mu sync.Mutex
	var auth string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		auth = r.Header.Get("Authorization")
		mu.Unlock()
		w.Header().Set("Content-Type", "text/event-stream")
		writeBody(w, sourcegraphMatchesFrame)
	}))
	defer srv.Close()

	c := NewSourcegraphClient(srv.Client(), srv.URL)
	c.Token = "sgp_secret"
	if _, err := c.Search("q", 1); err != nil {
		t.Fatalf("Search error: %v", err)
	}
	mu.Lock()
	gotAuth := auth
	mu.Unlock()
	if gotAuth != "token sgp_secret" {
		t.Fatalf("Authorization = %q, want %q", gotAuth, "token sgp_secret")
	}
}

func TestSourcegraphSearchCapsAtMaxResults(t *testing.T) {
	var calls int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt32(&calls, 1)
		w.Header().Set("Content-Type", "text/event-stream")
		writeBody(w, sourcegraphMatchesFrame)
	}))
	defer srv.Close()

	c := NewSourcegraphClient(srv.Client(), srv.URL)
	got, err := c.Search("q", 1)
	if err != nil {
		t.Fatalf("Search error: %v", err)
	}
	if len(got) != 1 {
		t.Fatalf("got %d matches, want 1: %+v", len(got), got)
	}
	if got[0].Line != 12 {
		t.Fatalf("got[0].Line = %d, want 12", got[0].Line)
	}
	if n := atomic.LoadInt32(&calls); n != 1 {
		t.Fatalf("server observed %d requests, want 1", n)
	}
}

func TestSourcegraphSearchIgnoresMalformedFrames(t *testing.T) {
	body := "event: matches\ndata: {not json}\n\n" +
		"event: alert\ndata: {\"title\":\"rate limited\"}\n\n" +
		"data: [{\"repository\":\"github.com/o/r\",\"path\":\"x\",\"lineMatches\":[{\"preview\":\"p\",\"lineNumber\":1}]}]\n\n" +
		"event: matches\ndata: [{\"type\":\"path\",\"repository\":\"github.com/o/r\",\"path\":\"dir/file.go\"}]\n\n" +
		"event: matches\ndata: [{\"type\":\"content\",\"path\":\"no-repo.py\",\"lineMatches\":[{\"preview\":\"p\",\"lineNumber\":1}]}]\n\n" +
		"event: matches\ndata: [{\"type\":\"content\",\"repository\":\"github.com/o/r\",\"path\":\"ok.go\",\"lineMatches\":[{\"preview\":\"FOUND\",\"lineNumber\":7}]}]\n\n"

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		writeBody(w, body)
	}))
	defer srv.Close()

	c := NewSourcegraphClient(srv.Client(), srv.URL)
	got, err := c.Search("q", 10)
	if err != nil {
		t.Fatalf("Search error: %v", err)
	}
	if len(got) != 1 {
		t.Fatalf("got %d matches, want only the well-formed one: %+v", len(got), got)
	}
	want := SourcegraphMatch{Repository: "github.com/o/r", Path: "ok.go", Preview: "FOUND", Line: 7}
	if got[0] != want {
		t.Fatalf("got[0] = %+v, want %+v", got[0], want)
	}
}

func TestSourcegraphSearchNon200Errors(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
	}))
	defer srv.Close()

	c := NewSourcegraphClient(srv.Client(), srv.URL)
	got, err := c.Search("q", 10)
	if err == nil {
		t.Fatal("Search on a 500 returned nil error, want error")
	}
	if got != nil {
		t.Fatalf("Search on a 500 returned %+v, want nil matches", got)
	}
}

func TestSourcegraphSearchEmptyQueryErrors(t *testing.T) {
	var calls int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt32(&calls, 1)
	}))
	defer srv.Close()

	c := NewSourcegraphClient(srv.Client(), srv.URL)
	if _, err := c.Search("   ", 10); err == nil {
		t.Fatal("Search with a blank query returned nil error, want error")
	}
	if n := atomic.LoadInt32(&calls); n != 0 {
		t.Fatalf("blank query issued %d requests, want 0", n)
	}
}

func TestSourcegraphRateLimitSleepsAndRetries(t *testing.T) {
	for _, status := range []int{http.StatusTooManyRequests, http.StatusForbidden} {
		t.Run(http.StatusText(status), func(t *testing.T) {
			var calls int32
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if atomic.AddInt32(&calls, 1) == 1 {
					w.Header().Set("Retry-After", "5")
					w.WriteHeader(status)
					return
				}
				w.Header().Set("Content-Type", "text/event-stream")
				writeBody(w, sourcegraphMatchesFrame)
			}))
			defer srv.Close()

			clk := newSeamClock()
			c := &SourcegraphClient{Doer: srv.Client(), BaseURL: srv.URL, Now: clk.now, Sleep: clk.sleep}
			got, err := c.Search("q", 10)
			if err != nil {
				t.Fatalf("Search error: %v", err)
			}
			if len(got) != 2 {
				t.Fatalf("got %d matches, want 2", len(got))
			}
			if sleeps := clk.sleepsCopy(); len(sleeps) != 1 || sleeps[0] != 5*time.Second {
				t.Fatalf("recorded sleeps = %v, want [5s]", sleeps)
			}
			if n := atomic.LoadInt32(&calls); n != 2 {
				t.Fatalf("server observed %d requests, want 2", n)
			}
		})
	}
}

func TestSourcegraphRateLimitExhaustsAndErrors(t *testing.T) {
	var calls int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt32(&calls, 1)
		w.Header().Set("Retry-After", "3")
		w.WriteHeader(http.StatusTooManyRequests)
	}))
	defer srv.Close()

	clk := newSeamClock()
	c := &SourcegraphClient{Doer: srv.Client(), BaseURL: srv.URL, Now: clk.now, Sleep: clk.sleep}
	got, err := c.Search("q", 10)
	if err == nil {
		t.Fatal("Search on a persistent 429 returned nil error, want error")
	}
	if got != nil {
		t.Fatalf("Search returned %+v, want nil matches", got)
	}
	if want := int32(sourcegraphRateLimitRetries + 1); atomic.LoadInt32(&calls) != want {
		t.Fatalf("server observed %d requests, want %d", calls, want)
	}
	if sleeps := clk.sleepsCopy(); len(sleeps) != sourcegraphRateLimitRetries {
		t.Fatalf("recorded sleeps = %v, want %d entries", sleeps, sourcegraphRateLimitRetries)
	}
}

func TestNewSourcegraphClientDefaultsBase(t *testing.T) {
	c := NewSourcegraphClient(http.DefaultClient, "  ")
	if c.BaseURL != defaultSourcegraphBaseURL {
		t.Fatalf("BaseURL = %q, want %q", c.BaseURL, defaultSourcegraphBaseURL)
	}
	if c.Doer == nil {
		t.Fatal("Doer = nil, want the supplied client")
	}

	trimmed := NewSourcegraphClient(http.DefaultClient, "https://example.test/")
	if trimmed.BaseURL != "https://example.test" {
		t.Fatalf("BaseURL = %q, want trailing slash trimmed", trimmed.BaseURL)
	}
}

func TestSourcegraphSearchNilDoerErrors(t *testing.T) {
	c := &SourcegraphClient{BaseURL: "https://example.test"}
	if _, err := c.Search("q", 10); err == nil || !strings.Contains(err.Error(), "Doer is nil") {
		t.Fatalf("Search with a nil Doer error = %v, want a Doer-is-nil error", err)
	}
}
