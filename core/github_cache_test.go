package core

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"testing"
	"time"

	"github.com/google/go-github/github"
)

// TestConditionalGETHandles304 pins the conditional-request behaviour: the
// first call gets a body and an ETag, and replaying that ETag must be reported
// as notModified rather than as an error, since a 304 is what makes a quiet
// poll free against the rate limit.
func TestConditionalGETHandles304(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("If-None-Match") == `"abc"` {
			w.WriteHeader(http.StatusNotModified)
			return
		}
		w.Header().Set("ETag", `"abc"`)
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, `[{"id":"1","type":"PushEvent"}]`)
	}))
	defer srv.Close()

	gh := github.NewClient(nil)
	base, err := url.Parse(srv.URL + "/")
	if err != nil {
		t.Fatal(err)
	}
	gh.BaseURL = base
	client := &GitHubClientWrapper{Client: gh}

	var events []*github.Event
	resp, notModified, err := conditionalGET(context.Background(), client, "events?per_page=100&page=1", "", &events)
	if err != nil {
		t.Fatalf("first request error: %v", err)
	}
	if notModified {
		t.Fatal("first request reported 304")
	}
	if len(events) != 1 || events[0].GetID() != "1" {
		t.Fatalf("first response decoded %d events (%v)", len(events), events)
	}
	etag := resp.Header.Get("ETag")
	if etag != `"abc"` {
		t.Fatalf("ETag = %q, want %q", etag, `"abc"`)
	}

	var again []*github.Event
	_, notModified, err = conditionalGET(context.Background(), client, "events?per_page=100&page=1", etag, &again)
	if err != nil {
		t.Fatalf("conditional request error: %v", err)
	}
	if !notModified {
		t.Fatal("replayed ETag did not report 304")
	}
}

func TestRepoCacheRoundTripAndExpiry(t *testing.T) {
	const id = int64(424242)

	if _, ok := cachedRepository(id); ok {
		t.Fatal("cache should start empty for this id")
	}

	repo := &github.Repository{ID: github.Int64(id)}
	cacheRepository(id, repo)

	got, ok := cachedRepository(id)
	if !ok || got != repo {
		t.Fatalf("cache miss after store: ok=%v got=%v", ok, got)
	}

	// An entry older than the TTL is not served.
	repoCacheMu.Lock()
	repoCache[id] = repoCacheEntry{repo: repo, at: time.Now().Add(-2 * repoCacheTTL)}
	repoCacheMu.Unlock()
	if _, ok := cachedRepository(id); ok {
		t.Error("expired entry was served")
	}
}
