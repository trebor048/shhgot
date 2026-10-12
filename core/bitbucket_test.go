package core

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

// testBitbucketRepo is the JSON shape the fake API writes.
type testBitbucketRepo struct {
	FullName   string `json:"full_name"`
	Slug       string `json:"slug"`
	IsPrivate  bool   `json:"is_private"`
	MainBranch struct {
		Name string `json:"name"`
	} `json:"mainbranch"`
	Links struct {
		HTML struct {
			Href string `json:"href"`
		} `json:"html"`
	} `json:"links"`
}

func newTestBitbucketRepo(fullName, slug, branch, htmlURL string, private bool) testBitbucketRepo {
	var r testBitbucketRepo
	r.FullName = fullName
	r.Slug = slug
	r.IsPrivate = private
	r.MainBranch.Name = branch
	r.Links.HTML.Href = htmlURL
	return r
}

func writeBitbucketRepos(w http.ResponseWriter, next string, repos ...testBitbucketRepo) {
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(struct {
		Values []testBitbucketRepo `json:"values"`
		Next   string              `json:"next"`
	}{Values: repos, Next: next})
}

// testBitbucketFile is the JSON shape the fake source listing writes.
type testBitbucketFile struct {
	Path  string `json:"path"`
	Type  string `json:"type"`
	Size  int    `json:"size"`
	Links struct {
		Self struct {
			Href string `json:"href"`
		} `json:"self"`
	} `json:"links"`
}

func newTestBitbucketFile(path, typ string, size int, raw string) testBitbucketFile {
	var f testBitbucketFile
	f.Path = path
	f.Type = typ
	f.Size = size
	f.Links.Self.Href = raw
	return f
}

func writeBitbucketFiles(w http.ResponseWriter, next string, files ...testBitbucketFile) {
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(struct {
		Values []testBitbucketFile `json:"values"`
		Next   string              `json:"next"`
	}{Values: files, Next: next})
}

func TestBitbucketListReposSkipsPrivateAndPaginates(t *testing.T) {
	var srv *httptest.Server
	var mu sync.Mutex
	var queries []string
	srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/2.0/repositories/ws" {
			http.NotFound(w, r)
			return
		}
		mu.Lock()
		queries = append(queries, r.URL.RawQuery)
		mu.Unlock()

		switch r.URL.Query().Get("page") {
		case "":
			writeBitbucketRepos(w, srv.URL+"/2.0/repositories/ws?page=2",
				newTestBitbucketRepo("ws/secret", "secret", "main", "https://bitbucket.org/ws/secret", true),
				newTestBitbucketRepo("ws/pub1", "pub1", "main", "https://bitbucket.org/ws/pub1", false),
			)
		case "2":
			writeBitbucketRepos(w, "",
				newTestBitbucketRepo("ws/pub2", "pub2", "develop", "https://bitbucket.org/ws/pub2", false),
			)
		default:
			http.NotFound(w, r)
		}
	}))
	defer srv.Close()

	c := NewBitbucketClient(srv.Client(), srv.URL)
	got, err := c.ListRepos("  ws  ", 0)
	if err != nil {
		t.Fatalf("ListRepos error: %v", err)
	}
	if len(got) != 2 {
		t.Fatalf("got %d repos, want 2: %+v", len(got), got)
	}
	if got[0].FullName != "ws/pub1" || got[0].Slug != "pub1" || got[0].MainBranch != "main" ||
		got[0].HTMLURL != "https://bitbucket.org/ws/pub1" || got[0].Private {
		t.Fatalf("unexpected first repo: %+v", got[0])
	}
	if got[1].FullName != "ws/pub2" || got[1].MainBranch != "develop" {
		t.Fatalf("unexpected second repo: %+v", got[1])
	}

	mu.Lock()
	gotQueries := append([]string(nil), queries...)
	mu.Unlock()
	if len(gotQueries) != 2 {
		t.Fatalf("server observed %d requests, want 2", len(gotQueries))
	}
	q, err := url.ParseQuery(gotQueries[0])
	if err != nil {
		t.Fatalf("parsing first query %q: %v", gotQueries[0], err)
	}
	if q.Get("pagelen") != "100" || q.Get("sort") != "-updated_on" {
		t.Fatalf("first query = %q, want pagelen=100 and sort=-updated_on", gotQueries[0])
	}
}

func TestBitbucketListReposCapsWithoutExtraRequest(t *testing.T) {
	var calls int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt32(&calls, 1)
		writeBitbucketRepos(w, "",
			newTestBitbucketRepo("ws/a", "a", "main", "", false),
			newTestBitbucketRepo("ws/b", "b", "main", "", false),
			newTestBitbucketRepo("ws/c", "c", "main", "", false),
		)
	}))
	defer srv.Close()

	c := NewBitbucketClient(srv.Client(), srv.URL)
	got, err := c.ListRepos("ws", 2)
	if err != nil {
		t.Fatalf("ListRepos error: %v", err)
	}
	if len(got) != 2 || got[0].Slug != "a" || got[1].Slug != "b" {
		t.Fatalf("got %+v, want the first two repos", got)
	}
	if n := atomic.LoadInt32(&calls); n != 1 {
		t.Fatalf("server observed %d requests, want 1", n)
	}
}

func TestBitbucketListReposNonOKReturnsError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
	}))
	defer srv.Close()

	c := NewBitbucketClient(srv.Client(), srv.URL)
	if _, err := c.ListRepos("ws", 0); err == nil {
		t.Fatal("ListRepos non-200 returned nil error, want error")
	}
}

func TestBitbucketListReposEmptyWorkspace(t *testing.T) {
	c := NewBitbucketClient(http.DefaultClient, "")
	if _, err := c.ListRepos("   ", 0); err == nil {
		t.Fatal("ListRepos empty workspace returned nil error, want error")
	}
}

func TestBitbucketListFilesKeepsFilesAndCaps(t *testing.T) {
	var srv *httptest.Server
	var mu sync.Mutex
	var query url.Values
	srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !strings.HasPrefix(r.URL.Path, "/2.0/repositories/ws/repo/src/") {
			http.NotFound(w, r)
			return
		}
		mu.Lock()
		query = r.URL.Query()
		mu.Unlock()
		writeBitbucketFiles(w, "",
			newTestBitbucketFile("README.md", "commit_file", 10, srv.URL+"/raw/README.md"),
			newTestBitbucketFile("src", "commit_directory", 0, ""),
			newTestBitbucketFile("src/main.go", "commit_file", 20, srv.URL+"/raw/src/main.go"),
			newTestBitbucketFile("src/other.go", "commit_file", 30, srv.URL+"/raw/src/other.go"),
		)
	}))
	defer srv.Close()

	c := NewBitbucketClient(srv.Client(), srv.URL)
	got, err := c.ListFiles("ws", "repo", "main", 0)
	if err != nil {
		t.Fatalf("ListFiles error: %v", err)
	}
	if len(got) != 3 {
		t.Fatalf("got %d files, want 3 (directory skipped): %+v", len(got), got)
	}
	if got[0].Path != "README.md" || got[0].Size != 10 || got[0].RawURL != srv.URL+"/raw/README.md" {
		t.Fatalf("unexpected first file: %+v", got[0])
	}
	if got[1].Path != "src/main.go" || got[1].Size != 20 || got[1].RawURL != srv.URL+"/raw/src/main.go" {
		t.Fatalf("unexpected second file: %+v", got[1])
	}
	if got[2].Path != "src/other.go" {
		t.Fatalf("unexpected third file: %+v", got[2])
	}

	mu.Lock()
	gotQuery := query
	mu.Unlock()
	if gotQuery.Get("recursive") != "true" || gotQuery.Get("pagelen") != "100" {
		t.Fatalf("query = %v, want recursive=true and pagelen=100", gotQuery)
	}
}

func TestBitbucketListFilesCaps(t *testing.T) {
	var calls int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt32(&calls, 1)
		writeBitbucketFiles(w, "",
			newTestBitbucketFile("a.env", "commit_file", 1, ""),
			newTestBitbucketFile("b.env", "commit_file", 2, ""),
			newTestBitbucketFile("c.env", "commit_file", 3, ""),
		)
	}))
	defer srv.Close()

	c := NewBitbucketClient(srv.Client(), srv.URL)
	got, err := c.ListFiles("ws", "repo", "main", 2)
	if err != nil {
		t.Fatalf("ListFiles error: %v", err)
	}
	if len(got) != 2 || got[0].Path != "a.env" || got[1].Path != "b.env" {
		t.Fatalf("got %+v, want the first two files", got)
	}
	if n := atomic.LoadInt32(&calls); n != 1 {
		t.Fatalf("server observed %d requests, want 1", n)
	}
}

func TestBitbucketFilesURLEncodesBranch(t *testing.T) {
	got, err := filesURL("https://api.bitbucket.org", "ws", "repo", "release/1.0")
	if err != nil {
		t.Fatalf("filesURL error: %v", err)
	}
	want := "https://api.bitbucket.org/2.0/repositories/ws/repo/src/release%2F1.0/?pagelen=100&recursive=true"
	if got != want {
		t.Fatalf("filesURL = %q, want %q", got, want)
	}
}

func TestBitbucketResolveNext(t *testing.T) {
	if got := resolveBitbucketNext("", "https://api.bitbucket.org"); got != "" {
		t.Fatalf("empty next = %q, want empty", got)
	}
	want := "https://api.bitbucket.org/rel?page=2"
	if got := resolveBitbucketNext("/rel?page=2", "https://api.bitbucket.org"); got != want {
		t.Fatalf("relative next = %q, want %q", got, want)
	}
	if got := resolveBitbucketNext("ftp://evil/x", "https://api.bitbucket.org"); got != "" {
		t.Fatalf("non-http next = %q, want empty", got)
	}
}

func TestBitbucketDefaultBaseURL(t *testing.T) {
	c := NewBitbucketClient(nil, "")
	if got := c.baseURL(); got != defaultBitbucketBaseURL {
		t.Fatalf("baseURL = %q, want %q", got, defaultBitbucketBaseURL)
	}
	if got := c.BaseURL; got != defaultBitbucketBaseURL {
		t.Fatalf("constructor BaseURL = %q, want %q", got, defaultBitbucketBaseURL)
	}
}

func TestBitbucketFileContentDownloadsAllowlistedHost(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		writeBody(w, "OPENAI_API_KEY=sk-test")
	}))
	defer srv.Close()

	c := NewBitbucketClient(srv.Client(), srv.URL)
	body, err := c.FileContent(srv.URL + "/2.0/repositories/ws/repo/src/main/creds.env")
	if err != nil {
		t.Fatalf("FileContent error: %v", err)
	}
	if string(body) != "OPENAI_API_KEY=sk-test" {
		t.Fatalf("FileContent = %q, want %q", body, "OPENAI_API_KEY=sk-test")
	}
}

func TestBitbucketFileContentRejectsOffAllowlistHost(t *testing.T) {
	var hits int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt32(&hits, 1)
		writeBody(w, "secret")
	}))
	defer srv.Close()

	c := NewBitbucketClient(srv.Client(), "https://api.bitbucket.org")
	if _, err := c.FileContent(srv.URL + "/raw/creds.env"); err == nil {
		t.Fatal("FileContent off-allowlist returned nil error, want error")
	}
	if n := atomic.LoadInt32(&hits); n != 0 {
		t.Fatalf("off-allowlist download issued %d requests, want 0", n)
	}
}

func TestBitbucketRateLimitSleepsAndRetries(t *testing.T) {
	var calls int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if atomic.AddInt32(&calls, 1) == 1 {
			w.Header().Set("Retry-After", "5")
			w.WriteHeader(http.StatusTooManyRequests)
			return
		}
		writeBitbucketRepos(w, "", newTestBitbucketRepo("ws/a", "a", "main", "", false))
	}))
	defer srv.Close()

	clk := newSeamClock()
	c := &BitbucketClient{Doer: srv.Client(), BaseURL: srv.URL, Now: clk.now, Sleep: clk.sleep}
	got, err := c.ListRepos("ws", 0)
	if err != nil {
		t.Fatalf("ListRepos error: %v", err)
	}
	if len(got) != 1 {
		t.Fatalf("got %d repos, want 1", len(got))
	}
	if sleeps := clk.sleepsCopy(); len(sleeps) != 1 || sleeps[0] != 5*time.Second {
		t.Fatalf("recorded sleeps = %v, want [5s]", sleeps)
	}
	if n := atomic.LoadInt32(&calls); n != 2 {
		t.Fatalf("server observed %d requests, want 2", n)
	}
}

func TestBitbucketForbiddenSleepsAndRetries(t *testing.T) {
	var calls int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if atomic.AddInt32(&calls, 1) == 1 {
			w.Header().Set("Retry-After", "3")
			w.WriteHeader(http.StatusForbidden)
			return
		}
		writeBitbucketFiles(w, "", newTestBitbucketFile("a.env", "commit_file", 1, ""))
	}))
	defer srv.Close()

	clk := newSeamClock()
	c := &BitbucketClient{Doer: srv.Client(), BaseURL: srv.URL, Now: clk.now, Sleep: clk.sleep}
	got, err := c.ListFiles("ws", "repo", "main", 0)
	if err != nil {
		t.Fatalf("ListFiles error: %v", err)
	}
	if len(got) != 1 {
		t.Fatalf("got %d files, want 1", len(got))
	}
	if sleeps := clk.sleepsCopy(); len(sleeps) != 1 || sleeps[0] != 3*time.Second {
		t.Fatalf("recorded sleeps = %v, want [3s]", sleeps)
	}
	if n := atomic.LoadInt32(&calls); n != 2 {
		t.Fatalf("server observed %d requests, want 2", n)
	}
}
