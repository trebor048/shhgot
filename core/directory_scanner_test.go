package core

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"testing"
	"time"
)

// A worker error must be reported without panicking. The old implementation
// stored errors in an atomic.Value, which panics when two workers store errors
// of different concrete types, and its producer could block forever on the
// items channel once every worker had returned on error.
func TestFastWalkReportsWorkerError(t *testing.T) {
	dir := t.TempDir()
	for i := 0; i < 50; i++ {
		if err := os.WriteFile(filepath.Join(dir, fmt.Sprintf("file-%02d", i)), []byte("x"), 0o644); err != nil {
			t.Fatalf("write fixture: %v", err)
		}
	}

	sentinel := errors.New("boom")
	done := make(chan error, 1)
	go func() {
		done <- FastWalk(dir, func(path string, info os.FileInfo, err error) error {
			if info != nil && !info.IsDir() {
				return sentinel
			}
			return nil
		}, 4)
	}()

	select {
	case err := <-done:
		if !errors.Is(err, sentinel) {
			t.Fatalf("FastWalk error = %v, want %v", err, sentinel)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("FastWalk hung instead of returning the worker error")
	}
}

// GetFileCount must still count every file when no worker errors.
func TestFastWalkCountsFiles(t *testing.T) {
	dir := t.TempDir()
	for i := 0; i < 20; i++ {
		if err := os.WriteFile(filepath.Join(dir, fmt.Sprintf("file-%02d", i)), []byte("x"), 0o644); err != nil {
			t.Fatalf("write fixture: %v", err)
		}
	}

	ds := NewDirectoryScanner(nil)
	if got := ds.GetFileCount(dir); got != 20 {
		t.Fatalf("GetFileCount = %d, want 20", got)
	}
}
