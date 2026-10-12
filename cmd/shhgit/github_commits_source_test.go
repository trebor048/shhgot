package main

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/trebor048/shhgot/core"
)

// installGitHubSearchSeams points the GitHub commit/issue search sources at a
// test server for one test.
func installGitHubSearchSeams(t *testing.T, baseURL string, doer core.Doer) {
	t.Helper()
	prevBase, prevDoer := gitHubSearchBaseURL, gitHubSearchDoer
	gitHubSearchBaseURL, gitHubSearchDoer = baseURL, doer
	t.Cleanup(func() { gitHubSearchBaseURL, gitHubSearchDoer = prevBase, prevDoer })
}

// A commit message that holds a key is published once with source
// github-commits.
func TestRunGitHubCommitsPublishesMatch(t *testing.T) {
	hub := ensureWebHub()
	resetHubForTest(hub)
	s := newSearchTestSession(t, false)
	s.Config.Sources.GitHubCommits.Enabled = searchBoolPtr(true)
	s.Config.Sources.GitHubCommits.Queries = []string{"OPENAI_API_KEY"}

	payload := "OPENAI_API_KEY=sk-" + strings.Repeat("a", 40)
	var hits int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt32(&hits, 1)
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{
			"total_count":        1,
			"incomplete_results": false,
			"items": []map[string]any{{
				"sha":      "abc123",
				"html_url": "https://github.com/acme/repo/commit/abc123",
				"commit": map[string]any{
					"message": payload,
					"author":  map[string]any{"date": "2024-01-01T00:00:00Z"},
				},
				"repository": map[string]any{"full_name": "acme/repo"},
			}},
		})
	}))
	t.Cleanup(srv.Close)

	installGitHubSearchSeams(t, srv.URL, srv.Client())

	runGitHubCommits()

	waitForCond(t, 2*time.Second, func() bool { return hubMatchCount(t) == 1 })
	hub.matchesMutex.RLock()
	got := *hub.matches[0]
	hub.matchesMutex.RUnlock()

	if got.Source != "github-commits" {
		t.Fatalf("match source = %q, want github-commits", got.Source)
	}
	if got.Signature != "Test Credential" {
		t.Fatalf("match signature = %q, want Test Credential", got.Signature)
	}
	if n := atomic.LoadInt32(&hits); n != 1 {
		t.Fatalf("GitHub commit search saw %d requests, want 1", n)
	}
}

// With the source disabled the start guard launches nothing, so the API sees
// zero requests and nothing is published.
func TestStartGitHubCommitsDisabledIssuesNoRequests(t *testing.T) {
	hub := ensureWebHub()
	resetHubForTest(hub)
	newSearchTestSession(t, false) // sources.github_commits.enabled absent

	var hits int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt32(&hits, 1)
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{})
	}))
	t.Cleanup(srv.Close)
	installGitHubSearchSeams(t, srv.URL, srv.Client())

	if session.Config.GitHubCommitsEnabled() {
		t.Fatal("GitHub commit source must be off by default")
	}
	startGitHubCommits()

	if n := atomic.LoadInt32(&hits); n != 0 {
		t.Fatalf("disabled GitHub commit source saw %d requests, want 0", n)
	}
	if n := hubMatchCount(t); n != 0 {
		t.Fatalf("disabled GitHub commit source published %d matches, want 0", n)
	}
}
