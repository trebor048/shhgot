package main

import (
	"io"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/trebor048/shhgot/core"
)

// installSourcegraphSeams points the Sourcegraph source at a test server for
// one test.
func installSourcegraphSeams(t *testing.T, baseURL string, doer core.Doer) {
	t.Helper()
	prevBase, prevDoer := sourcegraphBaseURL, sourcegraphDoer
	sourcegraphBaseURL, sourcegraphDoer = baseURL, doer
	t.Cleanup(func() { sourcegraphBaseURL, sourcegraphDoer = prevBase, prevDoer })
}

// A matched preview line that holds a key is published once with source
// sourcegraph.
func TestRunSourcegraphPublishesMatch(t *testing.T) {
	hub := ensureWebHub()
	resetHubForTest(hub)
	s := newSearchTestSession(t, false)
	s.Config.Sources.Sourcegraph.Enabled = searchBoolPtr(true)
	s.Config.Sources.Sourcegraph.Queries = []string{"OPENAI_API_KEY"}

	payload := "OPENAI_API_KEY=sk-" + strings.Repeat("a", 40)
	var hits int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt32(&hits, 1)
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = io.WriteString(w, "event: matches\ndata: [{\"type\":\"content\","+
			"\"repository\":\"github.com/owner/repo\",\"path\":\"a/b.py\",\"lineMatches\":[{\"preview\":"+
			strconv.Quote(payload)+",\"lineNumber\":1}]}]\n\n")
	}))
	t.Cleanup(srv.Close)

	installSourcegraphSeams(t, srv.URL, srv.Client())

	runSourcegraph()

	waitForCond(t, 2*time.Second, func() bool { return hubMatchCount(t) == 1 })
	hub.matchesMutex.RLock()
	got := *hub.matches[0]
	hub.matchesMutex.RUnlock()

	if got.Source != "sourcegraph" {
		t.Fatalf("match source = %q, want sourcegraph", got.Source)
	}
	if got.Signature != "Test Credential" {
		t.Fatalf("match signature = %q, want Test Credential", got.Signature)
	}
	if got.File != "a/b.py" {
		t.Fatalf("match file = %q, want a/b.py", got.File)
	}
	if n := atomic.LoadInt32(&hits); n != 1 {
		t.Fatalf("Sourcegraph API saw %d requests, want 1", n)
	}
}

// With the source disabled the start guard launches nothing, so the API sees
// zero requests and nothing is published.
func TestStartSourcegraphDisabledIssuesNoRequests(t *testing.T) {
	hub := ensureWebHub()
	resetHubForTest(hub)
	newSearchTestSession(t, false) // sources.sourcegraph.enabled absent

	var hits int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt32(&hits, 1)
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = io.WriteString(w, "event: done\ndata: \n\n")
	}))
	t.Cleanup(srv.Close)
	installSourcegraphSeams(t, srv.URL, srv.Client())

	if session.Config.SourcegraphEnabled() {
		t.Fatal("Sourcegraph source must be off by default")
	}
	startSourcegraph()

	if n := atomic.LoadInt32(&hits); n != 0 {
		t.Fatalf("disabled Sourcegraph source saw %d requests, want 0", n)
	}
	if n := hubMatchCount(t); n != 0 {
		t.Fatalf("disabled Sourcegraph source published %d matches, want 0", n)
	}
}
