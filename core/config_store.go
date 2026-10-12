package core

import (
	"strings"
	"time"

	"github.com/trebor048/shhgot/core/store"
)

// DefaultStorePath is where the findings journal lives when storage.path is
// absent. It is relative to the working directory, matching match_log_dir.
const DefaultStorePath = "shhgit-findings.jsonl"

// Defaults for the bounded dedupe index and journal compaction. They are only
// consulted when the corresponding storage key is absent; an explicit positive
// value wins.
const (
	defaultStoreDedupeTTL        = 24 * time.Hour
	defaultStoreMaxDedupeEntries = 100000
	defaultStoreCompactAfter     = 1000
)

// StoreEnabled reports whether the persistent findings store is enabled. The
// store is off unless storage.enabled is explicitly true, so an operator who
// has not opted in keeps the previous (stateless) behaviour.
func (c *Config) StoreEnabled() bool {
	if c == nil {
		return false
	}
	return boolOr(c.Storage.Enabled, false)
}

// StoreConfig maps the "storage" YAML section onto store.Config, filling in the
// documented defaults for absent keys. An absent path defaults to
// DefaultStorePath; the dedupe index is bounded by a 24h TTL and a 100k entry
// cap and the journal compacts every 1000 appends.
func (c *Config) StoreConfig() store.Config {
	if c == nil {
		return store.Config{}
	}
	path := strings.TrimSpace(c.Storage.Path)
	if path == "" {
		path = DefaultStorePath
	}
	return store.Config{
		Enabled:          c.StoreEnabled(),
		Path:             path,
		DedupeTTL:        secondsOr(c.Storage.DedupeTTLSeconds, defaultStoreDedupeTTL),
		MaxDedupeEntries: intOr(c.Storage.MaxDedupeEntries, defaultStoreMaxDedupeEntries),
		CompactAfter:     intOr(c.Storage.CompactAfter, defaultStoreCompactAfter),
	}
}
