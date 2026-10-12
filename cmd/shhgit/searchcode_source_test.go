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

// installSearchCodeSeams points the searchcode source at a test server for one
// test.
func installSearchCodeSeams(t *testing.T, baseURL string, doer core.Doer) {
	t.Helper()
	prevBase, prevDoer := searchCodeBaseURL, searchCodeDoer
	searchCodeBaseURL, searchCodeDoer = baseURL, doer
	t.Cleanup(func() { searchCodeBaseURL, searchCodeDoer = prevBase, prevDoer })
}

// A code-search result whose reconstructed content holds a key is published
// once with source searchcode.
func TestRunSearchCodePublishesMatch(t *testing.T) {
	hub := ensureWebHub()
	resetHubForTest(hub)
	s := newSearchTestSession(t, false)
	s.Config.Sources.SearchCode.Enabled = searchBoolPtr(true)
	s.Config.Sources.SearchCode.Queries = []string{"OPENAI_API_KEY"}
	s.Config.Sources.SearchCode.MaxPages = searchIntPtr(1)

	payload := "OPENAI_API_KEY=sk-" + strings.Repeat("a", 40)
	var searchHits int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt32(&searchHits, 1)
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, `{"results":[{"id":101,"filename":"a.env","repo":"acme/app",`+
			`"url":"https://searchcode.com/codesearch/view/101/","lines":{"1":`+strconv.Quote(payload)+`}}]}`)
	}))
	t.Cleanup(srv.Close)

	installSearchCodeSeams(t, srv.URL, srv.Client())

	runSearchCode()

	waitForCond(t, 2*time.Second, func() bool { return hubMatchCount(t) == 1 })
	hub.matchesMutex.RLock()
	got := *hub.matches[0]
	hub.matchesMutex.RUnlock()

	if got.Source != "searchcode" {
		t.Fatalf("match source = %q, want searchcode", got.Source)
	}
	if got.Signature != "Test Credential" {
		t.Fatalf("match signature = %q, want Test Credential", got.Signature)
	}
	if got.File != "a.env" {
		t.Fatalf("match file = %q, want a.env", got.File)
	}
	if n := atomic.LoadInt32(&searchHits); n != 1 {
		t.Fatalf("searchcode API saw %d requests, want 1", n)
	}
}

// With the source disabled the start guard launches nothing, so the API sees
// zero requests and nothing is published.
func TestStartSearchCodeDisabledIssuesNoRequests(t *testing.T) {
	hub := ensureWebHub()
	resetHubForTest(hub)
	newSearchTestSession(t, false) // sources.searchcode.enabled absent

	var hits int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt32(&hits, 1)
		_, _ = io.WriteString(w, `{"results":[]}`)
	}))
	t.Cleanup(srv.Close)
	installSearchCodeSeams(t, srv.URL, srv.Client())

	if session.Config.SearchCodeEnabled() {
		t.Fatal("searchcode source must be off by default")
	}
	startSearchCode()

	if n := atomic.LoadInt32(&hits); n != 0 {
		t.Fatalf("disabled searchcode source saw %d requests, want 0", n)
	}
	if n := hubMatchCount(t); n != 0 {
		t.Fatalf("disabled searchcode source published %d matches, want 0", n)
	}
}
