package core

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/google/go-github/github"
	"github.com/trebor048/shhgot/core/verifiers"
	"golang.org/x/oauth2"
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

// TestGitHubTokensSnapshotIsRaceFree ranges the tokens while another
// goroutine mutates them (run with -race): it must neither race nor observe
// a torn slice, and mutating the returned copy must not affect the session.
func TestGitHubTokensSnapshotIsRaceFree(t *testing.T) {
	s := newTestSession("ghp_a", "ghp_b")
	done := make(chan struct{})
	go func() {
		defer close(done)
		for i := 0; i < 50; i++ {
			_ = s.GitHubTokens()
		}
	}()
	for i := 0; i < 50; i++ {
		s.RemoveUnauthorizedToken("ghp_b")
	}
	<-done
	got := s.GitHubTokens()
	if len(got) != 1 || got[0] != "ghp_a" {
		t.Fatalf("tokens = %v, want [ghp_a]", got)
	}
	got[0] = "mutated"
	if s.Config.GitHubAccessTokens[0] != "ghp_a" {
		t.Fatal("GitHubTokens must return a copy, not the live slice")
	}
	if s.GitHubTokens() == nil {
		t.Fatal("expected non-nil snapshot")
	}
	nilSession := &Session{}
	if nilSession.GitHubTokens() != nil {
		t.Fatal("nil Config must yield nil snapshot")
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

// A validated GitHub token must be persisted to logs/priority3.log, announced
// through the validator callbacks (Tokens tab + outcome cache), and pooled.
// Previously it only got an Info line: validated GitHub tokens never showed
// up in the logs.
func TestAddGitHubTokenPersistsAnnouncesAndPools(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.Contains(r.Header.Get("Authorization"), "ghp_bad") {
			w.WriteHeader(http.StatusUnauthorized)
			_, _ = io.WriteString(w, `{"message":"Bad credentials"}`)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, `{"login":"octocat"}`)
	}))
	defer server.Close()

	old := newGitHubValidationClient
	newGitHubValidationClient = func(ctx context.Context, token string) *github.Client {
		ts := oauth2.StaticTokenSource(&oauth2.Token{AccessToken: token})
		c := github.NewClient(oauth2.NewClient(ctx, ts))
		u, _ := url.Parse(server.URL + "/")
		c.BaseURL = u
		c.UploadURL = u
		return c
	}
	defer func() { newGitHubValidationClient = old }()

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

	threads := 2
	tv := NewTokenValidator(nil)
	var tokenResults []string
	var outcomeStatuses []verifiers.Status
	tv.OnTokenResult = func(token string, valid bool, provider string) {
		tokenResults = append(tokenResults, fmt.Sprintf("%s|%v|%s", token, valid, provider))
	}
	tv.OnOutcome = func(token string, provider string, o verifiers.Outcome) {
		outcomeStatuses = append(outcomeStatuses, o.Status)
	}

	s := newTestSession()
	s.Options = &Options{Threads: &threads}
	s.TokenValidator = tv
	s.FoundTokens = make(map[string]bool)

	good := "ghp_" + strings.Repeat("Ab3_", 9)
	if !s.AddGitHubToken(good) {
		t.Fatal("valid GitHub token must be added")
	}
	data, err := os.ReadFile(filepath.Join("logs", "priority3.log"))
	if err != nil {
		t.Fatalf("reading priority3.log: %v", err)
	}
	if !strings.Contains(string(data), good) {
		t.Fatalf("priority3.log does not contain the full valid token; got %q", string(data))
	}
	if len(tokenResults) != 1 || tokenResults[0] != (good[:17]+"...|true|GitHub") {
		t.Fatalf("OnTokenResult calls = %v, want one valid GitHub announcement", tokenResults)
	}
	if len(outcomeStatuses) != 1 || outcomeStatuses[0] != verifiers.Valid {
		t.Fatalf("OnOutcome statuses = %v, want one Valid", outcomeStatuses)
	}
	if len(s.Config.GitHubAccessTokens) != 1 || s.Config.GitHubAccessTokens[0] != good {
		t.Fatalf("pooled tokens = %v, want [%s]", s.Config.GitHubAccessTokens, good)
	}

	// A duplicate must be rejected without re-validating or re-announcing.
	if s.AddGitHubToken(good) {
		t.Fatal("duplicate token must return false")
	}
	if len(tokenResults) != 1 || len(outcomeStatuses) != 1 {
		t.Fatal("duplicate must not re-fire callbacks")
	}

	// An invalid token is rejected and never persisted or announced.
	bad := "ghp_bad" + strings.Repeat("Xy9_", 8)
	if s.AddGitHubToken(bad) {
		t.Fatal("invalid GitHub token must be rejected")
	}
	data, err = os.ReadFile(filepath.Join("logs", "priority3.log"))
	if err != nil {
		t.Fatalf("reading priority3.log: %v", err)
	}
	if strings.Contains(string(data), bad) {
		t.Fatalf("priority3.log must not contain the rejected token")
	}
	if len(tokenResults) != 1 || len(outcomeStatuses) != 1 {
		t.Fatal("rejection must not fire callbacks")
	}
}

// TestFreeClientDropsDuplicateReturn proves a return against a full pool does
// not wedge the worker: pool capacity covers every client in flight, so a full
// channel means a duplicate return and the extra is dropped with a warning.
func TestFreeClientDropsDuplicateReturn(t *testing.T) {
	s := newTestSession("ghp_a")
	mk := func() *GitHubClientWrapper { return &GitHubClientWrapper{Token: "ghp_a"} }
	for i := 0; i < cap(s.Clients); i++ {
		s.Clients <- mk()
	}
	done := make(chan struct{})
	go func() {
		defer close(done)
		s.FreeClient(mk())
	}()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("FreeClient blocked on a full pool, want drop-and-warn")
	}
	if got := len(s.Clients); got != cap(s.Clients) {
		t.Fatalf("pool holds %d clients, want full at %d", got, cap(s.Clients))
	}
}
