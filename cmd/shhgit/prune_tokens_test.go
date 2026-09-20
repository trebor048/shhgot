package main

import (
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestCheckGitHubTokenVerdicts(t *testing.T) {
	cases := []struct {
		name   string
		status int
		want   tokenVerdict
	}{
		{"valid", http.StatusOK, verdictValid},
		{"revoked", http.StatusUnauthorized, verdictRevoked},
		{"rate limited", http.StatusForbidden, verdictAmbiguous},
		{"server error", http.StatusInternalServerError, verdictAmbiguous},
		{"not found", http.StatusNotFound, verdictAmbiguous},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.WriteHeader(c.status)
			}))
			defer srv.Close()

			oldClient, oldURL := pruneHTTPClient, pruneGitHubUserURL
			pruneHTTPClient, pruneGitHubUserURL = srv.Client(), srv.URL
			defer func() { pruneHTTPClient, pruneGitHubUserURL = oldClient, oldURL }()

			if got := checkGitHubToken("ghp_test"); got != c.want {
				t.Fatalf("checkGitHubToken(status=%d) = %v, want %v", c.status, got, c.want)
			}
		})
	}
}

func TestCheckGitHubTokenSendsAuthAndUserAgent(t *testing.T) {
	var gotAuth, gotUA string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotAuth = r.Header.Get("Authorization")
		gotUA = r.Header.Get("User-Agent")
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	oldClient, oldURL := pruneHTTPClient, pruneGitHubUserURL
	pruneHTTPClient, pruneGitHubUserURL = srv.Client(), srv.URL
	defer func() { pruneHTTPClient, pruneGitHubUserURL = oldClient, oldURL }()

	checkGitHubToken("ghp_secret")
	if gotAuth != "token ghp_secret" {
		t.Fatalf("Authorization = %q, want %q", gotAuth, "token ghp_secret")
	}
	if gotUA == "" {
		t.Fatal("User-Agent must be set (GitHub rejects requests without it)")
	}
}

func TestCheckGitHubTokenNetworkErrorIsAmbiguous(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {}))
	url, client := srv.URL, srv.Client()
	srv.Close() // connection refused afterwards

	oldClient, oldURL := pruneHTTPClient, pruneGitHubUserURL
	pruneHTTPClient, pruneGitHubUserURL = client, url
	defer func() { pruneHTTPClient, pruneGitHubUserURL = oldClient, oldURL }()

	if got := checkGitHubToken("ghp_test"); got != verdictAmbiguous {
		t.Fatalf("network error should be ambiguous, got %v", got)
	}
}

func TestClassifyTokens(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.Header.Get("Authorization") {
		case "token ghp_valid":
			w.WriteHeader(http.StatusOK)
		case "token ghp_revoked":
			w.WriteHeader(http.StatusUnauthorized)
		case "token ghp_ratelimited":
			w.WriteHeader(http.StatusForbidden)
		default:
			w.WriteHeader(http.StatusInternalServerError)
		}
	}))
	defer srv.Close()

	oldClient, oldURL := pruneHTTPClient, pruneGitHubUserURL
	pruneHTTPClient, pruneGitHubUserURL = srv.Client(), srv.URL
	defer func() { pruneHTTPClient, pruneGitHubUserURL = oldClient, oldURL }()

	t.Setenv("PRUNE_TEST_VALID", "ghp_valid")
	t.Setenv("PRUNE_TEST_REVOKED", "ghp_revoked")

	removed, kept, ambiguous := classifyTokens([]string{
		"ghp_valid",
		"ghp_revoked",
		"ghp_ratelimited",
		"ghp_unknown",
		"$PRUNE_TEST_VALID",
		"$PRUNE_TEST_REVOKED",
	})
	if !removed["ghp_revoked"] || !removed["$PRUNE_TEST_REVOKED"] {
		t.Fatalf("revoked tokens must be removed, got %v", removed)
	}
	if len(removed) != 2 {
		t.Fatalf("removed = %v, want exactly {ghp_revoked, $PRUNE_TEST_REVOKED}", removed)
	}
	if kept != 2 { // ghp_valid + $PRUNE_TEST_VALID
		t.Fatalf("kept = %d, want 2", kept)
	}
	if ambiguous != 2 { // ghp_ratelimited + ghp_unknown
		t.Fatalf("ambiguous = %d, want 2", ambiguous)
	}
}

func TestScanGitHubTokenBlock(t *testing.T) {
	lines := []string{
		"other: 1",
		"github_access_tokens:",
		"  - ghp_one",
		"  - ghp_two",
		"",
		"# a comment",
		"  - $GH_TOKEN",
		"  - 'ghp_quoted_single'",
		`  - "ghp_quoted_double"`,
		"after_key: value",
		"  - ghp_not_in_block",
	}
	items := scanGitHubTokenBlock(lines)
	got := make([]string, 0, len(items))
	indices := make([]int, 0, len(items))
	for _, it := range items {
		got = append(got, it.value)
		indices = append(indices, it.index)
	}
	want := []string{"ghp_one", "ghp_two", "$GH_TOKEN", "ghp_quoted_single", "ghp_quoted_double"}
	if strings.Join(got, "|") != strings.Join(want, "|") {
		t.Fatalf("values = %v, want %v", got, want)
	}
	if indices[0] != 2 || indices[1] != 3 || indices[2] != 6 {
		t.Fatalf("indices = %v, want [2 3 6 ...]", indices)
	}
}

func TestClassifyTokensStripsQuotes(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.Header.Get("Authorization") {
		case "token ghp_valid":
			w.WriteHeader(http.StatusOK)
		case "token ghp_revoked":
			w.WriteHeader(http.StatusUnauthorized)
		default:
			w.WriteHeader(http.StatusInternalServerError)
		}
	}))
	defer srv.Close()

	oldClient, oldURL := pruneHTTPClient, pruneGitHubUserURL
	pruneHTTPClient, pruneGitHubUserURL = srv.Client(), srv.URL
	defer func() { pruneHTTPClient, pruneGitHubUserURL = oldClient, oldURL }()

	// Raw config lines may quote the token; quotes must be stripped before the
	// token is sent to GitHub, otherwise a valid token would come back 401 and
	// be wrongly removed.
	removed, kept, ambiguous := classifyTokens([]string{"'ghp_valid'", `"ghp_revoked"`})
	if len(removed) != 1 || !removed[`"ghp_revoked"`] {
		t.Fatalf("removed = %v, want exactly {\"ghp_revoked\"} (quotes stripped, 401)", removed)
	}
	if kept != 1 {
		t.Fatalf("kept = %d, want 1 ('ghp_valid' must be stripped and count as valid)", kept)
	}
	if ambiguous != 0 {
		t.Fatalf("ambiguous = %d, want 0", ambiguous)
	}
}

func TestExtractGitHubTokenLines(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "config.yaml")
	content := "github_access_tokens:\n  - ghp_one\n  - ghp_two\n\n# comment\nafter: value\n"
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}

	values, err := extractGitHubTokenLines(path)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Join(values, "|") != "ghp_one|ghp_two" {
		t.Fatalf("values = %v, want [ghp_one ghp_two]", values)
	}
}

func TestMatchTokenLines(t *testing.T) {
	t.Setenv("MATCH_TEST_TOKEN", "ghp_from_env")

	raw := []string{"ghp_literal", "$MATCH_TEST_TOKEN", "ghp_other"}
	removed := matchTokenLines(raw, "ghp_from_env")
	if len(removed) != 1 || !removed["$MATCH_TEST_TOKEN"] {
		t.Fatalf("removed = %v, want exactly {$MATCH_TEST_TOKEN}", removed)
	}

	removed = matchTokenLines(raw, "ghp_literal")
	if len(removed) != 1 || !removed["ghp_literal"] {
		t.Fatalf("removed = %v, want exactly {ghp_literal}", removed)
	}

	if got := matchTokenLines(raw, "ghp_missing"); len(got) != 0 {
		t.Fatalf("removed = %v, want empty for a token not in the file", got)
	}
}

func TestRewriteConfigRemovingTokens(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "config.yaml")
	orig := "github_access_tokens:\n  - ghp_keep\n  - ghp_remove1\n  - ghp_remove2\n\n# comment stays\nafter: value\n"
	if err := os.WriteFile(path, []byte(orig), 0o644); err != nil {
		t.Fatal(err)
	}

	n, err := rewriteConfigRemovingTokens(path, map[string]bool{"ghp_remove1": true, "ghp_remove2": true})
	if err != nil {
		t.Fatal(err)
	}
	if n != 2 {
		t.Fatalf("removed %d lines, want 2", n)
	}

	got, _ := os.ReadFile(path)
	want := "github_access_tokens:\n  - ghp_keep\n\n# comment stays\nafter: value\n"
	if string(got) != want {
		t.Fatalf("rewritten config:\n%q\nwant:\n%q", got, want)
	}

	bak, err := os.ReadFile(path + ".bak")
	if err != nil {
		t.Fatalf("backup not written: %v", err)
	}
	if string(bak) != orig {
		t.Fatal("backup must equal the original config")
	}
}

func TestRewriteConfigRemovingTokensNoop(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "config.yaml")
	orig := "github_access_tokens:\n  - ghp_keep\n\nother: 1\n"
	if err := os.WriteFile(path, []byte(orig), 0o644); err != nil {
		t.Fatal(err)
	}

	n, err := rewriteConfigRemovingTokens(path, map[string]bool{"nope": true})
	if err != nil {
		t.Fatal(err)
	}
	if n != 0 {
		t.Fatalf("removed %d lines, want 0", n)
	}
	got, _ := os.ReadFile(path)
	if string(got) != orig {
		t.Fatal("file must be untouched when nothing is removed")
	}
	if _, err := os.Stat(path + ".bak"); !os.IsNotExist(err) {
		t.Fatal("no backup should be written when nothing is removed")
	}
}

func TestRewriteConfigRemovingTokensPreservesCRLF(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "config.yaml")
	orig := "github_access_tokens:\r\n  - ghp_keep\r\n  - 'ghp_remove'\r\n\r\n# comment stays\r\nafter: value\r\n"
	if err := os.WriteFile(path, []byte(orig), 0o644); err != nil {
		t.Fatal(err)
	}

	n, err := rewriteConfigRemovingTokens(path, map[string]bool{"ghp_remove": true})
	if err != nil {
		t.Fatal(err)
	}
	if n != 1 {
		t.Fatalf("removed %d lines, want 1", n)
	}
	got, _ := os.ReadFile(path)
	want := "github_access_tokens:\r\n  - ghp_keep\r\n\r\n# comment stays\r\nafter: value\r\n"
	if string(got) != want {
		t.Fatalf("CRLF not preserved:\n%q\nwant:\n%q", got, want)
	}
}
