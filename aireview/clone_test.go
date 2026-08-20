package aireview

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

func fakeClone(dst, url string) error {
	return os.WriteFile(filepath.Join(dst, "README.md"), []byte(url), 0o644)
}

func TestClonePoolEnsure(t *testing.T) {
	p := NewClonePool(t.TempDir(), fakeClone)
	path1, reused, err := p.Ensure("https://github.com/owner/repo.git")
	if err != nil {
		t.Fatal(err)
	}
	if reused {
		t.Fatal("first Ensure must clone, not reuse")
	}
	if filepath.Base(path1) != "owner-repo" {
		t.Fatalf("pool dir = %q, want owner-repo", path1)
	}
	if _, err := os.Stat(filepath.Join(path1, "README.md")); err != nil {
		t.Fatal(err)
	}
	path2, reused, err := p.Ensure("https://github.com/owner/repo.git")
	if err != nil {
		t.Fatal(err)
	}
	if !reused {
		t.Fatal("second Ensure must reuse the fresh clone")
	}
	if path1 != path2 {
		t.Fatalf("paths differ: %q vs %q", path1, path2)
	}
}

func TestClonePoolStale(t *testing.T) {
	p := NewClonePool(t.TempDir(), fakeClone)
	path1, _, err := p.Ensure("https://github.com/o/r")
	if err != nil {
		t.Fatal(err)
	}
	marker := filepath.Join(path1, ".cloned")
	old := time.Now().Add(-(MaxCloneFresh + time.Hour))
	if err := os.Chtimes(marker, old, old); err != nil {
		t.Fatal(err)
	}
	_, reused, err := p.Ensure("https://github.com/o/r")
	if err != nil {
		t.Fatal(err)
	}
	if reused {
		t.Fatal("stale clone must be re-cloned")
	}
}

func TestClonePoolBadURL(t *testing.T) {
	p := NewClonePool(t.TempDir(), fakeClone)
	if _, _, err := p.Ensure("not-a-url"); err == nil {
		t.Fatal("expected error for unparseable URL")
	}
}
