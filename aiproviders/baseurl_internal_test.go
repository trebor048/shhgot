package aiproviders

import (
	"errors"
	"strings"
	"testing"
)

// A base URL that is not an absolute http(s) URL used to be stored without
// complaint, so the mistake only appeared on the first review as an opaque
// "unsupported protocol scheme" from the HTTP client. url.Parse reads
// "localhost:11434" as the scheme "localhost", which is exactly the shape a user
// typing an Ollama endpoint by hand produces.
func TestBaseURLMustBeAnAbsoluteHTTPURL(t *testing.T) {
	reject := []string{
		"localhost:11434",
		"127.0.0.1:11434",
		"api.deepseek.com/v1",
		"ftp://example.com/v1",
		"file:///etc/passwd",
		"http://",
		"://example.com",
	}
	for _, raw := range reject {
		for _, provider := range []string{"ollama", "custom", "deepseek"} {
			in := Settings{Provider: provider, APIKey: "k", BaseURL: raw, Model: "m"}
			_, err := resolve(in)
			if err == nil {
				t.Errorf("resolve(%s, base_url=%q) was accepted", provider, raw)
				continue
			}
			if !errors.Is(err, ErrBadConfig) {
				t.Errorf("resolve(%s, base_url=%q) error = %v, want ErrBadConfig", provider, raw, err)
			}
			// The message has to name the offending value, or the operator cannot
			// tell which field to fix.
			if !strings.Contains(err.Error(), raw) {
				t.Errorf("error for %q does not quote the value: %v", raw, err)
			}
		}
		// New must refuse it too: that is the check Store.Set and the settings API
		// rely on to reject a bad save.
		if _, err := New(Settings{Provider: "custom", APIKey: "k", BaseURL: raw, Model: "m"}); err == nil {
			t.Errorf("New accepted base_url=%q", raw)
		}
	}

	accept := []string{
		"http://localhost:11434",
		"https://api.deepseek.com/v1",
		"http://127.0.0.1:9/v1",
		"https://llm.internal.example/v1/",
		"http://[::1]:11434",
	}
	for _, raw := range accept {
		got, err := resolve(Settings{Provider: "custom", APIKey: "k", BaseURL: raw, Model: "m"})
		if err != nil {
			t.Errorf("resolve(custom, base_url=%q) = %v, want it accepted", raw, err)
			continue
		}
		if got.BaseURL != raw {
			t.Errorf("base_url = %q, want it preserved as %q", got.BaseURL, raw)
		}
	}
}

// An empty base URL still means "use the provider default", so only a value that
// was actually supplied is judged.
func TestEmptyBaseURLIsNotRejected(t *testing.T) {
	for _, provider := range []string{"deepseek", "openai", "ollama"} {
		got, err := resolve(Settings{Provider: provider, APIKey: "k"})
		if err != nil {
			t.Fatalf("resolve(%s) with no base url: %v", provider, err)
		}
		if got.BaseURL == "" {
			t.Errorf("provider %s has no default base url", provider)
		}
	}
}
