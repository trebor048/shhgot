package aireview

import (
	"archive/zip"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestArchiveJob(t *testing.T) {
	root := t.TempDir()
	s, err := NewJobStore(root)
	if err != nil {
		t.Fatal(err)
	}
	if err := s.Create(&Job{ID: "jz", RepoURL: "https://x/y", Secret: "s", State: StateDone}); err != nil {
		t.Fatal(err)
	}
	if err := s.AppendLog("jz", "hello"); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(s.jobDir("jz"), "result.md"), []byte("# report"), 0o644); err != nil {
		t.Fatal(err)
	}
	ev := filepath.Join(s.jobDir("jz"), "evidence")
	if err := os.MkdirAll(ev, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(ev, "01-auth.response.json"), []byte("{}"), 0o644); err != nil {
		t.Fatal(err)
	}

	zipPath, err := ArchiveJob(root, "jz")
	if err != nil {
		t.Fatal(err)
	}
	f, err := zip.OpenReader(zipPath)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	var names []string
	for _, zf := range f.File {
		names = append(names, zf.Name)
	}
	joined := strings.Join(names, ",")
	for _, want := range []string{"job.json", "logs.txt", "result.md", "evidence/01-auth.response.json"} {
		if !strings.Contains(joined, want) {
			t.Fatalf("zip missing %q; has %q", want, joined)
		}
	}
	// original files still present (non-destructive)
	if _, err := os.Stat(filepath.Join(s.jobDir("jz"), "job.json")); err != nil {
		t.Fatal("archive must not delete originals")
	}
	// empty reader check not needed; io import used for signature
	var _ io.Reader
}
