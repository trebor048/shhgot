package main

import (
	"strings"
	"testing"
)

// GitHub code search returns gist hits as host-prefixed slugs
// ("gist.github.com/<id>"); searchRepoURL must add only the scheme for those,
// never a second "github.com/".
func TestSearchRepoURLHostAware(t *testing.T) {
	cases := map[string]string{
		"owner/repo":                     "https://github.com/owner/repo",
		"owner/repo.git":                 "https://github.com/owner/repo",
		"gist.github.com/abc123":         "https://gist.github.com/abc123",
		"gist.github.com/abc123.git":     "https://gist.github.com/abc123",
		"https://gist.github.com/abc123": "https://gist.github.com/abc123",
		"":                               "",
	}
	for in, want := range cases {
		if got := searchRepoURL(in); got != want {
			t.Errorf("searchRepoURL(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestGistFileURL(t *testing.T) {
	if got, ok := gistFileURL("https://gist.github.com/abc123", "poskds_token.txt"); !ok ||
		got != "https://gist.github.com/abc123#file-poskds_token-txt" {
		t.Errorf("gist file url = %q ok=%v", got, ok)
	}
	if got, ok := gistFileURL("https://gist.github.com/abc123.git", ""); !ok ||
		got != "https://gist.github.com/abc123" {
		t.Errorf("gist clone url = %q ok=%v", got, ok)
	}
	if _, ok := gistFileURL("https://github.com/o/r", "a.go"); ok {
		t.Error("a github repository URL must not be treated as a gist")
	}
	if _, ok := gistFileURL("https://gist.github.com/", "a.go"); ok {
		t.Error("a gist URL with no id must not match")
	}
}

func TestGistAnchor(t *testing.T) {
	cases := map[string]string{
		"poskds_token.txt": "poskds_token-txt",
		"Hello World.py":   "hello-world-py",
		"a/b/c.env":        "c-env",
	}
	for in, want := range cases {
		if got := gistAnchor(in); got != want {
			t.Errorf("gistAnchor(%q) = %q, want %q", in, got, want)
		}
	}
}

// The malformed "https://github.com/gist.github.com/<id>/blob/..." link must not
// come back: a gist finding resolves to the gist host, never a github.com blob.
func TestGetEnhancedLinkInfoGistIsNotBlobbed(t *testing.T) {
	userRepo, _, fileLink := getEnhancedLinkInfo(
		"https://gist.github.com/a67e5de3271d6d0716b276dc6a8391cb", "poskds_token.txt", "main", 1)
	if userRepo != "gist" {
		t.Errorf("userRepo = %q, want %q", userRepo, "gist")
	}
	for _, bad := range []string{"github.com/gist.github.com", "/blob/"} {
		if strings.Contains(fileLink, bad) {
			t.Errorf("gist link %q must not contain %q", fileLink, bad)
		}
	}
}
