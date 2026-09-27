package core

import (
	"bytes"
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
)

func TestIsBinaryContent(t *testing.T) {
	if IsBinaryContent([]byte("hello world")) {
		t.Error("plain text flagged as binary")
	}
	if !IsBinaryContent([]byte{'a', 0, 'b'}) {
		t.Error("NUL byte not flagged as binary")
	}
	// A NUL past the sniff window is not seen: only the head is inspected.
	long := append(bytes.Repeat([]byte("a"), 9*1024), 0)
	if IsBinaryContent(long) {
		t.Error("NUL beyond the sniff window should not flag the file")
	}
}

func TestBranchName(t *testing.T) {
	cases := map[string]string{
		"refs/heads/main": "main",
		"refs/tags/v1.0":  "v1.0",
		"main":            "main",
		"":                "",
		// An unhandled ref namespace is passed through rather than guessed at.
		"refs/pull/1/head": "refs/pull/1/head",
	}
	for in, want := range cases {
		if got := branchName(in); got != want {
			t.Errorf("branchName(%q) = %q, want %q", in, got, want)
		}
	}
}

// TestCloneNativeLocalRepo proves the native path produces a usable checkout,
// so the pure-Go fallback only runs when git is genuinely unavailable.
func TestCloneNativeLocalRepo(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git is not on PATH")
	}

	src := t.TempDir()
	run := func(args ...string) {
		t.Helper()
		cmd := exec.Command("git", args...)
		cmd.Dir = src
		cmd.Env = append(os.Environ(),
			"GIT_TERMINAL_PROMPT=0",
			"GIT_CONFIG_NOSYSTEM=1",
		)
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v (%s)", args, err, out)
		}
	}
	run("init", "-q", "-b", "main")
	run("config", "user.email", "test@example.com")
	run("config", "user.name", "test")
	if err := os.WriteFile(filepath.Join(src, "f.txt"), []byte("hello"), 0o600); err != nil {
		t.Fatal(err)
	}
	run("add", ".")
	run("commit", "-q", "-m", "init")

	dst := filepath.Join(t.TempDir(), "clone")
	if !cloneNative(context.Background(), src, "refs/heads/main", dst, 1, nil) {
		t.Fatal("cloneNative failed on a local repository")
	}
	if _, err := os.Stat(filepath.Join(dst, "f.txt")); err != nil {
		t.Fatalf("cloned tree is missing the committed file: %v", err)
	}
}
