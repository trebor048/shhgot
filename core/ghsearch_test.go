package core

import (
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

// gitHubCommitPage builds a commit-search page whose items are sha<start>..
// sha<start+n-1>.
func gitHubCommitPage(start, n int) string {
	var b strings.Builder
	b.WriteString(`{"total_count":999,"incomplete_results":false,"items":[`)
	for i := 0; i < n; i++ {
		if i > 0 {
			b.WriteByte(',')
		}
		sha := start + i
		fmt.Fprintf(&b, `{"sha":"sha%d","html_url":"https://github.com/o/r/commit/sha%d","commit":{"message":"msg%d","author":{"date":"2025-02-03T04:05:06Z"}},"repository":{"full_name":"o/r"}}`, sha, sha, sha)
	}
	b.WriteString(`]}`)
	return b.String()
}

func TestGitHubSearchCommitsParsesResults(t *testing.T) {
	const body = `{"total_count":1,"incomplete_results":false,"items":[{"sha":"abc","html_url":"https://github.com/o/r/commit/abc","commit":{"message":"add key","author":{"date":"2025-02-03T04:05:06Z"}},"repository":{"full_name":"o/r"}}]}`
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/search/commits" {
			t.Errorf("path = %q, want /search/commits", r.URL.Path)
		}
		q := r.URL.Query()
		if q.Get("q") != "AKIA" {
			t.Errorf("q = %q, want AKIA", q.Get("q"))
		}
		if q.Get("per_page") != "100" {
			t.Errorf("per_page = %q, want 100", q.Get("per_page"))
		}
		if q.Get("page") != "1" {
			t.Errorf("page = %q, want 1", q.Get("page"))
		}
		if got := r.Header.Get("Accept"); got != "application/vnd.github+json" {
			t.Errorf("Accept = %q", got)
		}
		if got := r.Header.Get("Authorization"); got != "Bearer tok" {
			t.Errorf("Authorization = %q", got)
		}
		_, _ = io.WriteString(w, body)
	}))
	defer srv.Close()

	c := NewGitHubSearchClient(srv.Client(), srv.URL, "tok")
	hits, err := c.SearchCommits("AKIA", 10)
	if err != nil {
		t.Fatalf("SearchCommits() error = %v", err)
	}
	if len(hits) != 1 {
		t.Fatalf("len(hits) = %d, want 1", len(hits))
	}
	got := hits[0]
	if got.SHA != "abc" || got.Repo != "o/r" || got.Message != "add key" {
		t.Errorf("unexpected hit fields: %+v", got)
	}
	if got.HTMLURL != "https://github.com/o/r/commit/abc" || got.Date != "2025-02-03T04:05:06Z" {
		t.Errorf("unexpected hit fields: %+v", got)
	}
}

func TestGitHubSearchIssuesParsesResults(t *testing.T) {
	const body = `{"total_count":1,"incomplete_results":false,"items":[{"html_url":"https://github.com/acme/tool/issues/7","title":"leaked key","body":"AKIAIOSFODNN7EXAMPLE","repository_url":"https://api.github.com/repos/acme/tool","user":{"login":"alice"},"created_at":"2025-03-04T05:06:07Z"}]}`
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/search/issues" {
			t.Errorf("path = %q, want /search/issues", r.URL.Path)
		}
		_, _ = io.WriteString(w, body)
	}))
	defer srv.Close()

	c := NewGitHubSearchClient(srv.Client(), srv.URL, "tok")
	hits, err := c.SearchIssues("AKIA", 10)
	if err != nil {
		t.Fatalf("SearchIssues() error = %v", err)
	}
	if len(hits) != 1 {
		t.Fatalf("len(hits) = %d, want 1", len(hits))
	}
	got := hits[0]
	if got.HTMLURL != "https://github.com/acme/tool/issues/7" || got.Title != "leaked key" || got.Body != "AKIAIOSFODNN7EXAMPLE" {
		t.Errorf("unexpected hit fields: %+v", got)
	}
	if got.Repo != "acme/tool" || got.Author != "alice" || got.CreatedAt != "2025-03-04T05:06:07Z" {
		t.Errorf("unexpected hit fields: %+v", got)
	}
}

func TestGitHubSearchCommitsPaginatesAndCaps(t *testing.T) {
	var calls atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		switch r.URL.Query().Get("page") {
		case "1":
			_, _ = io.WriteString(w, gitHubCommitPage(1, 100))
		case "2":
			_, _ = io.WriteString(w, gitHubCommitPage(101, 100))
		default:
			t.Errorf("unexpected page %q", r.URL.Query().Get("page"))
		}
	}))
	defer srv.Close()

	c := NewGitHubSearchClient(srv.Client(), srv.URL, "tok")
	hits, err := c.SearchCommits("q", 150)
	if err != nil {
		t.Fatalf("SearchCommits() error = %v", err)
	}
	if len(hits) != 150 {
		t.Fatalf("len(hits) = %d, want 150 (cap at maxResults)", len(hits))
	}
	if hits[149].SHA != "sha150" {
		t.Errorf("hits[149].SHA = %q, want sha150", hits[149].SHA)
	}
	if got := calls.Load(); got != 2 {
		t.Fatalf("requests = %d, want 2", got)
	}
}

func TestGitHubSearchStopsOnEmptyPage(t *testing.T) {
	var calls atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		switch r.URL.Query().Get("page") {
		case "1":
			_, _ = io.WriteString(w, gitHubCommitPage(1, 100))
		case "2":
			_, _ = io.WriteString(w, `{"total_count":999,"incomplete_results":false,"items":[]}`)
		default:
			t.Errorf("unexpected page %q", r.URL.Query().Get("page"))
		}
	}))
	defer srv.Close()

	c := NewGitHubSearchClient(srv.Client(), srv.URL, "tok")
	hits, err := c.SearchCommits("q", 500)
	if err != nil {
		t.Fatalf("SearchCommits() error = %v", err)
	}
	if len(hits) != 100 {
		t.Fatalf("len(hits) = %d, want 100", len(hits))
	}
	if got := calls.Load(); got != 2 {
		t.Fatalf("requests = %d, want 2 (page 2 empty)", got)
	}
}

func TestGitHubSearchIncompleteResultsStopsPaging(t *testing.T) {
	var calls atomic.Int32
	page := strings.Replace(gitHubCommitPage(1, 100), `"incomplete_results":false`, `"incomplete_results":true`, 1)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		if r.URL.Query().Get("page") != "1" {
			t.Errorf("unexpected page %q; paging must stop on incomplete_results", r.URL.Query().Get("page"))
		}
		_, _ = io.WriteString(w, page)
	}))
	defer srv.Close()

	c := NewGitHubSearchClient(srv.Client(), srv.URL, "tok")
	hits, err := c.SearchCommits("q", 500)
	if err != nil {
		t.Fatalf("SearchCommits() error = %v", err)
	}
	if len(hits) != 100 {
		t.Fatalf("len(hits) = %d, want 100", len(hits))
	}
	if got := calls.Load(); got != 1 {
		t.Fatalf("requests = %d, want 1", got)
	}
}

func TestGitHubSearchBacksOffOn429(t *testing.T) {
	doer := &scriptedDoer{responses: []*http.Response{
		{
			StatusCode: http.StatusTooManyRequests,
			Header:     http.Header{"Retry-After": []string{"3"}},
			Body:       io.NopCloser(strings.NewReader("")),
		},
		{
			StatusCode: http.StatusOK,
			Header:     http.Header{},
			Body:       io.NopCloser(strings.NewReader(`{"total_count":1,"incomplete_results":false,"items":[{"sha":"sha1","html_url":"u","commit":{"message":"m","author":{"date":"d"}},"repository":{"full_name":"o/r"}}]}`)),
		},
	}}

	rec := &sleepRecorder{}
	c := NewGitHubSearchClient(doer, "https://api.github.com", "tok")
	c.Sleep = rec.record

	hits, err := c.SearchCommits("q", 10)
	if err != nil {
		t.Fatalf("SearchCommits() error = %v", err)
	}
	if len(hits) != 1 {
		t.Fatalf("len(hits) = %d, want 1 after retry", len(hits))
	}
	if doer.calls != 2 {
		t.Fatalf("doer calls = %d, want 2", doer.calls)
	}
	sleeps := rec.durations()
	if len(sleeps) != 1 || sleeps[0] != 3*time.Second {
		t.Fatalf("sleeps = %v, want one 3s backoff", sleeps)
	}
}

func TestGitHubSearchMissingTokenMakesNoRequests(t *testing.T) {
	doer := &scriptedDoer{responses: []*http.Response{{
		StatusCode: http.StatusOK,
		Header:     http.Header{},
		Body:       io.NopCloser(strings.NewReader(`{"items":[]}`)),
	}}}
	c := NewGitHubSearchClient(doer, "https://api.github.com", "")
	if _, err := c.SearchCommits("q", 10); err == nil {
		t.Fatal("SearchCommits() error = nil, want missing-token error")
	}
	if _, err := c.SearchIssues("q", 10); err == nil {
		t.Fatal("SearchIssues() error = nil, want missing-token error")
	}
	if doer.calls != 0 {
		t.Fatalf("doer calls = %d, want 0", doer.calls)
	}
}

// ghCaptureDoer records the request it was handed and returns a fixed response.
type ghCaptureDoer struct {
	req  *http.Request
	resp *http.Response
}

func (d *ghCaptureDoer) Do(r *http.Request) (*http.Response, error) {
	d.req = r
	return d.resp, nil
}

func TestGitHubSearchInjectedOffHostBase(t *testing.T) {
	doer := &ghCaptureDoer{resp: &http.Response{
		StatusCode: http.StatusOK,
		Header:     http.Header{},
		Body:       io.NopCloser(strings.NewReader(gitHubCommitPage(1, 1))),
	}}
	c := NewGitHubSearchClient(doer, "https://github.example.com/api/v3", "tok")
	hits, err := c.SearchCommits("q", 10)
	if err != nil {
		t.Fatalf("SearchCommits() error = %v", err)
	}
	if len(hits) != 1 {
		t.Fatalf("len(hits) = %d, want 1", len(hits))
	}
	if doer.req == nil {
		t.Fatal("doer captured no request")
	}
	if got := doer.req.URL.Host; got != "github.example.com" {
		t.Errorf("host = %q, want github.example.com", got)
	}
	if got := doer.req.URL.Path; got != "/api/v3/search/commits" {
		t.Errorf("path = %q, want /api/v3/search/commits", got)
	}
}
