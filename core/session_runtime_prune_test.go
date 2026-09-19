package core

import (
	"net/http"
	"testing"

	"github.com/google/go-github/github"
)

// newTestSession builds a minimal Session good enough for the token-removal
// paths (a logger at WARN never touches the package-global session).
func newTestSession(tokens ...string) *Session {
	return &Session{
		Log:              &Logger{},
		Config:           &Config{GitHubAccessTokens: append([]string{}, tokens...)},
		RemovedTokens:    make(map[string]bool),
		Clients:          make(chan *GitHubClientWrapper, 8),
		ExhaustedClients: make(chan *GitHubClientWrapper, 8),
	}
}

func TestRemoveUnauthorizedTokenDropsFromConfigAndPersists(t *testing.T) {
	s := newTestSession("ghp_keep", "ghp_revoked")

	var persisted []string
	old := TokenPersistRemover
	TokenPersistRemover = func(token string) error {
		persisted = append(persisted, token)
		return nil
	}
	defer func() { TokenPersistRemover = old }()

	s.RemoveUnauthorizedToken("ghp_revoked")

	if !s.IsTokenRemoved("ghp_revoked") {
		t.Fatal("revoked token must be marked removed")
	}
	if s.IsTokenRemoved("ghp_keep") {
		t.Fatal("valid token must not be marked removed")
	}
	if len(s.Config.GitHubAccessTokens) != 1 || s.Config.GitHubAccessTokens[0] != "ghp_keep" {
		t.Fatalf("in-memory tokens = %v, want [ghp_keep]", s.Config.GitHubAccessTokens)
	}
	if len(persisted) != 1 || persisted[0] != "ghp_revoked" {
		t.Fatalf("persist hook calls = %v, want [ghp_revoked]", persisted)
	}

	// A second 401 for the same token must not rewrite the file again.
	s.RemoveUnauthorizedToken("ghp_revoked")
	if len(persisted) != 1 {
		t.Fatalf("persist hook called %d times, want 1 (idempotent)", len(persisted))
	}
}

func TestRemoveUnauthorizedTokenReportsPersistError(t *testing.T) {
	s := newTestSession("ghp_revoked")

	old := TokenPersistRemover
	TokenPersistRemover = func(string) error { return http.ErrHandlerTimeout }
	defer func() { TokenPersistRemover = old }()

	// Must not panic even when the config rewrite fails; the in-memory token
	// is still dropped.
	s.RemoveUnauthorizedToken("ghp_revoked")
	if !s.IsTokenRemoved("ghp_revoked") {
		t.Fatal("token must be dropped from memory even if persistence fails")
	}
}

func TestGetClientSkipsRemovedToken(t *testing.T) {
	s := newTestSession("ghp_keep")
	s.RemovedTokens["ghp_bad"] = true

	s.Clients <- &GitHubClientWrapper{Token: "ghp_bad"}
	s.Clients <- &GitHubClientWrapper{Token: "ghp_keep"}

	got := s.GetClient()
	if got.Token != "ghp_keep" {
		t.Fatalf("GetClient returned %q, want ghp_keep (revoked client must be skipped)", got.Token)
	}
}

func TestFreeClientDropsRemovedToken(t *testing.T) {
	s := newTestSession("ghp_keep")
	s.RemovedTokens["ghp_bad"] = true

	s.FreeClient(&GitHubClientWrapper{Token: "ghp_bad"})

	if len(s.Clients) != 0 || len(s.ExhaustedClients) != 0 {
		t.Fatalf("revoked client recycled: clients=%d exhausted=%d", len(s.Clients), len(s.ExhaustedClients))
	}
}

func TestFreeClientRecyclesValidToken(t *testing.T) {
	s := newTestSession("ghp_keep")

	s.FreeClient(&GitHubClientWrapper{Token: "ghp_keep"})

	if len(s.Clients) != 1 {
		t.Fatalf("valid client not recycled: clients=%d", len(s.Clients))
	}
}

func TestIsUnauthorized(t *testing.T) {
	cases := []struct {
		name string
		err  error
		resp *github.Response
		want bool
	}{
		{"nil", nil, nil, false},
		{"response 401", nil, &github.Response{Response: &http.Response{StatusCode: 401}}, true},
		{"response 200", nil, &github.Response{Response: &http.Response{StatusCode: 200}}, false},
		{"error 401", &github.ErrorResponse{Response: &http.Response{StatusCode: 401}}, nil, true},
		{"error 403", &github.ErrorResponse{Response: &http.Response{StatusCode: 403}}, nil, false},
		{"error no response", &github.ErrorResponse{}, nil, false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := isUnauthorized(c.err, c.resp); got != c.want {
				t.Fatalf("isUnauthorized() = %v, want %v", got, c.want)
			}
		})
	}
}

func TestMaskToken(t *testing.T) {
	if got := MaskToken("ghp_1234567890abcdef"); got != "ghp_123456" {
		t.Fatalf("MaskToken(long) = %q", got)
	}
	if got := MaskToken("short"); got != "short" {
		t.Fatalf("MaskToken(short) = %q", got)
	}
}
