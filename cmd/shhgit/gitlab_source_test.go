package main

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/trebor048/shhgot/core"
)

// installGitLabSeams points the GitLab source at a test server for one test.
func installGitLabSeams(t *testing.T, baseURL string, doer core.Doer) {
	t.Helper()
	prevBase, prevDoer := gitLabBaseURL, gitLabDoer
	gitLabBaseURL, gitLabDoer = baseURL, doer
	t.Cleanup(func() { gitLabBaseURL, gitLabDoer = prevBase, prevDoer })
}

// A public snippet whose raw body holds a key is downloaded through the
// allowlist and published once with source gitlab.
func TestRunGitLabPublishesMatch(t *testing.T) {
	hub := ensureWebHub()
	resetHubForTest(hub)
	newSearchTestSession(t, false)
	session.Config.Sources.GitLab.Enabled = searchBoolPtr(true)

	payload := "TOKEN=sk-" + strings.Repeat("a", 40)
	var listHits, rawHits int32
	var srv *httptest.Server
	srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/api/v4/snippets/public":
			atomic.AddInt32(&listHits, 1)
			w.Header().Set("Content-Type", "application/json")
			_ = json.NewEncoder(w).Encode([]map[string]any{{
				"id":        7,
				"title":     "leaky",
				"file_name": "creds.env",
				"raw_url":   srv.URL + "/api/v4/snippets/7/raw",
				"web_url":   srv.URL + "/snippets/7",
			}})
		case "/api/v4/snippets/7/raw":
			atomic.AddInt32(&rawHits, 1)
			_, _ = io.WriteString(w, payload)
		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(srv.Close)

	installGitLabSeams(t, srv.URL, srv.Client())

	runGitLab()

	waitForCond(t, 2*time.Second, func() bool { return hubMatchCount(t) == 1 })
	hub.matchesMutex.RLock()
	got := *hub.matches[0]
	hub.matchesMutex.RUnlock()

	if got.Source != "gitlab" {
		t.Fatalf("match source = %q, want gitlab", got.Source)
	}
	if got.File != "creds.env" {
		t.Fatalf("match file = %q, want creds.env", got.File)
	}
	if n := atomic.LoadInt32(&listHits); n != 1 {
		t.Fatalf("snippet listing saw %d requests, want 1", n)
	}
	if n := atomic.LoadInt32(&rawHits); n != 1 {
		t.Fatalf("raw download saw %d requests, want 1", n)
	}
}

// With the source disabled the start guard launches nothing, so the API sees
// zero requests and nothing is published.
func TestStartGitLabDisabledIssuesNoRequests(t *testing.T) {
	hub := ensureWebHub()
	resetHubForTest(hub)
	newSearchTestSession(t, false) // sources.gitlab.enabled absent

	var hits int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt32(&hits, 1)
		_ = json.NewEncoder(w).Encode([]map[string]any{})
	}))
	t.Cleanup(srv.Close)
	installGitLabSeams(t, srv.URL, srv.Client())

	if session.Config.GitLabEnabled() {
		t.Fatal("GitLab source must be off by default")
	}
	startGitLab()

	if n := atomic.LoadInt32(&hits); n != 0 {
		t.Fatalf("disabled GitLab source saw %d requests, want 0", n)
	}
	if n := hubMatchCount(t); n != 0 {
		t.Fatalf("disabled GitLab source published %d matches, want 0", n)
	}
}
