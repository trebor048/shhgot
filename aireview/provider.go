package aireview

import (
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"
)

// ProviderVerifier performs live auth-checks against provider endpoints,
// keyed by detection-signature name substrings.
type ProviderVerifier struct {
	Client *http.Client
}

// NewProviderVerifier builds a verifier; nil client uses http.DefaultClient.
func NewProviderVerifier(client *http.Client) *ProviderVerifier {
	if client == nil {
		client = &http.Client{Timeout: 10 * time.Second}
	}
	return &ProviderVerifier{Client: client}
}

type providerCheck struct {
	url   string
	build func(secret string) string // returns full URL (telegram embeds token)
}

// providerFor maps a signature-name substring to its auth-check endpoint.
var providerFor = map[string]providerCheck{
	"discord":     {url: "https://discord.com/api/v10/users/@me", build: func(s string) string { return "https://discord.com/api/v10/users/@me" }},
	"telegram":    {url: "", build: func(s string) string { return "https://api.telegram.org/bot" + s + "/getMe" }},
	"slack":       {url: "https://slack.com/api/auth.test", build: func(s string) string { return "https://slack.com/api/auth.test" }},
	"github":      {url: "https://api.github.com/user", build: func(s string) string { return "https://api.github.com/user" }},
	"openai":      {url: "https://api.openai.com/v1/models", build: func(s string) string { return "https://api.openai.com/v1/models" }},
	"huggingface": {url: "https://huggingface.co/api/whoami-v2", build: func(s string) string { return "https://huggingface.co/api/whoami-v2" }},
	"openrouter":  {url: "https://openrouter.ai/api/v1/auth/key", build: func(s string) string { return "https://openrouter.ai/api/v1/auth/key" }},
	"stripe":      {url: "https://api.stripe.com/v1/balance", build: func(s string) string { return "https://api.stripe.com/v1/balance" }},
}

// Check implements Verifier. Unknown signature types are inconclusive
// (valid=false, err=nil).
func (p *ProviderVerifier) Check(sigName, secret string) (bool, string, error) {
	low := strings.ToLower(sigName)
	var pc providerCheck
	found := false
	for sub, c := range providerFor {
		if strings.Contains(low, sub) {
			pc = c
			found = true
			break
		}
	}
	if !found {
		return false, "", nil
	}
	req, err := http.NewRequest(http.MethodGet, pc.build(secret), nil)
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
	case resp.StatusCode == http.StatusUnauthorized || resp.StatusCode == http.StatusForbidden:
		return false, fmt.Sprintf("%d", resp.StatusCode), nil
	default:
		return false, "", fmt.Errorf("provider returned %d", resp.StatusCode)
	}
}
