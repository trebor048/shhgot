package main

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

// An issue body that holds a key is published once with source github-issues.
func TestRunGitHubIssuesPublishesMatch(t *testing.T) {
	hub := ensureWebHub()
	resetHubForTest(hub)
	s := newSearchTestSession(t, false)
	s.Config.Sources.GitHubIssues.Enabled = searchBoolPtr(true)
	s.Config.Sources.GitHubIssues.Queries = []string{"OPENAI_API_KEY"}

	payload := "OPENAI_API_KEY=sk-" + strings.Repeat("a", 40)
	var hits int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt32(&hits, 1)
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{
			"total_count":        1,
			"incomplete_results": false,
			"items": []map[string]any{{
				"html_url":       "https://github.com/acme/repo/issues/7",
				"title":          "leaked key",
				"body":           payload,
				"repository_url": "https://api.github.com/repos/acme/repo",
				"user":           map[string]any{"login": "octocat"},
				"created_at":     "2024-01-01T00:00:00Z",
			}},
		})
	}))
	t.Cleanup(srv.Close)

	installGitHubSearchSeams(t, srv.URL, srv.Client())

	runGitHubIssues()

	waitForCond(t, 2*time.Second, func() bool { return hubMatchCount(t) == 1 })
	hub.matchesMutex.RLock()
	got := *hub.matches[0]
	hub.matchesMutex.RUnlock()

	if got.Source != "github-issues" {
		t.Fatalf("match source = %q, want github-issues", got.Source)
	}
	if got.Signature != "Test Credential" {
		t.Fatalf("match signature = %q, want Test Credential", got.Signature)
	}
	if n := atomic.LoadInt32(&hits); n != 1 {
		t.Fatalf("GitHub issue search saw %d requests, want 1", n)
	}
}

// With the source disabled the start guard launches nothing, so the API sees
// zero requests and nothing is published.
func TestStartGitHubIssuesDisabledIssuesNoRequests(t *testing.T) {
	hub := ensureWebHub()
	resetHubForTest(hub)
	newSearchTestSession(t, false) // sources.github_issues.enabled absent

	var hits int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt32(&hits, 1)
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{})
	}))
	t.Cleanup(srv.Close)
	installGitHubSearchSeams(t, srv.URL, srv.Client())

	if session.Config.GitHubIssuesEnabled() {
		t.Fatal("GitHub issue source must be off by default")
	}
	startGitHubIssues()

	if n := atomic.LoadInt32(&hits); n != 0 {
		t.Fatalf("disabled GitHub issue source saw %d requests, want 0", n)
	}
	if n := hubMatchCount(t); n != 0 {
		t.Fatalf("disabled GitHub issue source published %d matches, want 0", n)
	}
}
