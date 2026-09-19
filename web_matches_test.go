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
