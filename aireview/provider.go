package aireview

import (
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"
)

// ErrUnsupportedProvider marks a signature type this verifier has no live
// endpoint for, and any value that is not a bearer credential.
//
// It is an error rather than a clean (false, nil) on purpose. Gate.Evaluate
// reads a clean false as VerdictRevoked, which is terminal (dudVerdicts), so
// returning no error here silently discarded every signature type without a
// known auth endpoint — AWS keys, private keys, Anthropic, Google, xAI and
// roughly 120 of the shipped signature types — before any review happened.
var ErrUnsupportedProvider = errors.New("no auth-check endpoint for this signature type")

// ProviderVerifier performs live auth-checks against provider endpoints,
// keyed by the detection-signature name.
type ProviderVerifier struct {
	Client *http.Client
}

// NewProviderVerifier builds a verifier; a nil client uses a 10s-timeout client.
func NewProviderVerifier(client *http.Client) *ProviderVerifier {
	if client == nil {
		client = &http.Client{Timeout: 10 * time.Second}
	}
	return &ProviderVerifier{Client: client}
}

type providerCheck struct {
	key   string // normalized signature-name substring
	build func(secret string) string
}

// providerChecks lists the verifiable providers. It is an ordered slice matched
// in order, not a map: map iteration order is random, which would make the
// chosen endpoint (and therefore the verdict) nondeterministic for a signature
// name matching more than one key.
var providerChecks = []providerCheck{
	{key: "discord", build: func(string) string { return "https://discord.com/api/v10/users/@me" }},
	{key: "telegram", build: func(s string) string { return "https://api.telegram.org/bot" + s + "/getMe" }},
	{key: "slack", build: func(string) string { return "https://slack.com/api/auth.test" }},
	{key: "github", build: func(string) string { return "https://api.github.com/user" }},
	{key: "openai", build: func(string) string { return "https://api.openai.com/v1/models" }},
	{key: "huggingface", build: func(string) string { return "https://huggingface.co/api/whoami-v2" }},
	{key: "openrouter", build: func(string) string { return "https://openrouter.ai/api/v1/auth/key" }},
	{key: "stripe", build: func(string) string { return "https://api.stripe.com/v1/balance" }},
}

// unverifiable are values that are not bearer credentials. Submitting a webhook
// URL or a key that is public by design as "Authorization: Bearer" leaks the
// value to the provider and reports a valid public key as revoked.
var unverifiable = []string{"webhook", "publishable", "publickey"}

// normalizeSig makes matching tolerant of naming style, since the signature
// library spells one provider "Hugging Face", "huggingface" and "Hugging-Face".
func normalizeSig(s string) string {
	return strings.NewReplacer(" ", "", "_", "", "-", "").Replace(strings.ToLower(s))
}

// Check implements Verifier. Unsupported signature types are inconclusive and
// reported as an error, never as a revoked credential.
func (p *ProviderVerifier) Check(sigName, secret string) (bool, string, error) {
	norm := normalizeSig(sigName)

	for _, u := range unverifiable {
		if strings.Contains(norm, u) {
			return false, "", ErrUnsupportedProvider
		}
	}

	for _, pc := range providerChecks {
		if strings.Contains(norm, pc.key) {
			return p.check(pc.build(secret), secret)
		}
	}

	return false, "", ErrUnsupportedProvider
}

// check performs one auth request and classifies the response.
func (p *ProviderVerifier) check(url, secret string) (bool, string, error) {
	req, err := http.NewRequest(http.MethodGet, url, nil)
	if err != nil {
		return false, "", err
	}
	req.Header.Set("Authorization", "Bearer "+secret)

	resp, err := p.Client.Do(req)
	if err != nil {
		return false, "", err
	}
	defer resp.Body.Close()
	io.Copy(io.Discard, resp.Body)

	switch {
	case resp.StatusCode == http.StatusOK:
		return true, "200 OK", nil
	case resp.StatusCode == http.StatusUnauthorized:
		// The provider actively rejected the credential.
		return false, "401", nil
	case resp.StatusCode == http.StatusForbidden || resp.StatusCode == http.StatusTooManyRequests:
		// GitHub answers 403 while rate-limiting, so this is inconclusive and
		// must not be downgraded to "revoked".
		return false, "", fmt.Errorf("provider returned %d (inconclusive)", resp.StatusCode)
	default:
		return false, "", fmt.Errorf("provider returned %d", resp.StatusCode)
	}
}
