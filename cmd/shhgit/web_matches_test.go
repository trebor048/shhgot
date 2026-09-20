package main

import (
	"bufio"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"
	"unicode/utf8"

	"github.com/trebor048/shhgot/core"
)

// These tests exercise the dashboard's backend match pipeline end to end
// (hub broadcast -> /api/ws snapshot and /api/events SSE stream), mirroring
// what publish() does in the live scanner.

func resetHubForTest(h *WebHub) {
	h.matchesMutex.Lock()
	h.matches = h.matches[:0]
	h.stats = Stats{
		MatchesBySource:    make(map[string]int),
		MatchesBySignature: make(map[string]int),
		MatchesByPriority:  make(map[int]int),
	}
	h.matchesMutex.Unlock()
}

func testMatch(id string) *Match {
	return &Match{
		ID:        id,
		Timestamp: time.Now(),
		Source:    "github",
		URL:       "https://github.com/owner/repo",
		File:      "creds.env",
		Signature: "OpenAI API Key",
		Matches:   []string{"sk-" + strings.Repeat("a", 48)},
		Secret:    "sk-" + strings.Repeat("a", 48),
		Line:      3,
		HasFile:   true,
		Stars:     5,
		Priority:  3,
		Color:     "#10A37F",
	}
}

func waitForCond(t *testing.T, timeout time.Duration, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		if cond() {
			return
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatal("condition not met within timeout")
}

func TestHubWsSnapshotAndSSEStreamMatch(t *testing.T) {
	hub := ensureWebHub()
	resetHubForTest(hub)

	// 1) A broadcast match must land in hub.matches and show up in /api/ws.
	m1 := testMatch("m1")
	select {
	case hub.broadcast <- m1:
	case <-time.After(time.Second):
		t.Fatal("hub.broadcast full")
	}
	waitForCond(t, 2*time.Second, func() bool {
		hub.matchesMutex.RLock()
		defer hub.matchesMutex.RUnlock()
		return len(hub.matches) == 1
	})

	rec := httptest.NewRecorder()
	wsHandler(rec, httptest.NewRequest("GET", "/api/ws", nil))
	var snap struct {
		Matches []*Match `json:"matches"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &snap); err != nil {
		t.Fatalf("api/ws decode: %v", err)
	}
	if len(snap.Matches) != 1 || snap.Matches[0].ID != "m1" {
		t.Fatalf("api/ws matches = %+v, want [m1]", snap.Matches)
	}

	// 2) Live SSE: connect to /api/events first, then broadcast match #2 and
	// expect it to arrive on the stream.
	srv := httptest.NewServer(http.HandlerFunc(eventsHandler))
	defer srv.Close()

	resp, err := http.Get(srv.URL)
	if err != nil {
		t.Fatalf("events connect: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("events status = %d, want 200", resp.StatusCode)
	}

	m2 := testMatch("m2")
	select {
	case hub.broadcast <- m2:
	case <-time.After(time.Second):
		t.Fatal("hub.broadcast full")
	}

	sc := bufio.NewScanner(resp.Body)
	deadline := time.Now().Add(3 * time.Second)
	last := ""
	found := false
	for time.Now().Before(deadline) {
		if !sc.Scan() {
			break
		}
		line := sc.Text()
		if strings.HasPrefix(line, "data: ") {
			last = strings.TrimPrefix(line, "data: ")
			if strings.Contains(last, `"id":"m2"`) {
				found = true
				break
			}
		}
	}
	if !found {
		t.Fatalf("SSE stream never delivered m2; last data: %s", last)
	}
}

// TestNewMatchIDUnique is a regression test for the "matches disappear from
// the dashboard" bug: match IDs were time.Now().UnixNano() alone, which
// collides when concurrent scan threads publish within the same (coarse)
// Windows clock tick. Duplicate IDs collapse distinct matches into one card.
// On the old code this test fails with ~19 duplicates across 60 ids.
func TestNewMatchIDUnique(t *testing.T) {
	const n = 2000
	ids := make(chan string, n)
	var wg sync.WaitGroup
	for g := 0; g < 8; g++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for i := 0; i < n/8; i++ {
				ids <- newMatchID()
			}
		}()
	}
	wg.Wait()
	close(ids)

	seen := make(map[string]bool, n)
	dups := 0
	for id := range ids {
		if seen[id] {
			dups++
		}
		seen[id] = true
	}
	if dups > 0 {
		t.Fatalf("newMatchID produced %d duplicate ids across %d concurrent calls", dups, n)
	}
	if len(seen) != n {
		t.Fatalf("expected %d unique ids, got %d", n, len(seen))
	}
}

// --- restored dashboard endpoints -------------------------------------------

// The dashboard's scan-progress panel and regex-performance card used to poll
// /api/scan/progress and /api/stats/regex; the web refactor dropped both routes,
// so those panels 404'd. These tests pin the endpoints (and their shapes) back.
func TestScanProgressEndpointReturnsSnapshot(t *testing.T) {
	rec := httptest.NewRecorder()
	getScanProgress(rec, httptest.NewRequest(http.MethodGet, "/api/scan/progress", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", rec.Code)
	}
	var snap map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &snap); err != nil {
		t.Fatalf("decode: %v", err)
	}
	for _, key := range []string{"is_scanning", "progress", "repos_scanned", "files_processed", "matches_found", "speed", "estimated_time_remaining", "current_repo"} {
		if _, ok := snap[key]; !ok {
			t.Errorf("scan progress snapshot is missing %q", key)
		}
	}
}

func TestRegexStatsEndpointReturnsStats(t *testing.T) {
	prev := core.GlobalRegexOptimizer
	core.GlobalRegexOptimizer = core.NewRegexOptimizer(4, 1000)
	t.Cleanup(func() { core.GlobalRegexOptimizer = prev })

	if _, err := core.GlobalRegexOptimizer.CompilePattern(`AKIA[0-9A-Z]{16}`, "AWS Access Key ID", 3, "contents"); err != nil {
		t.Fatalf("compile: %v", err)
	}

	rec := httptest.NewRecorder()
	getRegexStats(rec, httptest.NewRequest(http.MethodGet, "/api/stats/regex", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", rec.Code)
	}
	var stats map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &stats); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if n, ok := stats["patterns_compiled"].(float64); !ok || n < 1 {
		t.Errorf("patterns_compiled = %v, want at least 1", stats["patterns_compiled"])
	}
	if w, ok := stats["max_workers"].(float64); !ok || int(w) != 4 {
		t.Errorf("max_workers = %v, want 4", stats["max_workers"])
	}
}

// A request before the scanner's session has created the optimizer must answer
// with zeroed counters, not dereference a nil singleton and abort the process.
func TestRegexStatsEndpointSurvivesNilOptimizer(t *testing.T) {
	prev := core.GlobalRegexOptimizer
	core.GlobalRegexOptimizer = nil
	t.Cleanup(func() { core.GlobalRegexOptimizer = prev })

	rec := httptest.NewRecorder()
	getRegexStats(rec, httptest.NewRequest(http.MethodGet, "/api/stats/regex", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", rec.Code)
	}
	var stats map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &stats); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if n, ok := stats["patterns_compiled"].(float64); !ok || n != 0 {
		t.Errorf("patterns_compiled = %v, want 0", stats["patterns_compiled"])
	}
}

// The SSE connect snapshot must carry the current matches and stats, so a
// dashboard that (re)connects after losing the stream recovers what it missed
// instead of silently falling behind.
func TestEventsConnectSnapshotCarriesMatchesAndStats(t *testing.T) {
	hub := ensureWebHub()
	resetHubForTest(hub)

	m := testMatch("snap-1")
	select {
	case hub.broadcast <- m:
	case <-time.After(time.Second):
		t.Fatal("hub.broadcast full")
	}
	waitForCond(t, 2*time.Second, func() bool {
		hub.matchesMutex.RLock()
		defer hub.matchesMutex.RUnlock()
		return len(hub.matches) == 1
	})

	srv := httptest.NewServer(http.HandlerFunc(eventsHandler))
	defer srv.Close()
	resp, err := http.Get(srv.URL)
	if err != nil {
		t.Fatalf("events connect: %v", err)
	}
	defer resp.Body.Close()

	sc := bufio.NewScanner(resp.Body)
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		if !sc.Scan() {
			break
		}
		line := sc.Text()
		if !strings.HasPrefix(line, "data: ") {
			continue
		}
		var ev struct {
			Type    string          `json:"type"`
			Matches []*Match        `json:"matches"`
			Stats   json.RawMessage `json:"stats"`
		}
		if err := json.Unmarshal([]byte(strings.TrimPrefix(line, "data: ")), &ev); err != nil {
			continue
		}
		if ev.Type != "snapshot" {
			continue
		}
		if len(ev.Matches) != 1 || ev.Matches[0].ID != "snap-1" {
			t.Fatalf("snapshot matches = %+v, want [snap-1]", ev.Matches)
		}
		if len(ev.Stats) == 0 || string(ev.Stats) == "null" {
			t.Fatal("snapshot carried no stats")
		}
		return
	}
	t.Fatal("no snapshot frame arrived")
}

// --- file cache eviction -----------------------------------------------------

func withFileCache(t *testing.T) {
	t.Helper()
	prevDetails, prevOrder := fileDetails, fileOrder
	prevMax, prevBytes := maxFileDetails, maxStoreFileBytes
	fileMu.Lock()
	fileDetails = make(map[string]*MatchFileDetail)
	fileOrder = nil
	fileMu.Unlock()
	t.Cleanup(func() {
		fileMu.Lock()
		fileDetails, fileOrder = prevDetails, prevOrder
		fileMu.Unlock()
		maxFileDetails, maxStoreFileBytes = prevMax, prevBytes
	})
}

// Re-storing a file must move it to the most-recent position. Otherwise a file
// captured again later kept its old slot and was evicted ahead of files seen
// earlier, so "view full file" failed for the newest match.
func TestStoreMatchFileRefreshesEvictionOrder(t *testing.T) {
	withFileCache(t)
	maxFileDetails = 2

	storeMatchFile("a", "u", "a.txt", "AAA", "", 0)
	storeMatchFile("b", "u", "b.txt", "BBB", "", 0)
	storeMatchFile("a", "u", "a.txt", "AAA2", "", 0) // re-capture: a is now newest
	storeMatchFile("c", "u", "c.txt", "CCC", "", 0)  // pushes the cache over its cap

	fileMu.Lock()
	_, hasA := fileDetails["a"]
	_, hasB := fileDetails["b"]
	_, hasC := fileDetails["c"]
	fileMu.Unlock()
	if !hasA {
		t.Error("the re-stored file was evicted: its position was not refreshed")
	}
	if hasB {
		t.Error("the oldest file should have been evicted")
	}
	if !hasC {
		t.Error("the newest file should be present")
	}
}

// Truncation must not split a multi-byte character: the fragment became U+FFFD
// on the wire and the viewer showed a stray replacement glyph.
func TestStoreMatchFileTruncatesOnRuneBoundary(t *testing.T) {
	withFileCache(t)
	// Each '世' is 3 bytes; a 4-byte cap would otherwise cut the second rune.
	maxStoreFileBytes = 4
	storeMatchFile("rune", "u", "f.txt", "世界", "", 0)

	fileMu.Lock()
	d := fileDetails["rune"]
	fileMu.Unlock()
	if d == nil {
		t.Fatal("file was not stored")
	}
	if !utf8.ValidString(d.Content) {
		t.Fatalf("stored content is not valid UTF-8: %q", d.Content)
	}
	if len(d.Content) > maxStoreFileBytes {
		t.Fatalf("stored %d bytes, want <= %d", len(d.Content), maxStoreFileBytes)
	}
	if !d.Truncated {
		t.Error("the stored file should be marked truncated")
	}
}
