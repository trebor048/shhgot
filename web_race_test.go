package main

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
)

// The dashboard polls /api/stats and /api/ws every few seconds while the hub
// appends matches. Encoding those responses used to happen after the read lock
// was released, so json.Marshal walked the MatchesBy* maps while WebHub.run was
// writing them — an unrecoverable "concurrent map read and map write" abort,
// not a 500.
//
// Run with -race (CI does). Without Stats.snapshot this test fails.
func TestStatsEncodingWhileHubWrites(t *testing.T) {
	hub := ensureWebHub()

	const rounds = 100

	var wg sync.WaitGroup

	// Writer: feed matches through the same channel the scanner uses.
	wg.Add(1)
	go func() {
		defer wg.Done()
		for i := 0; i < rounds; i++ {
			hub.broadcast <- &Match{
				ID:        newMatchID(),
				Source:    "github",
				Signature: "AWS Access Key ID",
				Priority:  3,
			}
		}
	}()

	// Readers: hammer the two endpoints the dashboard polls.
	for r := 0; r < 4; r++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for i := 0; i < rounds; i++ {
				for _, h := range []http.HandlerFunc{getStats, wsHandler} {
					rec := httptest.NewRecorder()
					h(rec, httptest.NewRequest(http.MethodGet, "/api/stats", nil))
					if rec.Code != http.StatusOK {
						t.Errorf("status = %d, want 200", rec.Code)
						return
					}
				}
			}
		}()
	}

	wg.Wait()

	// The hub must have actually recorded the matches, otherwise this test
	// would pass vacuously by never racing.
	hub.matchesMutex.RLock()
	got := hub.stats.TotalMatches
	hub.matchesMutex.RUnlock()
	if got < rounds {
		t.Fatalf("hub recorded %d matches, want at least %d", got, rounds)
	}
}

func TestCorsRejectsCrossOrigin(t *testing.T) {
	h := corsMiddleware(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	})

	req := httptest.NewRequest(http.MethodGet, "/api/matches", nil)
	req.Host = "127.0.0.1:8080"
	req.Header.Set("Origin", "https://evil.example")
	rec := httptest.NewRecorder()
	h(rec, req)

	if rec.Code != http.StatusForbidden {
		t.Fatalf("cross-origin status = %d, want 403", rec.Code)
	}
	if got := rec.Header().Get("Access-Control-Allow-Origin"); got != "" {
		t.Fatalf("cross-origin response leaked ACAO %q", got)
	}
}

func TestCorsAllowsSameOrigin(t *testing.T) {
	h := corsMiddleware(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	})

	req := httptest.NewRequest(http.MethodGet, "/api/matches", nil)
	req.Host = "127.0.0.1:8080"
	req.Header.Set("Origin", "http://127.0.0.1:8080")
	rec := httptest.NewRecorder()
	h(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("same-origin status = %d, want 200", rec.Code)
	}
	if got := rec.Header().Get("Access-Control-Allow-Origin"); got != "http://127.0.0.1:8080" {
		t.Fatalf("ACAO = %q, want the request origin echoed", got)
	}
}

// The CLI push path and curl send no Origin header and must keep working.
func TestCorsAllowsRequestWithoutOrigin(t *testing.T) {
	h := corsMiddleware(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	})

	req := httptest.NewRequest(http.MethodPost, "/api/push", nil)
	rec := httptest.NewRecorder()
	h(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", rec.Code)
	}
}

// A missing API route must be an honest 404, not the dashboard HTML with 200.
func TestUnknownAPIPathReturns404(t *testing.T) {
	rec := httptest.NewRecorder()
	serveEmbeddedWeb(rec, httptest.NewRequest(http.MethodGet, "/api/nope", nil))

	if rec.Code != http.StatusNotFound {
		t.Fatalf("status = %d, want 404", rec.Code)
	}
	if ct := rec.Header().Get("Content-Type"); !strings.Contains(ct, "application/json") {
		t.Fatalf("Content-Type = %q, want JSON", ct)
	}
}

func TestDashboardServedAtRoot(t *testing.T) {
	rec := httptest.NewRecorder()
	serveEmbeddedWeb(rec, httptest.NewRequest(http.MethodGet, "/", nil))

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", rec.Code)
	}
	if body := rec.Body.String(); !strings.Contains(body, "shhgit") {
		t.Fatal("dashboard body looks empty")
	}
}
