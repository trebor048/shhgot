package core

import (
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/trebor048/shhgot/core/store"
)

func storeBoolPtr(b bool) *bool { return &b }

// TestStoreConfigDefaults pins the off-by-default contract and every documented
// default: an absent storage section must not enable the store, and the path,
// TTL, entry cap and compaction threshold all fall back to the built-ins.
func TestStoreConfigDefaults(t *testing.T) {
	cfg := &Config{}
	if cfg.StoreEnabled() {
		t.Fatal("storage must default to disabled")
	}
	sc := cfg.StoreConfig()
	if sc.Enabled {
		t.Fatal("StoreConfig.Enabled must default to false")
	}
	if sc.Path != DefaultStorePath {
		t.Fatalf("path = %q, want %q", sc.Path, DefaultStorePath)
	}
	if sc.DedupeTTL != defaultStoreDedupeTTL {
		t.Fatalf("dedupe TTL = %s, want %s", sc.DedupeTTL, defaultStoreDedupeTTL)
	}
	if sc.MaxDedupeEntries != defaultStoreMaxDedupeEntries {
		t.Fatalf("max dedupe entries = %d, want %d", sc.MaxDedupeEntries, defaultStoreMaxDedupeEntries)
	}
	if sc.CompactAfter != defaultStoreCompactAfter {
		t.Fatalf("compact after = %d, want %d", sc.CompactAfter, defaultStoreCompactAfter)
	}
}

// TestStoreConfigOverrides asserts an explicit storage section wins, including a
// path padded with whitespace.
func TestStoreConfigOverrides(t *testing.T) {
	cfg := &Config{Storage: StorageConfig{
		Enabled:          storeBoolPtr(true),
		Path:             "  custom-findings.jsonl  ",
		DedupeTTLSeconds: intPtr(60),
		MaxDedupeEntries: intPtr(5),
		CompactAfter:     intPtr(7),
	}}
	if !cfg.StoreEnabled() {
		t.Fatal("explicit enabled:true must enable the store")
	}
	sc := cfg.StoreConfig()
	if !sc.Enabled {
		t.Fatal("StoreConfig.Enabled must be true")
	}
	if sc.Path != "custom-findings.jsonl" {
		t.Fatalf("path = %q, want trimmed value", sc.Path)
	}
	if sc.DedupeTTL != time.Minute {
		t.Fatalf("dedupe TTL = %s, want 1m", sc.DedupeTTL)
	}
	if sc.MaxDedupeEntries != 5 {
		t.Fatalf("max dedupe entries = %d, want 5", sc.MaxDedupeEntries)
	}
	if sc.CompactAfter != 7 {
		t.Fatalf("compact after = %d, want 7", sc.CompactAfter)
	}
}

// TestNilConfigStoreConfigIsSafe guards the nil-receiver path.
func TestNilConfigStoreConfigIsSafe(t *testing.T) {
	var cfg *Config
	if cfg.StoreEnabled() {
		t.Fatal("nil config must report the store disabled")
	}
	if sc := cfg.StoreConfig(); sc.Enabled {
		t.Fatal("nil config must yield a disabled store")
	}
}

// TestStoreDisabledCreatesNoFile is the acceptance test for "store disabled = no
// file created": even after an append the disabled store touches no disk.
func TestStoreDisabledCreatesNoFile(t *testing.T) {
	path := filepath.Join(t.TempDir(), "findings.jsonl")
	cfg := &Config{Storage: StorageConfig{Path: path}}

	st, err := store.New(cfg.StoreConfig())
	if err != nil {
		t.Fatalf("store.New: %v", err)
	}
	t.Cleanup(func() { _ = st.Close() })

	if err := st.Append(store.Record{Fingerprint: "fp", Signature: "sig"}); err != nil {
		t.Fatalf("append on disabled store: %v", err)
	}
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Fatalf("disabled store created %s (stat err = %v)", path, err)
	}
}
