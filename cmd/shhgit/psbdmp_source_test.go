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

// installPsbdmpSeams points the psbdmp source at a test server for one test.
func installPsbdmpSeams(t *testing.T, baseURL string, doer core.Doer) {
	t.Helper()
	prevBase, prevDoer := psbdmpBaseURL, psbdmpDoer
	psbdmpBaseURL, psbdmpDoer = baseURL, doer
	t.Cleanup(func() { psbdmpBaseURL, psbdmpDoer = prevBase, prevDoer })
}

// A psbdmp paste that holds a key is published once with source psbdmp.
func TestRunPsbdmpPublishesMatch(t *testing.T) {
	hub := ensureWebHub()
	resetHubForTest(hub)
	s := newSearchTestSession(t, false)
	s.Config.Sources.Psbdmp.Enabled = searchBoolPtr(true)
	s.Config.Sources.Psbdmp.Queries = []string{"OPENAI_API_KEY"}

	payload := "OPENAI_API_KEY=sk-" + strings.Repeat("a", 40)
	var hits int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt32(&hits, 1)
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{
			"data": []map[string]any{{
				"id":   "abc123",
				"text": payload,
				"url":  "https://pastebin.com/abc123",
				"tags": "leak,key",
			}},
		})
	}))
	t.Cleanup(srv.Close)

	installPsbdmpSeams(t, srv.URL, srv.Client())

	runPsbdmp()

	waitForCond(t, 2*time.Second, func() bool { return hubMatchCount(t) == 1 })
	hub.matchesMutex.RLock()
	got := *hub.matches[0]
	hub.matchesMutex.RUnlock()

	if got.Source != "psbdmp" {
		t.Fatalf("match source = %q, want psbdmp", got.Source)
	}
	if got.Signature != "Test Credential" {
		t.Fatalf("match signature = %q, want Test Credential", got.Signature)
	}
	if got.URL != "https://pastebin.com/abc123" {
		t.Fatalf("match url = %q, want https://pastebin.com/abc123", got.URL)
	}
	if n := atomic.LoadInt32(&hits); n != 1 {
		t.Fatalf("psbdmp search saw %d requests, want 1", n)
	}
}

// With the source disabled the start guard launches nothing, so the API sees
// zero requests and nothing is published.
func TestStartPsbdmpDisabledIssuesNoRequests(t *testing.T) {
	hub := ensureWebHub()
	resetHubForTest(hub)
	newSearchTestSession(t, false) // sources.psbdmp.enabled absent

	var hits int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt32(&hits, 1)
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{"data": []map[string]any{}})
	}))
	t.Cleanup(srv.Close)
	installPsbdmpSeams(t, srv.URL, srv.Client())

	if session.Config.PsbdmpEnabled() {
		t.Fatal("psbdmp source must be off by default")
	}
	startPsbdmp()

	if n := atomic.LoadInt32(&hits); n != 0 {
		t.Fatalf("disabled psbdmp source saw %d requests, want 0", n)
	}
	if n := hubMatchCount(t); n != 0 {
		t.Fatalf("disabled psbdmp source published %d matches, want 0", n)
	}
}
