package core

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

// TestCleanupSkipsLiveDirectories proves eviction never removes a checkout
// registered with BeginDir - even when a stale processed entry exists for it -
// and removes it once EndDir runs.
func TestCleanupSkipsLiveDirectories(t *testing.T) {
	root := t.TempDir()
	live := filepath.Join(root, "repo-live")
	if err := os.Mkdir(live, 0755); err != nil {
		t.Fatal(err)
	}
	// Enough bytes to exceed a 0 MB ceiling.
	if err := os.WriteFile(filepath.Join(live, "blob.bin"), make([]byte, 2<<20), 0644); err != nil {
		t.Fatal(err)
	}

	cm := NewCleanupManager(root, 0, &Logger{})
	cm.cleanupIntervalSecs = 0
	cm.lastCleanupTime = time.Now().Add(-time.Hour)

	// Stale "done" entry from a previous pass, then a fresh live scan.
	cm.MarkProcessed(live)
	cm.BeginDir(live)
	if err := cm.CleanupIfNeeded(); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(live); err != nil {
		t.Fatalf("live checkout was evicted: %v", err)
	}

	cm.EndDir(live)
	if err := cm.CleanupIfNeeded(); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(live); !os.IsNotExist(err) {
		t.Fatalf("finished checkout survived eviction (stat err %v)", err)
	}
}

// TestBeginEndDirRefcount proves nested registration needs matching EndDir
// calls before the path becomes evictable again.
func TestBeginEndDirRefcount(t *testing.T) {
	cm := NewCleanupManager(t.TempDir(), 0, nil)
	cm.BeginDir("d")
	cm.BeginDir("d")
	cm.EndDir("d")
	cm.Lock()
	_, live := cm.activeDirectories["d"]
	cm.Unlock()
	if !live {
		t.Fatal("doubly-registered dir became evictable after one EndDir")
	}
	cm.EndDir("d")
	cm.Lock()
	_, live = cm.activeDirectories["d"]
	cm.Unlock()
	if live {
		t.Fatal("dir stayed registered after balanced EndDir calls")
	}
	// Unbalanced EndDir must not panic or go negative.
	cm.EndDir("d")
}
