package core

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

const grepAppSnippetFixture = `<table class="highlight-table"><tbody>` +
	`<tr data-line="10"><td class="line-number"><a href="#10">10</a></td>` +
	`<td class="line-content"><div class="highlight"><pre><span class="k">const</span> <span class="nx">apiKey</span> <span class="o">=</span> <span class="s2">"<mark>sk-proj-abc123</mark>"</span><span class="p">;</span></pre></div></td></tr>` +
	`<tr data-line="11"><td class="line-number"><a href="#11">11</a></td>` +
	`<td class="line-content"><div class="highlight"><pre><span class="k">if</span> (<span class="nx">a</span> &lt; <mark><span class="nx">b</span></mark> &amp;&amp; <span class="nx">c</span> &gt; <span class="nx">d</span>) {</pre></div></td></tr>` +
	`<tr data-line="12"><td class="line-number"><a href="#12">12</a></td>` +
	`<td class="line-content"><div class="highlight"><pre>  <span class="c1">// entity: &quot;quoted&quot; &amp; done</span></pre></div></td></tr>` +
	`</tbody></table>`

const grepAppSnippetCode = "const apiKey = \"sk-proj-abc123\";\n" +
	"if (a < b && c > d) {\n" +
	"// entity: \"quoted\" & done"

func grepAppHitWire(repo, branch, path, snippet string) grepAppWireHit {
	h := grepAppWireHit{Repo: repo, Branch: branch, Path: path}
	h.Content.Snippet = snippet
	return h
}

func grepAppPage(hits ...grepAppWireHit) string {
	payload := grepAppResponse{Hits: grepAppHits{Total: len(hits), Hits: hits}}
	b, err := json.Marshal(payload)
	if err != nil {
		panic(err)
	}
	return string(b)
}

func TestGrepAppExtractSnippet(t *testing.T) {
	code, lines := extractGrepAppSnippet(grepAppSnippetFixture)
	if code != grepAppSnippetCode {
		t.Fatalf("code = %q, want %q", code, grepAppSnippetCode)
	}
	if want := []int{10, 11, 12}; !reflect.DeepEqual(lines, want) {
		t.Fatalf("lines = %v, want %v", lines, want)
	}
}

func TestGrepAppExtractSnippetWithoutLineRows(t *testing.T) {
	code, lines := extractGrepAppSnippet(`<pre>hello &amp; <mark>bye</mark></pre>`)
	if code != "hello & bye" {
		t.Fatalf("code = %q, want %q", code, "hello & bye")
	}
	if lines != nil {
		t.Fatalf("lines = %v, want nil", lines)
	}
	if code, lines := extractGrepAppSnippet(""); code != "" || lines != nil {
		t.Fatalf("empty snippet = (%q, %v), want (\"\", nil)", code, lines)
	}
}

func TestGrepAppSearchParsesFixture(t *testing.T) {
	var rawQuery, ua, accept string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		rawQuery = r.URL.RawQuery
		ua = r.Header.Get("User-Agent")
		accept = r.Header.Get("Accept")
		if r.URL.Query().Get("page") != "1" {
			writeBody(w, grepAppPage())
			return
		}
		writeBody(w, grepAppPage(grepAppHitWire("owner/name", "main", "a/b.go", grepAppSnippetFixture)))
	}))
	defer srv.Close()

	c := NewGrepAppClient(srv.Client(), srv.URL)
	got, err := c.Search("sk-proj", 1)
	if err != nil {
		t.Fatalf("Search error: %v", err)
	}
	if len(got) != 1 {
		t.Fatalf("got %d hits, want 1: %+v", len(got), got)
	}
	want := GrepAppHit{
		Repo:   "owner/name",
		Branch: "main",
		Path:   "a/b.go",
		Code:   grepAppSnippetCode,
		Lines:  []int{10, 11, 12},
	}
	if !reflect.DeepEqual(got[0], want) {
		t.Fatalf("got[0] = %+v, want %+v", got[0], want)
	}

	q := strings.Split(rawQuery, "&")
	params := map[string]string{}
	for _, part := range q {
		kv := strings.SplitN(part, "=", 2)
		if len(kv) == 2 {
			params[kv[0]] = kv[1]
		}
	}
	if params["q"] != "sk-proj" {
		t.Fatalf("q = %q, want sk-proj (raw %q)", params["q"], rawQuery)
	}
	if params["page"] != "1" {
		t.Fatalf("page = %q, want 1 (raw %q)", params["page"], rawQuery)
	}
	if ua != "shhgit2" {
		t.Fatalf("User-Agent = %q, want shhgit2", ua)
	}
	if accept != "application/json" {
		t.Fatalf("Accept = %q, want application/json", accept)
	}
}

func TestGrepAppSearchPaginatesAndCaps(t *testing.T) {
	var calls int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt32(&calls, 1)
		switch r.URL.Query().Get("page") {
		case "1":
			writeBody(w, grepAppPage(
				grepAppHitWire("o/r1", "main", "f1.go", grepAppSnippetFixture),
				grepAppHitWire("o/r2", "main", "f2.go", grepAppSnippetFixture),
			))
		case "2":
			writeBody(w, grepAppPage(
				grepAppHitWire("o/r3", "main", "f3.go", grepAppSnippetFixture),
				grepAppHitWire("o/r4", "main", "f4.go", grepAppSnippetFixture),
			))
		default:
			writeBody(w, grepAppPage())
		}
	}))
	defer srv.Close()

	c := NewGrepAppClient(srv.Client(), srv.URL)
	capped, err := c.Search("q", 3)
	if err != nil {
		t.Fatalf("capped Search error: %v", err)
	}
	if len(capped) != 3 {
		t.Fatalf("capped hits = %d, want 3", len(capped))
	}
	if capped[2].Repo != "o/r3" {
		t.Fatalf("capped[2].Repo = %q, want o/r3", capped[2].Repo)
	}
	if n := atomic.LoadInt32(&calls); n != 2 {
		t.Fatalf("capped search made %d requests, want 2", n)
	}

	atomic.StoreInt32(&calls, 0)
	all, err := c.Search("q", 10)
	if err != nil {
		t.Fatalf("uncapped Search error: %v", err)
	}
	if len(all) != 4 {
		t.Fatalf("uncapped hits = %d, want 4", len(all))
	}
	if n := atomic.LoadInt32(&calls); n != 3 {
		t.Fatalf("uncapped search made %d requests, want 3 (empty page stops)", n)
	}
}

// TestGrepAppSearchStopsAtMaxPages proves a server that never returns an
// empty page cannot trap the client in an infinite walk when maxResults <= 0.
func TestGrepAppSearchStopsAtMaxPages(t *testing.T) {
	var calls int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt32(&calls, 1)
		writeBody(w, grepAppPage(
			grepAppHitWire("o/r", "main", "f.go", grepAppSnippetFixture),
		))
	}))
	defer srv.Close()

	c := NewGrepAppClient(srv.Client(), srv.URL)
	got, err := c.Search("q", 0)
	if err != nil {
		t.Fatalf("Search error: %v", err)
	}
	if n := atomic.LoadInt32(&calls); n != maxGrepAppPages {
		t.Fatalf("requests = %d, want exactly maxGrepAppPages (%d)", n, maxGrepAppPages)
	}
	if len(got) != maxGrepAppPages {
		t.Fatalf("hits = %d, want %d (one per page)", len(got), maxGrepAppPages)
	}
}

func TestGrepAppSearchEmptyFirstPage(t *testing.T) {
	var calls int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt32(&calls, 1)
		writeBody(w, grepAppPage())
	}))
	defer srv.Close()

	c := NewGrepAppClient(srv.Client(), srv.URL)
	got, err := c.Search("q", 10)
	if err != nil {
		t.Fatalf("Search error: %v", err)
	}
	if len(got) != 0 {
		t.Fatalf("got %d hits, want 0", len(got))
	}
	if n := atomic.LoadInt32(&calls); n != 1 {
		t.Fatalf("empty search made %d requests, want 1", n)
	}
}

func TestGrepAppRateLimitSleepsAndRetries(t *testing.T) {
	for _, status := range []int{http.StatusTooManyRequests, http.StatusForbidden} {
		t.Run(http.StatusText(status), func(t *testing.T) {
			var calls int32
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if atomic.AddInt32(&calls, 1) == 1 {
					w.Header().Set("Retry-After", "5")
					w.WriteHeader(status)
					return
				}
				if r.URL.Query().Get("page") != "1" {
					writeBody(w, grepAppPage())
					return
				}
				writeBody(w, grepAppPage(grepAppHitWire("o/r", "main", "f.go", grepAppSnippetFixture)))
			}))
			defer srv.Close()

			clk := newSeamClock()
			c := &GrepAppClient{Doer: srv.Client(), BaseURL: srv.URL, Now: clk.now, Sleep: clk.sleep}
			got, err := c.Search("q", 1)
			if err != nil {
				t.Fatalf("Search error: %v", err)
			}
			if len(got) != 1 {
				t.Fatalf("got %d hits, want 1", len(got))
			}
			if sleeps := clk.sleepsCopy(); len(sleeps) != 1 || sleeps[0] != 5*time.Second {
				t.Fatalf("recorded sleeps = %v, want [5s]", sleeps)
			}
			if n := atomic.LoadInt32(&calls); n != 2 {
				t.Fatalf("server observed %d requests, want 2", n)
			}
		})
	}
}

func TestGrepAppRateLimitExhaustsAndErrors(t *testing.T) {
	var calls int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt32(&calls, 1)
		w.Header().Set("Retry-After", "3")
		w.WriteHeader(http.StatusTooManyRequests)
	}))
	defer srv.Close()

	clk := newSeamClock()
	c := &GrepAppClient{Doer: srv.Client(), BaseURL: srv.URL, Now: clk.now, Sleep: clk.sleep}
	got, err := c.Search("q", 10)
	if err == nil {
		t.Fatal("Search on a persistent 429 returned nil error, want error")
	}
	if got != nil {
		t.Fatalf("Search returned %+v, want nil hits", got)
	}
	if want := int32(grepAppRateLimitRetries + 1); atomic.LoadInt32(&calls) != want {
		t.Fatalf("server observed %d requests, want %d", calls, want)
	}
	if sleeps := clk.sleepsCopy(); len(sleeps) != grepAppRateLimitRetries {
		t.Fatalf("recorded sleeps = %v, want %d entries", sleeps, grepAppRateLimitRetries)
	}
}

func TestGrepAppSearchNon200Errors(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
	}))
	defer srv.Close()

	c := NewGrepAppClient(srv.Client(), srv.URL)
	got, err := c.Search("q", 10)
	if err == nil {
		t.Fatal("Search on a 500 returned nil error, want error")
	}
	if got != nil {
		t.Fatalf("Search on a 500 returned %+v, want nil hits", got)
	}
}

func TestGrepAppBlobURL(t *testing.T) {
	c := NewGrepAppClient(http.DefaultClient, "")
	got := c.BlobURL(GrepAppHit{Repo: "owner/name", Branch: "main", Path: "a/b.go"})
	if want := "https://github.com/owner/name/blob/main/a/b.go"; got != want {
		t.Fatalf("BlobURL = %q, want %q", got, want)
	}
	got = c.BlobURL(GrepAppHit{Repo: "/owner/name/", Branch: "main", Path: "/a/b.go"})
	if want := "https://github.com/owner/name/blob/main/a/b.go"; got != want {
		t.Fatalf("BlobURL with slashes = %q, want %q", got, want)
	}
}

func TestNewGrepAppClientDefaultsBase(t *testing.T) {
	c := NewGrepAppClient(http.DefaultClient, "  ")
	if c.BaseURL != defaultGrepAppBaseURL {
		t.Fatalf("BaseURL = %q, want %q", c.BaseURL, defaultGrepAppBaseURL)
	}
	if c.Doer == nil {
		t.Fatal("Doer = nil, want the supplied client")
	}
	if c.Sleep == nil || c.Now == nil {
		t.Fatal("Sleep/Now = nil, want real seams by default")
	}

	trimmed := NewGrepAppClient(http.DefaultClient, "https://example.test/")
	if trimmed.BaseURL != "https://example.test" {
		t.Fatalf("BaseURL = %q, want trailing slash trimmed", trimmed.BaseURL)
	}
}

func TestGrepAppSearchGuards(t *testing.T) {
	c := &GrepAppClient{BaseURL: "https://example.test"}
	if _, err := c.Search("q", 10); err == nil || !strings.Contains(err.Error(), "Doer is nil") {
		t.Fatalf("Search with a nil Doer error = %v, want a Doer-is-nil error", err)
	}

	var calls int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt32(&calls, 1)
	}))
	defer srv.Close()

	live := NewGrepAppClient(srv.Client(), srv.URL)
	if _, err := live.Search("   ", 10); err == nil {
		t.Fatal("Search with a blank query returned nil error, want error")
	}
	if n := atomic.LoadInt32(&calls); n != 0 {
		t.Fatalf("blank query issued %d requests, want 0", n)
	}
}
