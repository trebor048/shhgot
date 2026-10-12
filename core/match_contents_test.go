package core

import (
	"bytes"
	"os"
	"path/filepath"
	"testing"
)

// TestGetContentsKeepsHeadWhenFileShrank reproduces the shrink race: os.Stat
// recorded a Size larger than the file now holds, so only the 8 KiB head is
// readable and the remainder read returns io.EOF. GetContents must return the
// head it already read rather than discarding every byte.
func TestGetContentsKeepsHeadWhenFileShrank(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "shrink.env")
	head := bytes.Repeat([]byte("A"), 8*1024)
	if err := os.WriteFile(path, head, 0o600); err != nil {
		t.Fatal(err)
	}

	// Size claims 4 KiB more than the file now contains, so the remainder read
	// hits EOF with zero bytes read.
	m := &MatchFile{Path: path, Size: int64(len(head)) + 4096}

	got := m.GetContents()
	if len(got) != len(head) {
		t.Fatalf("GetContents returned %d bytes, want %d (the read head must be kept)", len(got), len(head))
	}
	if !bytes.Equal(got, head) {
		t.Fatal("GetContents returned content that does not match the file head")
	}
}
