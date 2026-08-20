package aireview

import (
	"io"
	"net/http"
	"strings"
	"testing"
)

// fakeTransport returns canned responses per request URL.
type fakeTransport struct{}

func (fakeTransport) RoundTrip(r *http.Request) (*http.Response, error) {
	switch {
	case strings.Contains(r.URL.String(), "slack.com"):
		if r.Header.Get("Authorization") == "Bearer xoxb-good" {
			return &http.Response{StatusCode: 200, Body: io.NopCloser(strings.NewReader(`{"ok":true}`)), Header: http.Header{}}, nil
		}
		return &http.Response{StatusCode: 401, Body: io.NopCloser(strings.NewReader(`{"ok":false}`)), Header: http.Header{}}, nil
	case strings.Contains(r.URL.String(), "api.github.com"):
		return &http.Response{StatusCode: 200, Body: io.NopCloser(strings.NewReader(`{}`)), Header: http.Header{}}, nil
	case strings.Contains(r.URL.String(), "huggingface.co"):
		return &http.Response{StatusCode: 403, Body: io.NopCloser(strings.NewReader(`{}`)), Header: http.Header{}}, nil
	}
	return &http.Response{StatusCode: 500, Body: io.NopCloser(strings.NewReader("")), Header: http.Header{}}, nil
}

func TestProviderVerifierSlack(t *testing.T) {
	p := NewProviderVerifier(&http.Client{Transport: fakeTransport{}})
	valid, info, err := p.Check("Slack Token", "xoxb-good")
	if err != nil {
		t.Fatal(err)
	}
	if !valid {
		t.Fatalf("slack valid=false, info=%q", info)
	}
	valid, info, err = p.Check("Slack Token", "xoxb-bad")
	if err != nil {
		t.Fatal(err)
	}
	if valid || info != "401" {
		t.Fatalf("slack bad: valid=%v info=%q", valid, info)
	}
}

func TestProviderVerifierGithub(t *testing.T) {
	p := NewProviderVerifier(&http.Client{Transport: fakeTransport{}})
	valid, _, err := p.Check("GitHub Token", "ghp_123")
	if err != nil || !valid {
		t.Fatalf("github: valid=%v err=%v", valid, err)
	}
}

func TestProviderVerifierUnknown(t *testing.T) {
	p := NewProviderVerifier(&http.Client{Transport: fakeTransport{}})
	valid, _, err := p.Check("Google API Key", "AIzaSy...")
	if err != nil {
		t.Fatalf("unknown type must be inconclusive without error, got %v", err)
	}
	if valid {
		t.Fatal("unknown type must not report valid")
	}
}
