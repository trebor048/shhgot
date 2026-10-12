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

// installGrepAppSeams points the grep.app source at a test server for one test.
func installGrepAppSeams(t *testing.T, baseURL string, doer core.Doer) {
	t.Helper()
	prevBase, prevDoer := grepAppBaseURL, grepAppDoer
	grepAppBaseURL, grepAppDoer = baseURL, doer
	t.Cleanup(func() { grepAppBaseURL, grepAppDoer = prevBase, prevDoer })
}

// A grep.app snippet that holds a key is published once with source grepapp.
func TestRunGrepAppPublishesMatch(t *testing.T) {
	hub := ensureWebHub()
	resetHubForTest(hub)
	s := newSearchTestSession(t, false)
	s.Config.Sources.GrepApp.Enabled = searchBoolPtr(true)
	s.Config.Sources.GrepApp.Queries = []string{"OPENAI_API_KEY"}

	payload := "OPENAI_API_KEY=sk-" + strings.Repeat("a", 40)
	var hits int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt32(&hits, 1)
		w.Header().Set("Content-Type", "application/json")
		if r.URL.Query().Get("page") != "1" {
			_, _ = io.WriteString(w, `{"hits":{"total":0,"hits":[]}}`)
			return
		}
		_ = json.NewEncoder(w).Encode(map[string]any{
			"hits": map[string]any{
				"total": 1,
				"hits": []map[string]any{{
					"repo":    "acme/repo",
					"branch":  "main",
					"path":    "config/creds.env",
					"content": map[string]any{"snippet": `data-line="1"><pre>` + payload + `</pre>`},
				}},
			},
		})
	}))
	t.Cleanup(srv.Close)

	installGrepAppSeams(t, srv.URL, srv.Client())

	runGrepApp()

	waitForCond(t, 2*time.Second, func() bool { return hubMatchCount(t) == 1 })
	hub.matchesMutex.RLock()
	got := *hub.matches[0]
	hub.matchesMutex.RUnlock()

	if got.Source != "grepapp" {
		t.Fatalf("match source = %q, want grepapp", got.Source)
	}
	if got.File != "config/creds.env" {
		t.Fatalf("match file = %q, want config/creds.env", got.File)
	}
	if got.URL != "https://github.com/acme/repo/blob/main/config/creds.env" {
		t.Fatalf("match url = %q, want the GitHub blob URL", got.URL)
	}
	if n := atomic.LoadInt32(&hits); n == 0 {
		t.Fatal("grep.app search issued no requests")
	}
}

// With the source disabled the start guard launches nothing, so the API sees
// zero requests and nothing is published.
func TestStartGrepAppDisabledIssuesNoRequests(t *testing.T) {
	hub := ensureWebHub()
	resetHubForTest(hub)
	newSearchTestSession(t, false) // sources.grepapp.enabled absent

	var hits int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt32(&hits, 1)
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, `{"hits":{"total":0,"hits":[]}}`)
	}))
	t.Cleanup(srv.Close)
	installGrepAppSeams(t, srv.URL, srv.Client())

	if session.Config.GrepAppEnabled() {
		t.Fatal("grep.app source must be off by default")
	}
	startGrepApp()

	if n := atomic.LoadInt32(&hits); n != 0 {
		t.Fatalf("disabled grep.app source saw %d requests, want 0", n)
	}
	if n := hubMatchCount(t); n != 0 {
		t.Fatalf("disabled grep.app source published %d matches, want 0", n)
	}
}
