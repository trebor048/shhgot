package core

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"sync"
	"testing"

	"github.com/google/go-github/github"
)

// TestEventsPagingCycle drives the helpers GetRepositories uses (listEventsPage
// and markNewEvents) against a fake events API. The first cycle must page 1 then
// 2; the second must replay page 1's ETag, get a 304, and stop without asking
// for page 2 again.
func TestEventsPagingCycle(t *testing.T) {
	var (
		mu       sync.Mutex
		requests []string
	)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		page := r.URL.Query().Get("page")
		etag := `"gen1-p` + page + `"`

		mu.Lock()
		requests = append(requests, "page="+page+" inm="+r.Header.Get("If-None-Match"))
		mu.Unlock()

		w.Header().Set("ETag", etag)
		if r.Header.Get("If-None-Match") == etag {
			w.WriteHeader(http.StatusNotModified)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, `[{"id":"p`+page+`-e1","type":"PushEvent"}]`)
	}))
	defer srv.Close()

	gh := github.NewClient(nil)
	base, err := url.Parse(srv.URL + "/")
	if err != nil {
		t.Fatal(err)
	}
	gh.BaseURL = base
	client := &GitHubClientWrapper{Client: gh}

	const (
		perPage  = 100
		maxPages = 2
	)
	etags := make([]string, maxPages+1)
	seen := map[string]bool{}
	var seenMu sync.Mutex

	// runCycle mirrors the production loop's stop conditions, driving the real
	// listEventsPage and markNewEvents.
	runCycle := func() int {
		fetched := 0
		for page := 1; page <= maxPages; page++ {
			events, resp, notModified, err := listEventsPage(context.Background(), client, perPage, page, etags[page])
			if err != nil {
				t.Fatalf("page %d: %v", page, err)
			}
			fetched++
			if notModified {
				break
			}
			if resp != nil {
				if tag := resp.Header.Get("ETag"); tag != "" {
					etags[page] = tag
				}
			}
			newly := markNewEvents(events, seen, &seenMu)
			if len(events) == 0 || len(newly) == 0 {
				break
			}
		}
		return fetched
	}

	if got := runCycle(); got != 2 {
		t.Errorf("cycle 1 fetched %d pages, want 2", got)
	}
	if got := runCycle(); got != 1 {
		t.Errorf("cycle 2 fetched %d pages, want 1 (304 on page 1)", got)
	}

	mu.Lock()
	defer mu.Unlock()
	if len(requests) != 3 {
		t.Fatalf("requests = %v, want 3", requests)
	}
	if requests[2] != `page=1 inm="gen1-p1"` {
		t.Errorf("cycle 2 did not replay page 1's ETag: %q", requests[2])
	}
}

// TestMarkNewEventsDedupes pins the dedup contract: an id already seen is not
// returned again, and within one page a repeated id is returned once.
func TestMarkNewEventsDedupes(t *testing.T) {
	seen := map[string]bool{}
	var mu sync.Mutex
	e := func(id string) *github.Event { return &github.Event{ID: github.String(id)} }

	got := markNewEvents([]*github.Event{e("a"), e("b"), e("a")}, seen, &mu)
	if len(got) != 2 {
		t.Fatalf("first call returned %d events, want 2", len(got))
	}
	if again := markNewEvents([]*github.Event{e("a"), e("b")}, seen, &mu); len(again) != 0 {
		t.Errorf("second call returned %d events, want 0", len(again))
	}
	if fresh := markNewEvents([]*github.Event{e("c")}, seen, &mu); len(fresh) != 1 {
		t.Errorf("new id returned %d events, want 1", len(fresh))
	}
}
