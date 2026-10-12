package core

import (
	"strings"
	"testing"

	"github.com/trebor048/shhgot/core/verifiers"
)

// contextOnlyVerifiers are registered verifiers that DetectProvider cannot
// select from a bare secret value, so the only way to run them is an explicit
// `verifier:` entry in config.yaml resolved through verifiers.Lookup (or, for
// the companion-credential entries, they additionally need the companion
// values). TestEveryRegisteredVerifierIsReachableOrDocumented fails when a
// verifier is both listed here and mapped in providerVerifierNames: a mapping
// claims the provider is auto-detected, which is exactly what this list denies.
var contextOnlyVerifiers = map[string]bool{
	// Companion credentials or a caller-supplied endpoint are required.
	"algolia":   true, // needs AppID
	"atlassian": true, // needs Email/Password/Domain
	"bitbucket": true, // needs Username/Password
	"freshdesk": true, // needs Subdomain/Password
	"paypal":    true, // needs Username/Password
	"twilio":    true, // needs SID/Password
	"zendesk":   true, // needs Subdomain/Email/Password
	// No distinctive key prefix and no value-shape detection.
	"airtable":    true,
	"amplitude":   true,
	"asana":       true,
	"circleci":    true,
	"cloudflare":  true,
	"cohere":      true,
	"datadog":     true,
	"google_maps": true,
	"heroku":      true,
	"intercom":    true,
	"mailchimp":   true,
	"mistral":     true,
	"mixpanel":    true,
	"pagerduty":   true,
	"segment":     true,
	"sentry":      true,
	"travis":      true,
	"vercel":      true,
}

// openAICompatibleFallbackVerifiers are registered verifiers with no
// DetectProvider prefix whose providers ValidateOpenAIToken probes after a
// generic "sk-" key fails its detected verifier, so the provider can still be
// reported at runtime and providerVerifierNames must keep mapping it for the
// dashboard to resolve its category.
var openAICompatibleFallbackVerifiers = map[string]bool{
	"dashscope": true,
	"fireworks": true,
	"moonshot":  true,
	"together":  true,
}

// detectSamples are representative values for verifiers that DetectProvider
// recognises by value shape instead of a registry prefix.
var detectSamples = map[string]string{
	"deepseek": "sk-" + strings.Repeat("a1b2c3d4", 4),
	"discord":  "M" + strings.Repeat("a", 23) + "." + strings.Repeat("b", 6) + "." + strings.Repeat("c", 27),
	"telegram": "123456:" + strings.Repeat("a", 35),
}

// detectProviderFor reports whether DetectProvider maps some representative
// value for v back to v through providerVerifierNames. Merely appearing in the
// map is not enough: there must be a detection path that produces the mapping.
func detectProviderFor(tv *TokenValidator, v verifiers.Verifier) (string, bool) {
	for _, prefix := range v.KeyPrefixes {
		if got := tv.DetectProvider(prefix); providerVerifierNames[got] == v.Name {
			return got, true
		}
	}
	if sample, ok := detectSamples[v.Name]; ok {
		if got := tv.DetectProvider(sample); providerVerifierNames[got] == v.Name {
			return got, true
		}
	}
	return "", false
}

// TestEveryRegisteredVerifierIsReachableOrDocumented makes the reachability gap
// visible instead of silent: every registered verifier must be Detect-reachable
// (DetectProvider returns a provider that maps back to it) or explicitly
// acknowledged as context-only. A verifier that is mapped in
// providerVerifierNames without a detection path fails, so map membership alone
// can no longer pass for reachability.
func TestEveryRegisteredVerifierIsReachableOrDocumented(t *testing.T) {
	tv := NewTokenValidator(nil)

	mapped := make(map[string]string, len(providerVerifierNames))
	for provider, name := range providerVerifierNames {
		mapped[name] = provider
		if _, ok := verifiers.Lookup(name); !ok {
			t.Errorf("providerVerifierNames[%q] maps to unknown verifier %q", provider, name)
		}
	}

	for _, v := range verifiers.All() {
		if contextOnlyVerifiers[v.Name] {
			if provider, ok := mapped[v.Name]; ok {
				t.Errorf("%s is both provider-mapped (as %q) and listed context-only", v.Name, provider)
			}
			continue
		}
		if openAICompatibleFallbackVerifiers[v.Name] {
			if _, ok := mapped[v.Name]; !ok {
				t.Errorf("%s is fallback-reachable but missing from providerVerifierNames", v.Name)
			}
			continue
		}
		if _, ok := detectProviderFor(tv, v); !ok {
			t.Errorf("%s is not Detect-reachable via DetectProvider and not listed context-only", v.Name)
			continue
		}
		if _, ok := mapped[v.Name]; !ok {
			t.Errorf("%s is Detect-reachable but missing from providerVerifierNames", v.Name)
		}
	}

	for name := range contextOnlyVerifiers {
		if _, ok := verifiers.Lookup(name); !ok {
			t.Errorf("contextOnlyVerifiers names unknown verifier %q", name)
		}
	}
}
