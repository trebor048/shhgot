package core

import (
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func TestNewPsbdmpClientDefaults(t *testing.T) {
	c := NewPsbdmpClient(nil, "")
	if c.BaseURL != "https://psbdmp.ws" {
		t.Fatalf("BaseURL = %q, want https://psbdmp.ws", c.BaseURL)
	}
	if c2 := NewPsbdmpClient(nil, "https://example.test/"); c2.BaseURL != "https://example.test" {
		t.Fatalf("BaseURL = %q, want the trailing slash trimmed", c2.BaseURL)
	}
	if !hostAllowed("psbdmp.ws", c.allowedHosts()) {
		t.Fatalf("allowedHosts() = %v, want psbdmp.ws allowlisted", c.allowedHosts())
	}
}

func TestPsbdmpSearchParsesArrayShapeAndSkipsUnusable(t *testing.T) {
	var gotPath string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
		w.Header().Set("Content-Type", "application/json")
		writeBody(w, `[
			{"id":123,"text":"secret A","tags":["key","token"]},
			{"key":"abc","content":"secret B","url":"https://psbdmp.ws/abc"},
			{"id":"","text":"skip me, no id"},
			{"id":"nokey","body":""},
			"bare-string-entry"
		]`)
	}))
	defer srv.Close()

	c := NewPsbdmpClient(srv.Client(), srv.URL)
	got, err := c.Search("amazon aws", 0)
	if err != nil {
		t.Fatalf("Search() error = %v", err)
	}
	if gotPath != "/api/v3/search/amazon aws" {
		t.Fatalf("request path = %q, want the URL-encoded query appended", gotPath)
	}
	if len(got) != 2 {
		t.Fatalf("len(got) = %d, want 2: %+v", len(got), got)
	}

	first := got[0]
	if first.ID != "123" || first.Text != "secret A" {
		t.Errorf("first = %+v, want numeric id 123 with text", first)
	}
	if first.URL != "https://pastebin.com/123" {
		t.Errorf("first.URL = %q, want a derived pastebin URL", first.URL)
	}
	if strings.Join(first.Tags, ",") != "key,token" {
		t.Errorf("first.Tags = %v, want [key token]", first.Tags)
	}

	second := got[1]
	if second.ID != "abc" || second.Text != "secret B" || second.URL != "https://psbdmp.ws/abc" {
		t.Errorf("second = %+v, want key abc with explicit url", second)
	}
}

func TestPsbdmpSearchObjectShapes(t *testing.T) {
	cases := []struct {
		name string
		body string
		want string
	}{
		{"data", `{"data":[{"id":"d1","text":"via data"}]}`, "d1"},
		{"results", `{"results":[{"id":"r1","text":"via results"}]}`, "r1"},
		{"hits", `{"hits":[{"key":"h1","text":"via hits"}]}`, "h1"},
		{"nested", `{"data":{"results":[{"id":"n1","text":"via nested"}]}}`, "n1"},
		{"single", `{"id":"s1","text":"single entry object"}`, "s1"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				writeBody(w, tc.body)
			}))
			defer srv.Close()

			c := NewPsbdmpClient(srv.Client(), srv.URL)
			got, err := c.Search("q", 0)
			if err != nil {
				t.Fatalf("Search() error = %v", err)
			}
			if len(got) != 1 || got[0].ID != tc.want {
				t.Fatalf("got %+v, want single id %q", got, tc.want)
			}
		})
	}
}

func TestPsbdmpSearchDataStringAndTagString(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		writeBody(w, `{"data":[{"key":"x1","data":"text in data field","tag":"alpha, beta gamma"}]}`)
	}))
	defer srv.Close()

	c := NewPsbdmpClient(srv.Client(), srv.URL)
	got, err := c.Search("q", 0)
	if err != nil {
		t.Fatalf("Search() error = %v", err)
	}
	if len(got) != 1 {
		t.Fatalf("got %+v, want one paste", got)
	}
	if got[0].Text != "text in data field" {
		t.Errorf("Text = %q, want the data field value", got[0].Text)
	}
	if strings.Join(got[0].Tags, ",") != "alpha,beta,gamma" {
		t.Errorf("Tags = %v, want [alpha beta gamma]", got[0].Tags)
	}
}

func TestPsbdmpSearchUnrecognisedShapeIsEmpty(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		writeBody(w, `"not-json"`)
	}))
	defer srv.Close()

	c := NewPsbdmpClient(srv.Client(), srv.URL)
	got, err := c.Search("q", 0)
	if err != nil {
		t.Fatalf("Search() error = %v, want nil for an unrecognised shape", err)
	}
	if len(got) != 0 {
		t.Fatalf("got %+v, want no results", got)
	}
}

func TestPsbdmpSearchCapsResults(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		writeBody(w, `[
			{"id":"1","text":"a"},
			{"id":"2","text":"b"},
			{"id":"3","text":"c"},
			{"id":"4","text":"d"}
		]`)
	}))
	defer srv.Close()

	c := NewPsbdmpClient(srv.Client(), srv.URL)
	got, err := c.Search("q", 2)
	if err != nil {
		t.Fatalf("Search() error = %v", err)
	}
	if len(got) != 2 || got[0].ID != "1" || got[1].ID != "2" {
		t.Fatalf("got %+v, want the first two pastes", got)
	}
}

func TestPsbdmpSearchBacksOffOn429(t *testing.T) {
	doer := &scriptedDoer{responses: []*http.Response{
		{
			StatusCode: http.StatusTooManyRequests,
			Header:     http.Header{"Retry-After": []string{"3"}},
			Body:       io.NopCloser(strings.NewReader("")),
		},
		{
			StatusCode: http.StatusOK,
			Header:     http.Header{},
			Body:       io.NopCloser(strings.NewReader(`[{"id":"after","text":"ok"}]`)),
		},
	}}

	clk := newSeamClock()
	c := NewPsbdmpClient(doer, "https://psbdmp.example")
	c.Now = clk.now
	c.Sleep = clk.sleep

	got, err := c.Search("q", 0)
	if err != nil {
		t.Fatalf("Search() error = %v", err)
	}
	if len(got) != 1 || got[0].ID != "after" {
		t.Fatalf("got %+v, want the paste from after the backoff", got)
	}
	if doer.calls != 2 {
		t.Fatalf("doer calls = %d, want 2", doer.calls)
	}
	if sleeps := clk.sleepsCopy(); len(sleeps) != 1 || sleeps[0] != 3*time.Second {
		t.Fatalf("recorded sleeps = %v, want [3s]", sleeps)
	}
}

func TestPsbdmpSearchErrorsOnNon200(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "boom", http.StatusInternalServerError)
	}))
	defer srv.Close()

	c := NewPsbdmpClient(srv.Client(), srv.URL)
	if _, err := c.Search("q", 0); err == nil {
		t.Fatal("Search() error = nil, want a non-200 error")
	}
}

func TestPsbdmpDump(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/v3/dump/abc" {
			http.NotFound(w, r)
			return
		}
		writeBody(w, "RAW-PASTE-BODY")
	}))
	defer srv.Close()

	c := NewPsbdmpClient(srv.Client(), srv.URL)
	body, err := c.Dump("abc")
	if err != nil {
		t.Fatalf("Dump() error = %v", err)
	}
	if string(body) != "RAW-PASTE-BODY" {
		t.Fatalf("Dump() = %q, want RAW-PASTE-BODY", body)
	}
	if _, err := c.Dump("   "); err == nil {
		t.Fatal("Dump() error = nil, want an empty-id error")
	}
}
