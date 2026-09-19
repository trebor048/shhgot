package aireview

import (
	"errors"
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
	if !errors.Is(err, ErrUnsupportedProvider) {
		t.Fatalf("unknown type must be inconclusive, got err=%v", err)
	}
	if valid {
		t.Fatal("unknown type must not report valid")
	}
}

// Webhook URLs and public-by-design keys are not bearer credentials: sending
// them as one leaks the value to the provider and reports a valid public key as
// revoked.
func TestProviderVerifierSkipsNonCredentials(t *testing.T) {
	p := NewProviderVerifier(&http.Client{Transport: fakeTransport{}})
	for _, sig := range []string{
		"Discord Webhook URL",
		"Slack Webhook URL",
		"Stripe Live Publishable Key",
	} {
		if _, _, err := p.Check(sig, "https://hooks.example.com/services/x"); !errors.Is(err, ErrUnsupportedProvider) {
			t.Errorf("%s: got err=%v, want ErrUnsupportedProvider", sig, err)
		}
	}
}

// 403 is how GitHub rate-limits, so it is inconclusive rather than revoked.
func TestProviderVerifierForbiddenIsInconclusive(t *testing.T) {
	p := NewProviderVerifier(&http.Client{Transport: fakeTransport{}})
	valid, _, err := p.Check("Huggingface Token", "hf_x")
	if err == nil || valid {
		t.Fatalf("403 must be inconclusive: valid=%v err=%v", valid, err)
	}
}

// Signature names spell one provider several ways; "Hugging Face" must still
// reach the huggingface endpoint.
func TestProviderVerifierMatchesNamingStyle(t *testing.T) {
	p := NewProviderVerifier(&http.Client{Transport: fakeTransport{}})
	_, _, err := p.Check("Hugging Face API Token", "hf_x")
	if errors.Is(err, ErrUnsupportedProvider) {
		t.Fatal("'Hugging Face' should match the huggingface provider despite the space")
	}
	if err == nil {
		t.Fatal("the fake transport answers 403 for huggingface, so this must be inconclusive")
	}
}
