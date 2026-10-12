package core

import (
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"
)

// sleepRecorder captures injected Sleep calls so pacing can be asserted without
// real waiting.
type sleepRecorder struct {
	mu    sync.Mutex
	slept []time.Duration
}

func (r *sleepRecorder) record(d time.Duration) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.slept = append(r.slept, d)
}

func (r *sleepRecorder) durations() []time.Duration {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]time.Duration(nil), r.slept...)
}

func TestSearchCodeComSearchParsesResults(t *testing.T) {
	const body = `{"results":[
		{"id":101,"filename":"a.env","repo":"acme/app","url":"https://searchcode.com/codesearch/view/101/","lines":{"2":"sk-proj-secret","1":"OPENAI_API_KEY=test"}},
		{"id":"","filename":"skip.go","repo":"acme/skip","lines":{"1":"ignored"}}
	]}`
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/codesearch_I/" {
			t.Errorf("path = %q, want /api/codesearch_I/", r.URL.Path)
		}
		q := r.URL.Query()
		if q.Get("q") != "OPENAI_API_KEY" {
			t.Errorf("q = %q, want OPENAI_API_KEY", q.Get("q"))
		}
		if q.Get("p") != "0" {
			t.Errorf("p = %q, want 0", q.Get("p"))
		}
		if q.Get("per_page") != "100" {
			t.Errorf("per_page = %q, want 100", q.Get("per_page"))
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, body)
	}))
	defer srv.Close()

	rec := &sleepRecorder{}
	c := NewSearchCodeClient(srv.Client(), srv.URL)
	c.Sleep = rec.record

	results, err := c.Search("OPENAI_API_KEY", 0)
	if err != nil {
		t.Fatalf("Search() error = %v", err)
	}
	if len(results) != 1 {
		t.Fatalf("len(results) = %d, want 1 (empty id must be skipped)", len(results))
	}
	got := results[0]
	if got.ID != "101" {
		t.Errorf("ID = %q, want 101", got.ID)
	}
	if got.Filename != "a.env" || got.Repo != "acme/app" || got.URL != "https://searchcode.com/codesearch/view/101/" {
		t.Errorf("unexpected result fields: %+v", got)
	}
	if got.Content() != "OPENAI_API_KEY=test\nsk-proj-secret" {
		t.Errorf("Content() = %q", got.Content())
	}
	if sleeps := rec.durations(); len(sleeps) != 0 {
		t.Errorf("Search() slept %v, want none", sleeps)
	}
}

func TestSearchCodeResultContentOrdersLinesNumerically(t *testing.T) {
	r := SearchCodeResult{Lines: map[string]string{
		"10":  "ten",
		"2":   "two",
		"1":   "one",
		"bad": "ignored",
	}}
	if got := r.Content(); got != "one\ntwo\nten" {
		t.Fatalf("Content() = %q, want %q", got, "one\ntwo\nten")
	}
	if got := (SearchCodeResult{}).Content(); got != "" {
		t.Fatalf("empty Content() = %q, want empty", got)
	}
}

func TestSearchCodeClientSearchKeywordStopsOnEmptyPage(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Query().Get("p") {
		case "0":
			_, _ = io.WriteString(w, `{"results":[{"id":1,"filename":"a.go","lines":{"1":"x"}}]}`)
		case "1":
			_, _ = io.WriteString(w, `{"results":[]}`)
		default:
			t.Errorf("unexpected page %q", r.URL.Query().Get("p"))
		}
	}))
	defer srv.Close()

	rec := &sleepRecorder{}
	c := NewSearchCodeClient(srv.Client(), srv.URL)
	c.Sleep = rec.record

	results, err := c.SearchKeyword("kw", 5)
	if err != nil {
		t.Fatalf("SearchKeyword() error = %v", err)
	}
	if len(results) != 1 || results[0].ID != "1" {
		t.Fatalf("results = %+v, want single id 1", results)
	}
	sleeps := rec.durations()
	if len(sleeps) != 1 || sleeps[0] != time.Second {
		t.Fatalf("sleeps = %v, want one 1s pause between pages", sleeps)
	}
}

func TestSearchCodeClientSearchKeywordDeduplicatesByID(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Query().Get("p") {
		case "0":
			_, _ = io.WriteString(w, `{"results":[{"id":1,"filename":"a"},{"id":2,"filename":"b"}]}`)
		case "1":
			_, _ = io.WriteString(w, `{"results":[{"id":2,"filename":"b"},{"id":3,"filename":"c"}]}`)
		case "2":
			_, _ = io.WriteString(w, `{"results":[]}`)
		default:
			t.Errorf("unexpected page %q", r.URL.Query().Get("p"))
		}
	}))
	defer srv.Close()

	rec := &sleepRecorder{}
	c := NewSearchCodeClient(srv.Client(), srv.URL)
	c.Sleep = rec.record

	results, err := c.SearchKeyword("kw", 10)
	if err != nil {
		t.Fatalf("SearchKeyword() error = %v", err)
	}
	var ids []string
	for _, r := range results {
		ids = append(ids, r.ID)
	}
	want := []string{"1", "2", "3"}
	if strings.Join(ids, ",") != strings.Join(want, ",") {
		t.Fatalf("ids = %v, want %v", ids, want)
	}
}

func TestSearchCodeClientSearchErrorsOnNon200(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "boom", http.StatusInternalServerError)
	}))
	defer srv.Close()

	c := NewSearchCodeClient(srv.Client(), srv.URL)
	if _, err := c.Search("kw", 0); err == nil {
		t.Fatal("Search() error = nil, want non-200 error")
	}
}

func TestSearchCodeClientSearchBacksOffOn429(t *testing.T) {
	doer := &scriptedDoer{responses: []*http.Response{
		{
			StatusCode: http.StatusTooManyRequests,
			Header:     http.Header{"Retry-After": []string{"3"}},
			Body:       io.NopCloser(strings.NewReader("")),
		},
		{
			StatusCode: http.StatusOK,
			Header:     http.Header{},
			Body:       io.NopCloser(strings.NewReader(`{"results":[{"id":7,"filename":"c.go","lines":{"1":"ok"}}]}`)),
		},
	}}

	rec := &sleepRecorder{}
	c := NewSearchCodeClient(doer, "https://searchcode.example")
	c.Sleep = rec.record

	results, err := c.Search("kw", 0)
	if err != nil {
		t.Fatalf("Search() error = %v", err)
	}
	if len(results) != 1 || results[0].ID != "7" {
		t.Fatalf("results = %+v, want single id 7 after retry", results)
	}
	if doer.calls != 2 {
		t.Fatalf("doer calls = %d, want 2", doer.calls)
	}
	sleeps := rec.durations()
	if len(sleeps) != 1 || sleeps[0] != 3*time.Second {
		t.Fatalf("sleeps = %v, want one 3s backoff", sleeps)
	}
}

// scriptedDoer returns its scripted responses in order.
type scriptedDoer struct {
	mu        sync.Mutex
	calls     int
	responses []*http.Response
}

func (d *scriptedDoer) Do(*http.Request) (*http.Response, error) {
	d.mu.Lock()
	defer d.mu.Unlock()
	i := d.calls
	d.calls++
	if i >= len(d.responses) {
		i = len(d.responses) - 1
	}
	return d.responses[i], nil
}
