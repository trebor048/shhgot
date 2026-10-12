package proxy

import (
	"net/url"
	"strings"
	"testing"
)

func mustURL(t *testing.T, raw string) *url.URL {
	t.Helper()
	u, err := url.Parse(raw)
	if err != nil {
		t.Fatalf("parse %q: %v", raw, err)
	}
	return u
}

func TestNewRejectsInvalidURLs(t *testing.T) {
	cases := []struct {
		name string
		url  string
	}{
		{"empty", ""},
		{"blank", "   "},
		{"unsupported-scheme", "ftp://127.0.0.1:21"},
		{"no-host", "http:///path"},
		{"malformed", "http://[::1"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if _, err := New([]string{tc.url}); err == nil {
				t.Fatalf("New(%q) = nil error, want error", tc.url)
			}
		})
	}
}

func TestParseErrorsRedactCredentials(t *testing.T) {
	// Credentialed proxy URLs are valid config; the rule is only that the
	// credentials never reach an error string.
	if _, err := parse("http://user:s3cret@proxy:8080"); err != nil {
		t.Fatalf("parse credentialed URL error = %v, want nil", err)
	}
	if _, err := parse("http://user:s3cret@[::1"); err == nil || strings.Contains(err.Error(), "s3cret") {
		t.Fatalf("malformed credentialed URL error = %v, want rejection without the password", err)
	}
	if got := redactRawProxyURL("socks5://u:p@h:1080/x"); got != "socks5://***@h:1080/x" {
		t.Fatalf("redact = %q, want socks5://***@h:1080/x", got)
	}
	if got := redactRawProxyURL("http://h:8080"); got != "http://h:8080" {
		t.Fatalf("redact passthrough = %q, want unchanged", got)
	}
}

func TestNextRoundRobin(t *testing.T) {
	p, err := New([]string{"http://a:8080", "http://b:8080", "http://c:8080"})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	want := []string{
		"http://a:8080",
		"http://b:8080",
		"http://c:8080",
		"http://a:8080",
		"http://b:8080",
	}
	for i, w := range want {
		got, ok := p.Next()
		if !ok {
			t.Fatalf("Next #%d: not ok, want a proxy", i)
		}
		if got.String() != w {
			t.Fatalf("Next #%d = %s, want %s", i, got, w)
		}
	}
}

func TestBanAfterFailuresAndRecovery(t *testing.T) {
	p, err := New([]string{"http://a:8080", "http://b:8080"})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	a := mustURL(t, "http://a:8080")
	b := mustURL(t, "http://b:8080")

	if h, total := p.Stats(); h != 2 || total != 2 {
		t.Fatalf("Stats = (%d,%d), want (2,2)", h, total)
	}

	p.ReportFailure(a)
	p.ReportFailure(a)
	if _, ok := p.Next(); !ok {
		t.Fatal("Next after two failures: not ok, want a proxy")
	}
	if h, total := p.Stats(); h != 2 || total != 2 {
		t.Fatalf("Stats after two failures = (%d,%d), want (2,2)", h, total)
	}

	// The third consecutive failure benches a.
	p.ReportFailure(a)
	if h, total := p.Stats(); h != 1 || total != 2 {
		t.Fatalf("Stats after ban = (%d,%d), want (1,2)", h, total)
	}
	for i := 0; i < 3; i++ {
		got, ok := p.Next()
		if !ok {
			t.Fatalf("Next #%d after ban: not ok, want b", i)
		}
		if got.String() != b.String() {
			t.Fatalf("Next #%d after ban = %s, want %s", i, got, b)
		}
	}

	// A success resets the counter and un-bans the member.
	p.ReportSuccess(a)
	if h, total := p.Stats(); h != 2 || total != 2 {
		t.Fatalf("Stats after recovery = (%d,%d), want (2,2)", h, total)
	}
	seen := map[string]bool{}
	for i := 0; i < 2; i++ {
		got, ok := p.Next()
		if !ok {
			t.Fatalf("Next #%d after recovery: not ok", i)
		}
		seen[got.String()] = true
	}
	if !seen[a.String()] || !seen[b.String()] {
		t.Fatalf("after recovery served %v, want both a and b", seen)
	}
}

func TestSetBanAfter(t *testing.T) {
	p, err := New([]string{"http://a:8080"})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	p.SetBanAfter(2)
	a := mustURL(t, "http://a:8080")

	p.ReportFailure(a)
	if _, ok := p.Next(); !ok {
		t.Fatal("Next after one failure: not ok, want the proxy healthy")
	}
	p.ReportFailure(a)
	if h, _ := p.Stats(); h != 0 {
		t.Fatalf("healthy after two failures = %d, want 0", h)
	}
	if _, ok := p.Next(); ok {
		t.Fatal("Next returned a benched proxy")
	}
}

func TestEmptyPoolAndAdd(t *testing.T) {
	p, err := New(nil)
	if err != nil {
		t.Fatalf("New(nil): %v", err)
	}
	if h, total := p.Stats(); h != 0 || total != 0 {
		t.Fatalf("Stats = (%d,%d), want (0,0)", h, total)
	}
	if _, ok := p.Next(); ok {
		t.Fatal("Next on an empty pool returned a proxy")
	}

	if err := p.Add("socks5://127.0.0.1:1080"); err != nil {
		t.Fatalf("Add(socks5): %v", err)
	}
	if err := p.Add("nonsense"); err == nil {
		t.Fatal("Add(bad) = nil error, want error")
	}
	if h, total := p.Stats(); h != 1 || total != 1 {
		t.Fatalf("Stats after Add = (%d,%d), want (1,1)", h, total)
	}
}

func TestReportUnknownURLIsNoop(t *testing.T) {
	p, err := New([]string{"http://a:8080"})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	p.ReportFailure(mustURL(t, "http://unknown:1"))
	p.ReportSuccess(mustURL(t, "http://unknown:1"))
	p.ReportFailure(nil)
	p.ReportSuccess(nil)
	if h, total := p.Stats(); h != 1 || total != 1 {
		t.Fatalf("Stats = (%d,%d), want (1,1)", h, total)
	}
}
