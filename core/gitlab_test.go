package core

import (
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

// testGitLabSnippet is the JSON shape the fake API writes.
type testGitLabSnippet struct {
	ID       int    `json:"id"`
	Title    string `json:"title"`
	FileName string `json:"file_name"`
	RawURL   string `json:"raw_url,omitempty"`
	WebURL   string `json:"web_url,omitempty"`
}

func writeGitLabSnippets(w http.ResponseWriter, snippets ...testGitLabSnippet) {
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(snippets)
}

func TestGitLabPublicSnippetsPagination(t *testing.T) {
	var mu sync.Mutex
	var pages []string
	var perPages []string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/v4/snippets/public" {
			http.NotFound(w, r)
			return
		}
		mu.Lock()
		pages = append(pages, r.URL.Query().Get("page"))
		perPages = append(perPages, r.URL.Query().Get("per_page"))
		mu.Unlock()

		switch r.URL.Query().Get("page") {
		case "1":
			w.Header().Set("X-Next-Page", "2")
			writeGitLabSnippets(w, testGitLabSnippet{ID: 11, Title: "first", FileName: "a.env"})
		case "2":
			writeGitLabSnippets(w, testGitLabSnippet{ID: 22, Title: "second", FileName: "b.env"})
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	defer srv.Close()

	c := &GitLabClient{Doer: srv.Client(), BaseURL: srv.URL, PerPage: 5, Now: time.Now}
	got, err := c.PublicSnippets(0)
	if err != nil {
		t.Fatalf("PublicSnippets error: %v", err)
	}
	if len(got) != 2 {
		t.Fatalf("got %d snippets, want 2: %+v", len(got), got)
	}
	if got[0].ID != 11 || got[0].Title != "first" || got[0].FileName != "a.env" {
		t.Fatalf("unexpected first snippet: %+v", got[0])
	}
	if got[1].ID != 22 || got[1].Title != "second" {
		t.Fatalf("unexpected second snippet: %+v", got[1])
	}
	if want := srv.URL + "/api/v4/snippets/11/raw"; got[0].RawURL != want {
		t.Fatalf("derived RawURL = %q, want %q", got[0].RawURL, want)
	}

	mu.Lock()
	gotPages := append([]string(nil), pages...)
	gotPerPages := append([]string(nil), perPages...)
	mu.Unlock()
	if !reflect.DeepEqual(gotPages, []string{"1", "2"}) {
		t.Fatalf("requested pages = %v, want [1 2]", gotPages)
	}
	for _, pp := range gotPerPages {
		if pp != "5" {
			t.Fatalf("per_page = %q, want 5", pp)
		}
	}
}

func TestGitLabSnippetContentDownload(t *testing.T) {
	var srv *httptest.Server
	srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/api/v4/snippets/public":
			writeGitLabSnippets(w, testGitLabSnippet{
				ID:       7,
				Title:    "leaky",
				FileName: "creds.env",
				RawURL:   srv.URL + "/api/v4/snippets/7/raw",
				WebURL:   srv.URL + "/snippets/7",
			})
		case "/api/v4/snippets/7/raw":
			writeBody(w, "OPENAI_API_KEY=sk-test")
		default:
			http.NotFound(w, r)
		}
	}))
	defer srv.Close()

	c := &GitLabClient{Doer: srv.Client(), BaseURL: srv.URL, PerPage: 5, Now: time.Now}
	snippets, err := c.PublicSnippets(0)
	if err != nil {
		t.Fatalf("PublicSnippets error: %v", err)
	}
	if len(snippets) != 1 {
		t.Fatalf("got %d snippets, want 1", len(snippets))
	}
	content, err := c.SnippetContent(snippets[0].RawURL)
	if err != nil {
		t.Fatalf("SnippetContent error: %v", err)
	}
	if string(content) != "OPENAI_API_KEY=sk-test" {
		t.Fatalf("SnippetContent = %q, want %q", content, "OPENAI_API_KEY=sk-test")
	}
}

func TestGitLabSnippetContentRejectsOffAllowlistHost(t *testing.T) {
	var hits int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt32(&hits, 1)
		writeBody(w, "secret")
	}))
	defer srv.Close()

	clk := newSeamClock()
	c := &GitLabClient{Doer: srv.Client(), BaseURL: "https://gitlab.com", Now: clk.now, Sleep: clk.sleep}
	if _, err := c.SnippetContent(srv.URL + "/api/v4/snippets/7/raw"); err == nil {
		t.Fatal("SnippetContent off-allowlist returned nil error, want error")
	}
	if got := atomic.LoadInt32(&hits); got != 0 {
		t.Fatalf("off-allowlist download issued %d requests, want 0", got)
	}
}

func TestGitLabRateLimitSleepsAndRetries(t *testing.T) {
	var calls int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if atomic.AddInt32(&calls, 1) == 1 {
			w.Header().Set("Retry-After", "5")
			w.WriteHeader(http.StatusTooManyRequests)
			return
		}
		writeGitLabSnippets(w, testGitLabSnippet{ID: 1, Title: "after-backoff"})
	}))
	defer srv.Close()

	clk := newSeamClock()
	c := &GitLabClient{Doer: srv.Client(), BaseURL: srv.URL, PerPage: 5, Now: clk.now, Sleep: clk.sleep}
	got, err := c.PublicSnippets(0)
	if err != nil {
		t.Fatalf("PublicSnippets error: %v", err)
	}
	if len(got) != 1 {
		t.Fatalf("got %d snippets, want 1", len(got))
	}
	if gotSleeps := clk.sleepsCopy(); len(gotSleeps) != 1 || gotSleeps[0] != 5*time.Second {
		t.Fatalf("recorded sleeps = %v, want [5s]", gotSleeps)
	}
	if n := atomic.LoadInt32(&calls); n != 2 {
		t.Fatalf("server observed %d requests, want 2", n)
	}
}

func TestGitLabNonOKEndsPagination(t *testing.T) {
	var mu sync.Mutex
	var pages []string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		page := r.URL.Query().Get("page")
		mu.Lock()
		pages = append(pages, page)
		mu.Unlock()
		switch page {
		case "1":
			w.Header().Set("X-Next-Page", "2")
			writeGitLabSnippets(w, testGitLabSnippet{ID: 1, Title: "only"})
		default:
			w.WriteHeader(http.StatusInternalServerError)
		}
	}))
	defer srv.Close()

	c := &GitLabClient{Doer: srv.Client(), BaseURL: srv.URL, PerPage: 5, Now: time.Now}
	got, err := c.PublicSnippets(0)
	if err != nil {
		t.Fatalf("PublicSnippets returned error on a non-200 page, want clean end: %v", err)
	}
	if len(got) != 1 || got[0].ID != 1 {
		t.Fatalf("got %+v, want the one snippet from page 1", got)
	}
	mu.Lock()
	gotPages := append([]string(nil), pages...)
	mu.Unlock()
	if !reflect.DeepEqual(gotPages, []string{"1", "2"}) {
		t.Fatalf("requested pages = %v, want [1 2]", gotPages)
	}
}

func TestGitLabPublicSnippetsLimit(t *testing.T) {
	var calls int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt32(&calls, 1)
		writeGitLabSnippets(w,
			testGitLabSnippet{ID: 1, Title: "one"},
			testGitLabSnippet{ID: 2, Title: "two"},
			testGitLabSnippet{ID: 3, Title: "three"},
		)
	}))
	defer srv.Close()

	c := &GitLabClient{Doer: srv.Client(), BaseURL: srv.URL, PerPage: 5, Now: time.Now}
	got, err := c.PublicSnippets(1)
	if err != nil {
		t.Fatalf("PublicSnippets error: %v", err)
	}
	if len(got) != 1 || got[0].ID != 1 {
		t.Fatalf("got %+v, want the first snippet only", got)
	}
	if n := atomic.LoadInt32(&calls); n != 1 {
		t.Fatalf("server observed %d requests, want 1", n)
	}
}

// testGitLabBlob is the JSON blob shape the fake API writes.
type testGitLabBlob struct {
	Path      string `json:"path"`
	Filename  string `json:"filename"`
	Ref       string `json:"ref"`
	Data      string `json:"data"`
	StartLine int    `json:"startline"`
	ProjectID int    `json:"project_id"`
}

func writeGitLabBlobs(w http.ResponseWriter, blobs ...testGitLabBlob) {
	w.Header().Set("Content-Type", "application/json")
	if blobs == nil {
		blobs = []testGitLabBlob{}
	}
	_ = json.NewEncoder(w).Encode(blobs)
}

func TestGitLabSearchBlobsParses(t *testing.T) {
	var mu sync.Mutex
	var path, scope, search, perPage, page, token string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		path = r.URL.Path
		scope = r.URL.Query().Get("scope")
		search = r.URL.Query().Get("search")
		perPage = r.URL.Query().Get("per_page")
		page = r.URL.Query().Get("page")
		token = r.Header.Get("PRIVATE-TOKEN")
		mu.Unlock()
		writeGitLabBlobs(w, testGitLabBlob{
			Path:      "config/secrets.yml",
			Filename:  "secrets.yml",
			Ref:       "main",
			Data:      "aws_key: AKIAEXAMPLE",
			StartLine: 12,
			ProjectID: 42,
		})
	}))
	defer srv.Close()

	c := &GitLabClient{Doer: srv.Client(), BaseURL: srv.URL, PerPage: 5, Token: "glpat-secret", Now: time.Now}
	got, err := c.SearchBlobs("AKIA", 5, 2)
	if err != nil {
		t.Fatalf("SearchBlobs error: %v", err)
	}
	want := GitLabBlob{
		Path:      "config/secrets.yml",
		Filename:  "secrets.yml",
		Ref:       "main",
		Data:      "aws_key: AKIAEXAMPLE",
		StartLine: 12,
		ProjectID: 42,
	}
	if len(got) != 1 || !reflect.DeepEqual(got[0], want) {
		t.Fatalf("blobs = %+v, want exactly [%+v]", got, want)
	}

	mu.Lock()
	defer mu.Unlock()
	if path != "/api/v4/search" || scope != "blobs" || search != "AKIA" || perPage != "5" || page != "2" {
		t.Fatalf("request path=%q scope=%q search=%q per_page=%q page=%q", path, scope, search, perPage, page)
	}
	if token != "glpat-secret" {
		t.Fatalf("PRIVATE-TOKEN = %q, want glpat-secret", token)
	}
}

func TestGitLabSearchBlobsNoTokenHeader(t *testing.T) {
	var hadHeader bool
	var token string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, hadHeader = r.Header["Private-Token"]
		token = r.Header.Get("PRIVATE-TOKEN")
		writeGitLabBlobs(w)
	}))
	defer srv.Close()

	c := &GitLabClient{Doer: srv.Client(), BaseURL: srv.URL, PerPage: 5, Now: time.Now}
	if _, err := c.SearchBlobs("x", 5, 1); err != nil {
		t.Fatalf("SearchBlobs error: %v", err)
	}
	if hadHeader || token != "" {
		t.Fatalf("PRIVATE-TOKEN header sent with empty token: %q", token)
	}
}

func TestGitLabSearchBlobsAllPaginatesAndDedupes(t *testing.T) {
	var mu sync.Mutex
	var pages []string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		p := r.URL.Query().Get("page")
		mu.Lock()
		pages = append(pages, p)
		mu.Unlock()
		switch p {
		case "1":
			writeGitLabBlobs(w,
				testGitLabBlob{Path: "a.env", Ref: "main", Data: "one"},
				testGitLabBlob{Path: "b.env", Ref: "main", Data: "two"},
			)
		case "2":
			writeGitLabBlobs(w,
				testGitLabBlob{Path: "a.env", Ref: "main", Data: "duplicate"},
				testGitLabBlob{Path: "c.env", Ref: "dev", Data: "three"},
			)
		default:
			writeGitLabBlobs(w)
		}
	}))
	defer srv.Close()

	c := &GitLabClient{Doer: srv.Client(), BaseURL: srv.URL, PerPage: 2, Now: time.Now}
	got, err := c.SearchBlobsAll("KEY", 5)
	if err != nil {
		t.Fatalf("SearchBlobsAll error: %v", err)
	}
	keys := make([]string, 0, len(got))
	for _, b := range got {
		keys = append(keys, b.Path+"/"+b.Ref)
	}
	if want := []string{"a.env/main", "b.env/main", "c.env/dev"}; !reflect.DeepEqual(keys, want) {
		t.Fatalf("blobs = %v, want %v", keys, want)
	}
	mu.Lock()
	gotPages := append([]string(nil), pages...)
	mu.Unlock()
	if want := []string{"1", "2", "3"}; !reflect.DeepEqual(gotPages, want) {
		t.Fatalf("requested pages = %v, want %v", gotPages, want)
	}
}

func TestGitLabSearchBlobsNonOKErrors(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
	}))
	defer srv.Close()

	c := &GitLabClient{Doer: srv.Client(), BaseURL: srv.URL, PerPage: 5, Now: time.Now}
	if _, err := c.SearchBlobs("x", 5, 1); err == nil {
		t.Fatal("SearchBlobs non-200 returned nil error, want error")
	}
}

func TestGitLabSearchBlobsRateLimitBackoff(t *testing.T) {
	var calls int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if atomic.AddInt32(&calls, 1) == 1 {
			w.Header().Set("Retry-After", "3")
			w.WriteHeader(http.StatusTooManyRequests)
			return
		}
		writeGitLabBlobs(w, testGitLabBlob{Path: "after", Ref: "main"})
	}))
	defer srv.Close()

	clk := newSeamClock()
	c := &GitLabClient{Doer: srv.Client(), BaseURL: srv.URL, PerPage: 5, Now: clk.now, Sleep: clk.sleep}
	got, err := c.SearchBlobs("x", 5, 1)
	if err != nil {
		t.Fatalf("SearchBlobs error: %v", err)
	}
	if len(got) != 1 || got[0].Path != "after" {
		t.Fatalf("got %+v, want the post-backoff blob", got)
	}
	if sleeps := clk.sleepsCopy(); len(sleeps) != 1 || sleeps[0] != 3*time.Second {
		t.Fatalf("recorded sleeps = %v, want [3s]", sleeps)
	}
	if n := atomic.LoadInt32(&calls); n != 2 {
		t.Fatalf("server observed %d requests, want 2", n)
	}
}

func TestGitLabSearchBlobsRateLimitGivesUpAfterRetries(t *testing.T) {
	var calls int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt32(&calls, 1)
		w.Header().Set("Retry-After", "1")
		w.WriteHeader(http.StatusTooManyRequests)
	}))
	defer srv.Close()

	clk := newSeamClock()
	c := &GitLabClient{Doer: srv.Client(), BaseURL: srv.URL, Now: clk.now, Sleep: clk.sleep}
	if _, err := c.SearchBlobs("x", 5, 1); err == nil {
		t.Fatal("SearchBlobs persistent 429 returned nil error, want error")
	}
	if n := atomic.LoadInt32(&calls); n != gitlabRateLimitRetries+1 {
		t.Fatalf("persistent 429 issued %d requests, want %d", n, gitlabRateLimitRetries+1)
	}
	if sleeps := clk.sleepsCopy(); len(sleeps) != gitlabRateLimitRetries {
		t.Fatalf("persistent 429 slept %d times, want %d", len(sleeps), gitlabRateLimitRetries)
	}
}

func TestGitLabSearchBlobsTierForbiddenSurfacesError(t *testing.T) {
	var calls int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt32(&calls, 1)
		w.Header().Set("Retry-After", "1")
		w.WriteHeader(http.StatusForbidden)
		writeBody(w, `{"message":"403 Forbidden - advanced search (exact code search) requires GitLab Premium or Ultimate"}`)
	}))
	defer srv.Close()

	clk := newSeamClock()
	c := &GitLabClient{Doer: srv.Client(), BaseURL: srv.URL, Now: clk.now, Sleep: clk.sleep}
	_, err := c.SearchBlobs("x", 5, 1)
	if err == nil {
		t.Fatal("SearchBlobs tier 403 returned nil error, want error")
	}
	if !errors.Is(err, ErrGitLabSearchUnavailable) {
		t.Fatalf("error = %v, want ErrGitLabSearchUnavailable", err)
	}
	if !strings.Contains(err.Error(), "advanced search") {
		t.Fatalf("error %q does not surface the tier message", err)
	}
	if n := atomic.LoadInt32(&calls); n != 1 {
		t.Fatalf("tier 403 issued %d requests, want 1 (no retry)", n)
	}
	if sleeps := clk.sleepsCopy(); len(sleeps) != 0 {
		t.Fatalf("tier 403 slept %v, want none", sleeps)
	}
}

func TestGitLabSearchBlobsForbiddenWithoutRetryAfterErrors(t *testing.T) {
	var calls int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt32(&calls, 1)
		w.WriteHeader(http.StatusForbidden)
		writeBody(w, `{"message":"403 Forbidden"}`)
	}))
	defer srv.Close()

	clk := newSeamClock()
	c := &GitLabClient{Doer: srv.Client(), BaseURL: srv.URL, Now: clk.now, Sleep: clk.sleep}
	if _, err := c.SearchBlobs("x", 5, 1); err == nil {
		t.Fatal("SearchBlobs 403 without Retry-After returned nil error, want error")
	}
	if n := atomic.LoadInt32(&calls); n != 1 {
		t.Fatalf("403 without Retry-After issued %d requests, want 1 (no retry)", n)
	}
	if sleeps := clk.sleepsCopy(); len(sleeps) != 0 {
		t.Fatalf("403 without Retry-After slept %v, want none", sleeps)
	}
}
