package core

import (
	"net/http"
	"net/http/httptest"
	"testing"
)

// TestValidateDiscordTokenSendsBotScheme proves the regression fix: Discord bot
// tokens are only accepted with the "Bot " prefix. Before the fix the bare token
// was sent, so every valid bot token was reported invalid.
func TestValidateDiscordTokenSendsBotScheme(t *testing.T) {
	var gotAuth string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotAuth = r.Header.Get("Authorization")
		if gotAuth == "Bot good-bot-token" {
			w.WriteHeader(http.StatusOK)
			return
		}
		w.WriteHeader(http.StatusUnauthorized)
	}))
	defer srv.Close()

	old := discordAPIBase
	discordAPIBase = srv.URL
	defer func() { discordAPIBase = old }()

	tv := NewTokenValidator(nil)
	tv.minInterval = 0

	valid, provider, _ := tv.ValidateDiscordToken("good-bot-token")
	if !valid {
		t.Fatalf("ValidateDiscordToken = false, want true (server saw Authorization %q)", gotAuth)
	}
	if provider != ProviderDiscord {
		t.Errorf("provider = %q, want %q", provider, ProviderDiscord)
	}
}

// TestValidateDiscordTokenFallsBackToRawScheme covers OAuth/user tokens, which
// are sent without the "Bot " prefix: the second attempt must succeed.
func TestValidateDiscordTokenFallsBackToRawScheme(t *testing.T) {
	var attempts []string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		auth := r.Header.Get("Authorization")
		attempts = append(attempts, auth)
		if auth == "user-token" {
			w.WriteHeader(http.StatusOK)
			return
		}
		w.WriteHeader(http.StatusUnauthorized)
	}))
	defer srv.Close()

	old := discordAPIBase
	discordAPIBase = srv.URL
	defer func() { discordAPIBase = old }()

	tv := NewTokenValidator(nil)
	tv.minInterval = 0

	valid, _, _ := tv.ValidateDiscordToken("user-token")
	if !valid {
		t.Fatalf("ValidateDiscordToken = false, want true; attempts=%v", attempts)
	}
	if len(attempts) != 2 || attempts[0] != "Bot user-token" || attempts[1] != "user-token" {
		t.Errorf("attempts = %v, want [Bot user-token  user-token]", attempts)
	}
}

// TestValidateDiscordTokenStopsOnNonAuthError makes sure a 5xx is not retried
// under the second scheme.
func TestValidateDiscordTokenStopsOnNonAuthError(t *testing.T) {
	calls := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		w.WriteHeader(http.StatusInternalServerError)
	}))
	defer srv.Close()

	old := discordAPIBase
	discordAPIBase = srv.URL
	defer func() { discordAPIBase = old }()

	tv := NewTokenValidator(nil)
	tv.minInterval = 0

	valid, _, _ := tv.ValidateDiscordToken("t")
	if valid {
		t.Fatal("ValidateDiscordToken = true, want false")
	}
	if calls != 1 {
		t.Errorf("server called %d times, want 1 (no retry on 500)", calls)
	}
}

// TestTokenResultCache covers the cache that applyVerifier relies on: a token
// whose verdict is already cached is answered without another provider call.
// An unknown verdict (reserved before an in-flight check) counts as valid so a
// concurrent duplicate cannot wrongly drop a match.
func TestTokenResultCache(t *testing.T) {
	tv := NewTokenValidator(nil)

	tv.testedTokens["reserved"] = tokenTestResult{valid: true}
	if !tv.TestAndLogToken("reserved", "") {
		t.Error("cached valid token reported invalid")
	}

	tv.testedTokens["known-bad"] = tokenTestResult{valid: false, provider: ProviderOpenAI}
	if tv.TestAndLogToken("known-bad", "") {
		t.Error("cached invalid token reported valid")
	}
}
