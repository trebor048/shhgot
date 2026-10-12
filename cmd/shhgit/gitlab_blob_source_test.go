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

// newGitLabBlobTestSession installs a session with one contents signature whose
// literal anchor can drive a derived blob query, plus the GitLab blob source on.
func newGitLabBlobTestSession(t *testing.T) *core.Session {
	t.Helper()
	s := newSearchTestSession(t, false)
	s.Config.Signatures = []core.ConfigSignature{{
		Name:     "Test Credential",
		Part:     core.PartContents,
		Regex:    `OPENAI_API_KEY\s*=\s*sk-[A-Za-z0-9]{20,}`,
		Priority: 3,
		Color:    "#10A37F",
	}}
	s.Signatures = core.GetSignatures(s)
	s.Config.Sources.GitLab.SearchEnabled = searchBoolPtr(true)
	s.Config.Sources.GitLab.Token = "glpat-test"
	return s
}

// A GitLab blob whose data holds a key is published once with source gitlab and
// linked through the resolved project path.
func TestRunGitLabBlobsPublishesMatch(t *testing.T) {
	hub := ensureWebHub()
	resetHubForTest(hub)
	newGitLabBlobTestSession(t)

	payload := "OPENAI_API_KEY=sk-" + strings.Repeat("a", 40)
	var searchHits, projectHits int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/api/v4/search":
			atomic.AddInt32(&searchHits, 1)
			w.Header().Set("Content-Type", "application/json")
			if r.URL.Query().Get("page") != "1" {
				_, _ = io.WriteString(w, "[]")
				return
			}
			_ = json.NewEncoder(w).Encode([]map[string]any{{
				"path":       "config/creds.env",
				"filename":   "creds.env",
				"ref":        "main",
				"data":       payload,
				"startline":  1,
				"project_id": 42,
			}})
		case "/api/v4/projects/42":
			atomic.AddInt32(&projectHits, 1)
			w.Header().Set("Content-Type", "application/json")
			_ = json.NewEncoder(w).Encode(map[string]any{"path_with_namespace": "acme/repo"})
		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(srv.Close)

	installGitLabSeams(t, srv.URL, srv.Client())

	if !runGitLabBlobs() {
		t.Fatal("runGitLabBlobs must keep the source running when the search succeeds")
	}

	waitForCond(t, 2*time.Second, func() bool { return hubMatchCount(t) == 1 })
	hub.matchesMutex.RLock()
	got := *hub.matches[0]
	hub.matchesMutex.RUnlock()

	if got.Source != "gitlab" {
		t.Fatalf("match source = %q, want gitlab", got.Source)
	}
	if got.File != "config/creds.env" {
		t.Fatalf("match file = %q, want config/creds.env", got.File)
	}
	if !strings.Contains(got.URL, "/acme/repo/-/blob/main/config/creds.env") {
		t.Fatalf("match url = %q, want a resolved GitLab blob URL", got.URL)
	}
	if n := atomic.LoadInt32(&searchHits); n == 0 {
		t.Fatal("GitLab blob search issued no requests")
	}
	if n := atomic.LoadInt32(&projectHits); n != 1 {
		t.Fatalf("project lookup saw %d requests, want 1 (cached)", n)
	}
}

// A tier refusal must stop the source permanently after one warning.
func TestRunGitLabBlobsStopsOnUnavailable(t *testing.T) {
	hub := ensureWebHub()
	resetHubForTest(hub)
	newGitLabBlobTestSession(t)

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusForbidden)
		_, _ = io.WriteString(w, `{"message":"Advanced search is not enabled"}`)
	}))
	t.Cleanup(srv.Close)
	installGitLabSeams(t, srv.URL, srv.Client())

	if runGitLabBlobs() {
		t.Fatal("runGitLabBlobs must report stop when GitLab refuses the blobs scope")
	}
	if n := hubMatchCount(t); n != 0 {
		t.Fatalf("unavailable GitLab blob search published %d matches, want 0", n)
	}
}

// With the source disabled the start guard launches nothing, so the API sees
// zero requests and nothing is published.
func TestStartGitLabBlobsDisabledIssuesNoRequests(t *testing.T) {
	hub := ensureWebHub()
	resetHubForTest(hub)
	newSearchTestSession(t, false) // sources.gitlab.search_enabled absent

	var hits int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt32(&hits, 1)
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode([]map[string]any{})
	}))
	t.Cleanup(srv.Close)
	installGitLabSeams(t, srv.URL, srv.Client())

	if session.Config.GitLabBlobsEnabled() {
		t.Fatal("GitLab blob source must be off by default")
	}
	startGitLabBlobs()

	if n := atomic.LoadInt32(&hits); n != 0 {
		t.Fatalf("disabled GitLab blob source saw %d requests, want 0", n)
	}
	if n := hubMatchCount(t); n != 0 {
		t.Fatalf("disabled GitLab blob source published %d matches, want 0", n)
	}
}
