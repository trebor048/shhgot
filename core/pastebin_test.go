package core

import (
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

func TestNewPastebinClientDefaults(t *testing.T) {
	c := NewPastebinClient(nil, "")
	if c.BaseURL != "https://scrape.pastebin.com" {
		t.Fatalf("BaseURL = %q, want https://scrape.pastebin.com", c.BaseURL)
	}
	if c.RawBase != "https://scrape.pastebin.com/api_scrape_item.php?i=" {
		t.Fatalf("RawBase = %q, want the scrape item URL", c.RawBase)
	}
	if c.PublicRaw != "https://pastebin.com/raw/" {
		t.Fatalf("PublicRaw = %q, want https://pastebin.com/raw/", c.PublicRaw)
	}

	c2 := NewPastebinClient(nil, "https://example.test/")
	if c2.BaseURL != "https://example.test" {
		t.Fatalf("BaseURL = %q, want the trailing slash trimmed", c2.BaseURL)
	}
}

func TestPastebinListRecentParsesAndSkipsEmptyKeys(t *testing.T) {
	var mu sync.Mutex
	var path, limit string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		path = r.URL.Path
		limit = r.URL.Query().Get("limit")
		mu.Unlock()
		w.Header().Set("Content-Type", "application/json")
		writeBody(w, `[
			{"key":"aaa","title":"first","syntax":"go","size":12,"date":"2026-01-01"},
			{"key":"","title":"skip-me","syntax":"text","size":3,"date":"2026-01-02"},
			{"key":"bbb","title":"second","syntax":"python","size":34,"date":"2026-01-03"}
		]`)
	}))
	defer srv.Close()

	c := NewPastebinClient(srv.Client(), srv.URL)
	got, err := c.ListRecent(7)
	if err != nil {
		t.Fatalf("ListRecent error: %v", err)
	}

	mu.Lock()
	gotPath, gotLimit := path, limit
	mu.Unlock()
	if gotPath != "/api_scraping.php" {
		t.Fatalf("request path = %q, want /api_scraping.php", gotPath)
	}
	if gotLimit != "7" {
		t.Fatalf("limit query = %q, want 7", gotLimit)
	}
	if len(got) != 2 {
		t.Fatalf("got %d pastes, want 2: %+v", len(got), got)
	}
	want := PastebinPaste{
		Key:    "aaa",
		Title:  "first",
		Syntax: "go",
		Date:   "2026-01-01",
		Size:   12,
		URL:    "https://pastebin.com/aaa",
	}
	if got[0] != want {
		t.Fatalf("first paste = %+v, want %+v", got[0], want)
	}
	if got[1].Key != "bbb" || got[1].URL != "https://pastebin.com/bbb" {
		t.Fatalf("second paste = %+v, want key bbb at https://pastebin.com/bbb", got[1])
	}
}

func TestPastebinListRecentDefaultLimit(t *testing.T) {
	var mu sync.Mutex
	var limit string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		limit = r.URL.Query().Get("limit")
		mu.Unlock()
		w.Header().Set("Content-Type", "application/json")
		writeBody(w, `[]`)
	}))
	defer srv.Close()

	c := NewPastebinClient(srv.Client(), srv.URL)
	if _, err := c.ListRecent(0); err != nil {
		t.Fatalf("ListRecent error: %v", err)
	}
	mu.Lock()
	gotLimit := limit
	mu.Unlock()
	if gotLimit != "100" {
		t.Fatalf("limit query = %q, want 100", gotLimit)
	}
}

func TestPastebinPasteContentBothURLForms(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.URL.Path == "/api_scrape_item.php":
			if r.URL.Query().Get("i") != "KEY123" {
				http.Error(w, "bad key", http.StatusBadRequest)
				return
			}
			writeBody(w, "SCRAPE-BODY")
		case r.URL.Path == "/raw/KEY123":
			writeBody(w, "PUBLIC-BODY")
		default:
			http.NotFound(w, r)
		}
	}))
	defer srv.Close()

	c := NewPastebinClient(srv.Client(), srv.URL)
	c.RawBase = srv.URL + "/api_scrape_item.php?i="
	c.PublicRaw = srv.URL + "/raw/"

	scrape, err := c.PasteContent("KEY123", true)
	if err != nil {
		t.Fatalf("scrape PasteContent error: %v", err)
	}
	if string(scrape) != "SCRAPE-BODY" {
		t.Fatalf("scrape PasteContent = %q, want SCRAPE-BODY", scrape)
	}

	public, err := c.PasteContent("KEY123", false)
	if err != nil {
		t.Fatalf("public PasteContent error: %v", err)
	}
	if string(public) != "PUBLIC-BODY" {
		t.Fatalf("public PasteContent = %q, want PUBLIC-BODY", public)
	}
}

func TestPastebinPasteContentRejectsNonBareKeys(t *testing.T) {
	var hits int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt32(&hits, 1)
		writeBody(w, "BODY")
	}))
	defer srv.Close()

	c := NewPastebinClient(srv.Client(), srv.URL)
	c.PublicRaw = srv.URL + "/raw/"
	for _, key := range []string{"KEY?x=1", "KEY#frag", "../etc", "KEY/123", "KEY 123", "KEY.123"} {
		if _, err := c.PasteContent(key, false); err == nil {
			t.Fatalf("PasteContent(%q) returned nil error, want rejection", key)
		}
	}
	if got := atomic.LoadInt32(&hits); got != 0 {
		t.Fatalf("rejected keys issued %d requests, want 0", got)
	}
}

func TestPastebinListRecentRateLimitSleeps(t *testing.T) {
	var calls int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if atomic.AddInt32(&calls, 1) == 1 {
			w.Header().Set("Retry-After", "5")
			w.WriteHeader(http.StatusTooManyRequests)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		writeBody(w, `[{"key":"after","title":"after-backoff"}]`)
	}))
	defer srv.Close()

	clk := newSeamClock()
	c := NewPastebinClient(srv.Client(), srv.URL)
	c.Now = clk.now
	c.Sleep = clk.sleep
	got, err := c.ListRecent(0)
	if err != nil {
		t.Fatalf("ListRecent error: %v", err)
	}
	if len(got) != 1 || got[0].Key != "after" {
		t.Fatalf("got %+v, want the paste from after the backoff", got)
	}
	if sleeps := clk.sleepsCopy(); len(sleeps) != 1 || sleeps[0] != 5*time.Second {
		t.Fatalf("recorded sleeps = %v, want [5s]", sleeps)
	}
	if n := atomic.LoadInt32(&calls); n != 2 {
		t.Fatalf("server observed %d requests, want 2", n)
	}
}

func TestPastebinListRecentForbiddenSleeps(t *testing.T) {
	var calls int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if atomic.AddInt32(&calls, 1) == 1 {
			w.Header().Set("Retry-After", "3")
			w.WriteHeader(http.StatusForbidden)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		writeBody(w, `[]`)
	}))
	defer srv.Close()

	clk := newSeamClock()
	c := NewPastebinClient(srv.Client(), srv.URL)
	c.Now = clk.now
	c.Sleep = clk.sleep
	if _, err := c.ListRecent(0); err != nil {
		t.Fatalf("ListRecent error: %v", err)
	}
	if sleeps := clk.sleepsCopy(); len(sleeps) != 1 || sleeps[0] != 3*time.Second {
		t.Fatalf("recorded sleeps = %v, want [3s]", sleeps)
	}
	if n := atomic.LoadInt32(&calls); n != 2 {
		t.Fatalf("server observed %d requests, want 2", n)
	}
}

func TestPastebinPasteContentRejectsOffAllowlistHost(t *testing.T) {
	var hits int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt32(&hits, 1)
		writeBody(w, "secret")
	}))
	defer srv.Close()

	// The BaseURL host is scrape.pastebin.com, so the httptest host is not on
	// the allowlist even though the body bases point at it.
	c := NewPastebinClient(srv.Client(), "https://scrape.pastebin.com")
	c.RawBase = srv.URL + "/api_scrape_item.php?i="
	c.PublicRaw = srv.URL + "/raw/"

	if _, err := c.PasteContent("abc", false); err == nil {
		t.Fatal("PasteContent off-allowlist returned nil error, want error")
	}
	if got := atomic.LoadInt32(&hits); got != 0 {
		t.Fatalf("off-allowlist download issued %d requests, want 0", got)
	}
}

func TestPastebinPasteContentRejectsOversizedBody(t *testing.T) {
	big := strings.Repeat("a", (2<<20)+1)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		writeBody(w, big)
	}))
	defer srv.Close()

	c := NewPastebinClient(srv.Client(), srv.URL)
	c.PublicRaw = srv.URL + "/raw/"

	_, err := c.PasteContent("big", false)
	if !errors.Is(err, errFileTooLarge) {
		t.Fatalf("PasteContent oversized error = %v, want errFileTooLarge", err)
	}
}

func TestPastebinPasteContentEmptyKey(t *testing.T) {
	c := NewPastebinClient(nil, "")
	if _, err := c.PasteContent("   ", false); err == nil {
		t.Fatal("PasteContent empty key returned nil error, want error")
	}
}
