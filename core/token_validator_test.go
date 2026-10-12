package core

import (
	"io"
	"net/http"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"testing"

	"github.com/trebor048/shhgot/core/verifiers"
)

// TestDetectProviderRoutesOpenRouterBeforeGenericSK is a regression test for
// the provider-detection ordering bug: OpenRouter keys start with "sk-" and are
// 64+ characters, so the generic "sk-" case used to swallow them and they were
// validated against OpenAI endpoints (and always reported invalid). The
// "sk-or-v1-" case must be evaluated before the generic "sk-" case.
func TestDetectProviderRoutesOpenRouterBeforeGenericSK(t *testing.T) {
	tv := NewTokenValidator(nil)

	// Real OpenRouter keys are "sk-or-v1-" followed by 64 lowercase hex chars.
	openRouterKey := "sk-or-v1-" + "0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef"

	if got := tv.DetectProvider(openRouterKey); got != ProviderOpenRouter {
		t.Errorf("DetectProvider(openRouterKey) = %q, want %q", got, ProviderOpenRouter)
	}

	// The minimum-length OpenRouter key (60 chars after the prefix) must also
	// route to OpenRouter, not to the generic OpenAI fallback.
	shortOpenRouterKey := "sk-or-v1-" + "0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcd"
	if got := tv.DetectProvider(shortOpenRouterKey); got != ProviderOpenRouter {
		t.Errorf("DetectProvider(shortOpenRouterKey) = %q, want %q", got, ProviderOpenRouter)
	}
}

// TestDetectProviderKeepsOtherSKPrefixes ensures the reorder did not disturb
// the other sk-* prefixes or the generic OpenAI fallback.
func TestDetectProviderKeepsOtherSKPrefixes(t *testing.T) {
	tv := NewTokenValidator(nil)

	tests := []struct {
		name  string
		token string
		want  string
	}{
		{"generic sk- still OpenAI", "sk-" + "abcdefghijklmnopqrstuvwxyz123456", ProviderOpenAI},
		{"sk-proj- still OpenAI", "sk-proj-" + "abcdefghijklmnopqrstuvwxyz123456", ProviderOpenAI},
		{"sk-svcacct- still OpenAI", "sk-svcacct-" + "abcdefghijklmnopqrstuvwxyz123456", ProviderOpenAI},
		{"sk-ant-api03- still Anthropic", "sk-ant-api03-" + "abcdefghijklmnopqrstuvwxyz123456", ProviderAnthropic},
		{"AIzaSy still Google", "AIzaSy" + "abcdefghijklmnopqrstuvwxyz1234567890", ProviderGoogle},
		{"xai- still xAI", "xai-" + "abcdefghijklmnopqrstuvwxyz1234567890abcdefghijklmnopqrstuvwxyz1234567890abcdefghijklmnopqrstuvwxyz1234567890", ProviderXAI},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := tv.DetectProvider(tt.token); got != tt.want {
				t.Errorf("DetectProvider(%q) = %q, want %q", tt.token, got, tt.want)
			}
		})
	}
}

// TestDetectProviderRoutesDocumentedKeyFormats is a regression test for the
// registered verifiers that had a documented key prefix but no DetectProvider
// case: mapbox, notion, linear and newrelic could never be auto-verified because
// detection stopped at ProviderUnknown. Each prefix must yield its provider.
func TestDetectProviderRoutesDocumentedKeyFormats(t *testing.T) {
	tv := NewTokenValidator(nil)

	tests := []struct {
		name  string
		token string
		want  string
	}{
		{"mapbox public", "pk.eyJ1IjoiYWNtZSJ9", ProviderMapbox},
		{"mapbox secret", "sk.eyJ1IjoiYWNtZSJ9", ProviderMapbox},
		{"notion integration", "ntn_abcdefghijklmnopqrstuvwxyz", ProviderNotion},
		{"notion legacy", "secret_abcdefghijklmnopqrstuvwxyz", ProviderNotion},
		{"linear", "lin_api_abcdefghijklmnopqrstuvwxyz", ProviderLinear},
		{"newrelic", "NRAK-abcdefghijklmnopqrstuvwxyz", ProviderNewRelic},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := tv.DetectProvider(tt.token); got != tt.want {
				t.Errorf("DetectProvider(%q) = %q, want %q", tt.token, got, tt.want)
			}
		})
	}
}

// TestDetectProviderRoutesStripeBeforeElevenLabs is a regression test for the
// provider-detection ordering bug: a Stripe secret key ("sk_live_"/"sk_test_")
// also starts with "sk_", so the ElevenLabs case swallowed it and the key was
// validated against api.elevenlabs.io (and reported invalid). The Stripe case
// must be evaluated before the "sk_" case.
func TestDetectProviderRoutesStripeBeforeElevenLabs(t *testing.T) {
	tv := NewTokenValidator(nil)

	for _, stripeKey := range []string{
		"sk_live_" + strings.Repeat("a", 24),
		"sk_test_" + strings.Repeat("b", 24),
	} {
		if got := tv.DetectProvider(stripeKey); got != ProviderStripe {
			t.Errorf("DetectProvider(%q) = %q, want %q", stripeKey, got, ProviderStripe)
		}
	}

	// A non-Stripe "sk_" key stays ElevenLabs.
	elevenKey := "sk_" + strings.Repeat("c", 48)
	if got := tv.DetectProvider(elevenKey); got != ProviderElevenLabs {
		t.Errorf("DetectProvider(%q) = %q, want %q", elevenKey, got, ProviderElevenLabs)
	}
}

// hostSelectDoer answers 200 (with an OpenAI-compatible models body) for one
// host and 401 for every other, so a test can make a token's only accepting
// endpoint a chosen provider.
type hostSelectDoer struct {
	acceptHost string

	mu       sync.Mutex
	requests map[string]int
}

func (d *hostSelectDoer) Do(req *http.Request) (*http.Response, error) {
	d.mu.Lock()
	if d.requests == nil {
		d.requests = make(map[string]int)
	}
	d.requests[req.URL.Host]++
	d.mu.Unlock()

	status := http.StatusUnauthorized
	body := `{"error":{"message":"invalid api key"}}`
	if req.URL.Host == d.acceptHost {
		status = http.StatusOK
		body = `{"data":[{"id":"deepseek-chat"}]}`
	}
	return &http.Response{
		StatusCode: status,
		Header:     make(http.Header),
		Body:       io.NopCloser(strings.NewReader(body)),
	}, nil
}

// TestSKTokenFallsBackToCompatibleEndpoint is a regression test for the registry
// dispatch bypassing the OpenAI-compatible fallback: a generic "sk-" key whose
// only accepting endpoint is DeepSeek/Moonshot was reported invalid because only
// the detected provider's (OpenAI) verifier ran. After the fix the fallback
// probe reports it valid under the endpoint that accepted it.
func TestSKTokenFallsBackToCompatibleEndpoint(t *testing.T) {
	tv := NewTokenValidator(nil)
	tv.httpClient = &hostSelectDoer{acceptHost: "api.moonshot.cn"}

	// "sk-" plus non-hex characters is not claimed by the DeepSeek shape check,
	// so DetectProvider routes it to OpenAI and only the openai verifier runs.
	token := "sk-" + strings.Repeat("z", 40)
	if got := tv.DetectProvider(token); got != ProviderOpenAI {
		t.Fatalf("DetectProvider(%q) = %q, want %q", token, got, ProviderOpenAI)
	}

	valid, provider, _, outcome := tv.validateToken(token)
	if !valid {
		t.Fatal("a key accepted only by Moonshot must be reported valid")
	}
	if provider != ProviderKimi {
		t.Fatalf("provider = %q, want %q", provider, ProviderKimi)
	}
	if outcome.Status != verifiers.Valid {
		t.Fatalf("outcome status = %q, want %q", outcome.Status, verifiers.Valid)
	}
}

// TestSKTokenStillInvalidWhenNoEndpointAccepts guards the fallback's negative
// case: when no compatible endpoint accepts the key it stays invalid with the
// original verdict.
func TestSKTokenStillInvalidWhenNoEndpointAccepts(t *testing.T) {
	tv := NewTokenValidator(nil)
	tv.httpClient = &hostSelectDoer{acceptHost: "example.invalid"}

	token := "sk-" + strings.Repeat("z", 40)
	valid, _, _, outcome := tv.validateToken(token)
	if valid {
		t.Fatal("a key no endpoint accepts must be reported invalid")
	}
	if outcome.Status != verifiers.Invalid {
		t.Fatalf("outcome status = %q, want %q", outcome.Status, verifiers.Invalid)
	}
}

// TestAnthropicKeyDoesNotFallBackToOpenAICompatible ensures the fallback is
// scoped to the ambiguous generic "sk-" family: an Anthropic key must not be
// probed against OpenAI-compatible endpoints.
func TestAnthropicKeyDoesNotFallBackToOpenAICompatible(t *testing.T) {
	doer := &hostSelectDoer{acceptHost: "api.deepseek.com"}
	tv := NewTokenValidator(nil)
	tv.httpClient = doer

	token := "sk-ant-api03-" + strings.Repeat("z", 40)
	if openAICompatibleSK(token) {
		t.Fatal("Anthropic keys must not be treated as generic sk- keys")
	}
	valid, provider, _, _ := tv.validateToken(token)
	if valid {
		t.Fatal("an Anthropic key must not be accepted by a DeepSeek-only endpoint")
	}
	if provider != ProviderAnthropic {
		t.Fatalf("provider = %q, want %q", provider, ProviderAnthropic)
	}
	if got := doer.requests["api.deepseek.com"]; got != 0 {
		t.Fatalf("DeepSeek received %d requests for an Anthropic key, want 0", got)
	}
}

// TestGitHubPrefixesRoutedToGitHub pins the full GitHub bearer family to the
// GitHub provider and extractor. ghu_/ghs_/ghr_ used to fall through to
// Unknown (never validated, never pooled) while the shipped signatures already
// matched them.
func TestGitHubPrefixesRoutedToGitHub(t *testing.T) {
	tv := NewTokenValidator(nil)
	for _, prefix := range []string{"ghp_", "gho_", "ghu_", "ghs_", "ghr_"} {
		token := prefix + strings.Repeat("Ab3_", 9)
		if got := tv.DetectProvider(token); got != ProviderGithub {
			t.Errorf("DetectProvider(%q) = %q, want %q", token, got, ProviderGithub)
		}
		if !githubRegex.MatchString(token) {
			t.Errorf("githubRegex does not match %q", token)
		}
		if toks := tv.ExtractTokensFromMatch("key=" + token); len(toks) != 1 || toks[0] != token {
			t.Errorf("ExtractTokensFromMatch(%q) = %v, want [%q]", token, toks, token)
		}
	}
	fine := "github_pat_" + strings.Repeat("Xy9_", 21)[:82]
	if got := tv.DetectProvider(fine); got != ProviderGithub {
		t.Errorf("DetectProvider(fine-grained) = %q, want %q", got, ProviderGithub)
	}
	if !githubRegex.MatchString(fine) {
		t.Errorf("githubRegex does not match fine-grained PAT")
	}
}

// TestSaveValidTokenPersistsFullToken is a regression test for validated tokens
// never showing up usably in logs/priority3.log: the saver used to write only
// a masked prefix (sk-c2e4b...225a), so a validated credential could never be
// recovered from the loot file. The full token must be stored; the console
// output stays masked by the callers.
func TestSaveValidTokenPersistsFullToken(t *testing.T) {
	dir := t.TempDir()
	cwd, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Chdir(dir); err != nil {
		t.Fatal(err)
	}
	defer func() {
		if err := os.Chdir(cwd); err != nil {
			t.Fatal(err)
		}
	}()

	tv := NewTokenValidator(nil)
	token := "sk-c2e4be00a94a41cfafad9f908726225a"
	if err := tv.SaveValidToken(token, ProviderDeepSeek, "deepseek: 2 models available"); err != nil {
		t.Fatalf("SaveValidToken: %v", err)
	}

	data, err := os.ReadFile(filepath.Join("logs", "priority3.log"))
	if err != nil {
		t.Fatalf("reading priority3.log: %v", err)
	}
	if !strings.Contains(string(data), token) {
		t.Fatalf("priority3.log does not contain the full token; got %q", string(data))
	}
	// The loot file holds plaintext credentials; it must not be world-readable.
	// Windows does not report POSIX permission bits, so this is skipped there.
	if runtime.GOOS != "windows" {
		info, err := os.Stat(filepath.Join("logs", "priority3.log"))
		if err != nil {
			t.Fatalf("stat priority3.log: %v", err)
		}
		if perm := info.Mode().Perm(); perm != 0o600 {
			t.Errorf("priority3.log mode = %v, want 0600", perm)
		}
	}
}
