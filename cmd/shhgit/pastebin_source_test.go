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

// installPastebinSeams points the Pastebin source at a test server for one test.
func installPastebinSeams(t *testing.T, baseURL, rawBase, publicRaw string, doer core.Doer) {
	t.Helper()
	prevBase, prevRaw, prevPublic, prevDoer := pastebinBaseURL, pastebinRawBase, pastebinPublicRaw, pastebinDoer
	pastebinBaseURL, pastebinRawBase, pastebinPublicRaw, pastebinDoer = baseURL, rawBase, publicRaw, doer
	t.Cleanup(func() {
		pastebinBaseURL, pastebinRawBase, pastebinPublicRaw, pastebinDoer = prevBase, prevRaw, prevPublic, prevDoer
	})
}

// A paste whose body holds a key is listed and downloaded through the Scraping
// API and published once with source pastebin.
func TestRunPastebinPublishesMatch(t *testing.T) {
	hub := ensureWebHub()
	resetHubForTest(hub)
	s := newSearchTestSession(t, false)
	s.Config.Sources.Pastebin.Enabled = searchBoolPtr(true)
	s.Config.Sources.Pastebin.APIKey = "pro-key"

	payload := "TOKEN=sk-" + strings.Repeat("a", 40)
	var listHits, rawHits int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/api_scraping.php":
			atomic.AddInt32(&listHits, 1)
			w.Header().Set("Content-Type", "application/json")
			_ = json.NewEncoder(w).Encode([]map[string]any{{
				"key":    "KEY1",
				"title":  "leaky",
				"syntax": "env",
				"size":   len(payload),
				"date":   "2026-01-01",
			}})
		case "/api_scrape_item.php":
			if r.URL.Query().Get("i") != "KEY1" {
				http.Error(w, "bad key", http.StatusBadRequest)
				return
			}
			atomic.AddInt32(&rawHits, 1)
			_, _ = io.WriteString(w, payload)
		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(srv.Close)

	installPastebinSeams(t, srv.URL, srv.URL+"/api_scrape_item.php?i=", "", srv.Client())

	runPastebin()

	waitForCond(t, 2*time.Second, func() bool { return hubMatchCount(t) == 1 })
	hub.matchesMutex.RLock()
	got := *hub.matches[0]
	hub.matchesMutex.RUnlock()

	if got.Source != "pastebin" {
		t.Fatalf("match source = %q, want pastebin", got.Source)
	}
	if got.Signature != "Test Credential" {
		t.Fatalf("match signature = %q, want Test Credential", got.Signature)
	}
	if n := atomic.LoadInt32(&listHits); n != 1 {
		t.Fatalf("paste listing saw %d requests, want 1", n)
	}
	if n := atomic.LoadInt32(&rawHits); n != 1 {
		t.Fatalf("paste download saw %d requests, want 1", n)
	}
}

// With the source disabled the start guard launches nothing, so the API sees
// zero requests and nothing is published.
func TestStartPastebinDisabledIssuesNoRequests(t *testing.T) {
	hub := ensureWebHub()
	resetHubForTest(hub)
	newSearchTestSession(t, false) // sources.pastebin.enabled absent

	var hits int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt32(&hits, 1)
		_ = json.NewEncoder(w).Encode([]map[string]any{})
	}))
	t.Cleanup(srv.Close)
	installPastebinSeams(t, srv.URL, srv.URL+"/api_scrape_item.php?i=", "", srv.Client())

	if session.Config.PastebinEnabled() {
		t.Fatal("Pastebin source must be off by default")
	}
	startPastebin()

	if n := atomic.LoadInt32(&hits); n != 0 {
		t.Fatalf("disabled Pastebin source saw %d requests, want 0", n)
	}
	if n := hubMatchCount(t); n != 0 {
		t.Fatalf("disabled Pastebin source published %d matches, want 0", n)
	}
}
