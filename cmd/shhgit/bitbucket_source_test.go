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

// installBitbucketSeams points the Bitbucket source at a test server for one
// test.
func installBitbucketSeams(t *testing.T, baseURL string, doer core.Doer) {
	t.Helper()
	prevBase, prevDoer := bitbucketBaseURL, bitbucketDoer
	bitbucketBaseURL, bitbucketDoer = baseURL, doer
	t.Cleanup(func() { bitbucketBaseURL, bitbucketDoer = prevBase, prevDoer })
}

// A Bitbucket file body that holds a key is published once with source
// bitbucket and linked to the file's raw URL.
func TestRunBitbucketPublishesMatch(t *testing.T) {
	hub := ensureWebHub()
	resetHubForTest(hub)
	s := newSearchTestSession(t, false)
	s.Config.Sources.Bitbucket.Enabled = searchBoolPtr(true)
	s.Config.Sources.Bitbucket.Workspaces = []string{"acme"}

	payload := "OPENAI_API_KEY=sk-" + strings.Repeat("a", 40)
	var listHits, fileHits, rawHits int32
	var srv *httptest.Server
	srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch {
		case r.URL.Path == "/2.0/repositories/acme":
			atomic.AddInt32(&listHits, 1)
			_ = json.NewEncoder(w).Encode(map[string]any{
				"values": []map[string]any{{
					"full_name":  "acme/repo",
					"slug":       "repo",
					"is_private": false,
					"mainbranch": map[string]any{"name": "main"},
					"links":      map[string]any{"html": map[string]any{"href": "https://bitbucket.org/acme/repo"}},
				}},
				"next": "",
			})
		case r.URL.Path == "/2.0/repositories/acme/repo/src/main/":
			atomic.AddInt32(&fileHits, 1)
			_ = json.NewEncoder(w).Encode(map[string]any{
				"values": []map[string]any{{
					"path": "config/creds.env",
					"type": "commit_file",
					"size": len(payload),
					"links": map[string]any{
						"self": map[string]any{"href": srv.URL + "/raw/creds.env"},
					},
				}},
				"next": "",
			})
		case r.URL.Path == "/raw/creds.env":
			atomic.AddInt32(&rawHits, 1)
			_, _ = io.WriteString(w, payload)
		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(srv.Close)

	installBitbucketSeams(t, srv.URL, srv.Client())

	runBitbucket()

	waitForCond(t, 2*time.Second, func() bool { return hubMatchCount(t) == 1 })
	hub.matchesMutex.RLock()
	got := *hub.matches[0]
	hub.matchesMutex.RUnlock()

	if got.Source != "bitbucket" {
		t.Fatalf("match source = %q, want bitbucket", got.Source)
	}
	if got.File != "config/creds.env" {
		t.Fatalf("match file = %q, want config/creds.env", got.File)
	}
	if got.URL != srv.URL+"/raw/creds.env" {
		t.Fatalf("match url = %q, want the file raw URL", got.URL)
	}
	if n := atomic.LoadInt32(&listHits); n != 1 {
		t.Fatalf("repository listing saw %d requests, want 1", n)
	}
	if n := atomic.LoadInt32(&fileHits); n != 1 {
		t.Fatalf("file listing saw %d requests, want 1", n)
	}
	if n := atomic.LoadInt32(&rawHits); n != 1 {
		t.Fatalf("raw download saw %d requests, want 1", n)
	}
}

// With the source disabled the start guard launches nothing, so the API sees
// zero requests and nothing is published.
func TestStartBitbucketDisabledIssuesNoRequests(t *testing.T) {
	hub := ensureWebHub()
	resetHubForTest(hub)
	newSearchTestSession(t, false) // sources.bitbucket.enabled absent

	var hits int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt32(&hits, 1)
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{"values": []map[string]any{}, "next": ""})
	}))
	t.Cleanup(srv.Close)
	installBitbucketSeams(t, srv.URL, srv.Client())

	if session.Config.BitbucketEnabled() {
		t.Fatal("Bitbucket source must be off by default")
	}
	startBitbucket()

	if n := atomic.LoadInt32(&hits); n != 0 {
		t.Fatalf("disabled Bitbucket source saw %d requests, want 0", n)
	}
	if n := hubMatchCount(t); n != 0 {
		t.Fatalf("disabled Bitbucket source published %d matches, want 0", n)
	}
}
