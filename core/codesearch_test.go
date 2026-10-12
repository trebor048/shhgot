package core

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strconv"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

// installSearchSeams points the search clock and sleep at clk, resets the
// shared rate limiter, and restores both when the test finishes.
func installSearchSeams(t *testing.T, clk *seamClock) {
	t.Helper()
	oldNow, oldSleep := searchNow, searchSleep
	searchNow, searchSleep = clk.now, clk.sleep
	resetSearchLimiter()
	t.Cleanup(func() {
		searchNow, searchSleep = oldNow, oldSleep
		resetSearchLimiter()
	})
}

func resetSearchLimiter() {
	searchLimiter.mu.Lock()
	searchLimiter.windowStart = time.Time{}
	searchLimiter.count = 0
	searchLimiter.mu.Unlock()
}

type testSearchRepo struct {
	FullName        string `json:"full_name"`
	DefaultBranch   string `json:"default_branch"`
	Language        string `json:"language"`
	StargazersCount int    `json:"stargazers_count"`
}

type testSearchItem struct {
	Name       string         `json:"name"`
	Path       string         `json:"path"`
	SHA        string         `json:"sha"`
	HTMLURL    string         `json:"html_url"`
	Repository testSearchRepo `json:"repository"`
}

type testSearchResponse struct {
	TotalCount        int              `json:"total_count"`
	IncompleteResults bool             `json:"incomplete_results"`
	Items             []testSearchItem `json:"items"`
}

func testSearchPage(n int) testSearchResponse {
	items := make([]testSearchItem, n)
	for i := range items {
		items[i] = testSearchItem{
			Name:    fmt.Sprintf("file%d.go", i),
			Path:    fmt.Sprintf("dir/file%d.go", i),
			SHA:     "deadbeef",
			HTMLURL: "https://github.com/acme/repo/blob/main/dir/file.go",
			Repository: testSearchRepo{
				FullName:        "acme/repo",
				DefaultBranch:   "main",
				Language:        "Go",
				StargazersCount: 7,
			},
		}
	}
	return testSearchResponse{TotalCount: 9999, Items: items}
}

func writeSearchPage(w http.ResponseWriter, n int) {
	_ = json.NewEncoder(w).Encode(testSearchPage(n))
}

func TestCodeSearchPagination(t *testing.T) {
	clk := newSeamClock()
	installSearchSeams(t, clk)

	var mu sync.Mutex
	var pages []int
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		q := r.URL.Query()
		page, _ := strconv.Atoi(q.Get("page"))
		mu.Lock()
		pages = append(pages, page)
		mu.Unlock()
		if q.Get("q") != `"sk-proj-" in:file` {
			t.Errorf("unexpected q parameter %q", q.Get("q"))
		}
		if page == 3 {
			writeSearchPage(w, 2)
			return
		}
		writeSearchPage(w, 5)
	}))
	defer srv.Close()

	hits, err := SearchCode(srv.Client(), CodeSearchOptions{
		PerPage:  5,
		MaxPages: 10,
		BaseURL:  srv.URL,
	}, `"sk-proj-" in:file`)
	if err != nil {
		t.Fatalf("SearchCode error: %v", err)
	}
	if len(hits) != 12 {
		t.Fatalf("got %d hits, want 12", len(hits))
	}
	mu.Lock()
	gotPages := append([]int(nil), pages...)
	mu.Unlock()
	if !reflect.DeepEqual(gotPages, []int{1, 2, 3}) {
		t.Fatalf("requested pages = %v, want [1 2 3]", gotPages)
	}
	first := hits[0]
	if first.Repo != "acme/repo" || first.Path != "dir/file0.go" || first.SHA != "deadbeef" ||
		first.Language != "Go" || first.Stars != 7 || first.HTMLURL == "" {
		t.Fatalf("unexpected first hit: %+v", first)
	}
	wantRaw := "https://raw.githubusercontent.com/acme/repo/main/dir/file0.go"
	if first.RawURL != wantRaw {
		t.Fatalf("RawURL = %q, want %q", first.RawURL, wantRaw)
	}
}

func TestCodeSearchHardCap(t *testing.T) {
	clk := newSeamClock()
	installSearchSeams(t, clk)

	var mu sync.Mutex
	var pages []int
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		page, _ := strconv.Atoi(r.URL.Query().Get("page"))
		mu.Lock()
		pages = append(pages, page)
		mu.Unlock()
		writeSearchPage(w, 100)
	}))
	defer srv.Close()

	hits, err := SearchCode(srv.Client(), CodeSearchOptions{
		PerPage:    100,
		MaxPages:   50,
		MaxResults: 5000,
		BaseURL:    srv.URL,
	}, "q")
	if err != nil {
		t.Fatalf("SearchCode error: %v", err)
	}
	if len(hits) != 1000 {
		t.Fatalf("got %d hits, want hard cap 1000", len(hits))
	}
	mu.Lock()
	requested := len(pages)
	mu.Unlock()
	if requested != 10 {
		t.Fatalf("requested %d pages, want 10 before the cap", requested)
	}
}

func TestCodeSearchRateLimitPerMinute(t *testing.T) {
	clk := newSeamClock()
	installSearchSeams(t, clk)

	var mu sync.Mutex
	var times []time.Time
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		times = append(times, clk.now())
		mu.Unlock()
		writeSearchPage(w, 1)
	}))
	defer srv.Close()

	hits, err := SearchCode(srv.Client(), CodeSearchOptions{
		PerPage:  1,
		MaxPages: 40,
		BaseURL:  srv.URL,
	}, "q")
	if err != nil {
		t.Fatalf("SearchCode error: %v", err)
	}
	if len(hits) != 40 {
		t.Fatalf("got %d hits, want 40", len(hits))
	}

	mu.Lock()
	gotTimes := append([]time.Time(nil), times...)
	mu.Unlock()
	if len(gotTimes) != 40 {
		t.Fatalf("server saw %d requests, want 40", len(gotTimes))
	}

	start := time.Unix(1_700_000_000, 0)
	buckets := map[int]int{}
	for _, ts := range gotTimes {
		buckets[int(ts.Sub(start)/time.Minute)]++
	}
	for minute, count := range buckets {
		if count > 30 {
			t.Fatalf("minute %d saw %d requests, want at most 30", minute, count)
		}
	}
	if total := clk.totalSleep(); total < time.Minute {
		t.Fatalf("recorded sleep total = %v, want at least 1m of parking", total)
	}
}

func TestCodeSearchResetsOnRateLimit(t *testing.T) {
	clk := newSeamClock()
	installSearchSeams(t, clk)

	var hits int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if atomic.AddInt32(&hits, 1) == 1 {
			w.Header().Set("Retry-After", "3")
			w.WriteHeader(http.StatusTooManyRequests)
			return
		}
		writeSearchPage(w, 5)
	}))
	defer srv.Close()

	got, err := SearchCode(srv.Client(), CodeSearchOptions{
		PerPage:  5,
		MaxPages: 1,
		BaseURL:  srv.URL,
	}, "q")
	if err != nil {
		t.Fatalf("SearchCode error: %v", err)
	}
	if len(got) != 5 {
		t.Fatalf("got %d hits, want 5", len(got))
	}
	if sleeps := clk.sleepsCopy(); len(sleeps) != 1 || sleeps[0] != 3*time.Second {
		t.Fatalf("recorded sleeps = %v, want [3s]", sleeps)
	}
}

// TestCodeSearchSurfacesBare403Immediately proves a 403 without any Retry-After
// (an auth failure, not quota) is returned at once: no sleep, no retry burn.
func TestCodeSearchSurfacesBare403Immediately(t *testing.T) {
	clk := newSeamClock()
	installSearchSeams(t, clk)

	var hits int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt32(&hits, 1)
		w.WriteHeader(http.StatusForbidden)
	}))
	defer srv.Close()

	resp, err := searchPage(srv.Client(), srv.URL, "bad-token", "q", 5, 1)
	if err != nil {
		t.Fatalf("searchPage error: %v", err)
	}
	closeResponse(resp)
	if resp.StatusCode != http.StatusForbidden {
		t.Fatalf("status = %d, want 403 surfaced", resp.StatusCode)
	}
	if n := atomic.LoadInt32(&hits); n != 1 {
		t.Fatalf("requests = %d, want 1 (no retry)", n)
	}
	if sleeps := clk.sleepsCopy(); len(sleeps) != 0 {
		t.Fatalf("recorded sleeps = %v, want none", sleeps)
	}
}

func TestCodeSearchBuildDorks(t *testing.T) {
	got := BuildDorks([]string{
		"sk-proj-", "AKIA", "ghp_",
		"api_key", "apikey", "key", "token", "secret", "password",
		"abc", "", "  ", "sk-proj-",
	})
	want := []string{
		`"sk-proj-" in:file NOT test NOT example NOT sample NOT mock`,
		`"AKIA" in:file NOT test NOT example NOT sample NOT mock`,
		`"ghp_" in:file NOT test NOT example NOT sample NOT mock`,
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("BuildDorks = %#v, want %#v", got, want)
	}
}

// TestSearchGateReleasesLockWhileSleeping verifies that searchGate.wait()
// does not hold the mutex while sleeping, so concurrent callers can
// observe the updated window state without blocking on the sleep.
func TestSearchGateReleasesLockWhileSleeping(t *testing.T) {
	resetSearchLimiter()
	oldNow, oldSleep := searchNow, searchSleep
	defer func() { searchNow, searchSleep = oldNow, oldSleep }()

	start := time.Unix(1_700_000_000, 0)
	searchNow = func() time.Time { return start }

	entered := make(chan struct{})
	cont := make(chan struct{})
	searchSleep = func(d time.Duration) {
		close(entered)
		<-cont
	}

	// Fill the window to trigger a sleep.
	searchLimiter.mu.Lock()
	searchLimiter.windowStart = start
	searchLimiter.count = searchMaxPerMinute
	searchLimiter.mu.Unlock()

	var wg sync.WaitGroup
	wg.Add(1)
	go func() {
		defer wg.Done()
		searchLimiter.wait()
	}()

	select {
	case <-entered:
	case <-time.After(time.Second):
		t.Fatal("sleep function was not entered")
	}

	// If the lock were held during sleep, this Lock would block.
	locked := make(chan struct{})
	go func() {
		searchLimiter.mu.Lock()
		close(locked)
		searchLimiter.mu.Unlock()
	}()

	select {
	case <-locked:
	case <-time.After(time.Second):
		t.Fatal("searchLimiter.mu was still held while sleeping")
	}

	close(cont)
	wg.Wait()
}
