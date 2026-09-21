package core

import "testing"

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
