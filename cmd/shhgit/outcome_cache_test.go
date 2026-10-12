package main

import (
	"fmt"
	"testing"

	"github.com/trebor048/shhgot/core/verifiers"
)

// resetOutcomeCache swaps in an empty cache and restores the previous one when
// the test finishes, so a bounded-cache test cannot disturb the process-global
// state other tests rely on.
func resetOutcomeCache(t *testing.T) {
	t.Helper()
	outcomeCacheMu.Lock()
	prevCache := outcomeCache
	prevOrder := outcomeCacheOrder
	outcomeCache = map[string]cachedOutcome{}
	outcomeCacheOrder = nil
	outcomeCacheMu.Unlock()
	t.Cleanup(func() {
		outcomeCacheMu.Lock()
		outcomeCache = prevCache
		outcomeCacheOrder = prevOrder
		outcomeCacheMu.Unlock()
	})
}

// The outcome cache is process-global and lived in an unbounded map: every
// distinct verified secret a long scan saw stayed resident forever. It must be
// capped, evicting the oldest entries once it is full.
func TestOutcomeCacheIsBounded(t *testing.T) {
	resetOutcomeCache(t)

	const over = 100
	for i := 0; i < maxOutcomeCache+over; i++ {
		// Keep the key at or below 20 bytes so maskedOutcomeKey returns it
		// unchanged and cachedFor can look it up.
		rememberOutcome(fmt.Sprintf("tok-%08d", i), "OpenAI", verifiers.Outcome{})
	}

	outcomeCacheMu.RLock()
	size := len(outcomeCache)
	order := len(outcomeCacheOrder)
	outcomeCacheMu.RUnlock()
	if size != maxOutcomeCache {
		t.Fatalf("cache size = %d, want the cap %d", size, maxOutcomeCache)
	}
	if order != size {
		t.Fatalf("eviction order has %d entries for a cache of %d; they must stay in step", order, size)
	}

	for i := 0; i < over; i++ {
		if _, _, ok := cachedFor(fmt.Sprintf("tok-%08d", i)); ok {
			t.Fatalf("entry %d should have been evicted as the oldest", i)
		}
	}
	if _, _, ok := cachedFor(fmt.Sprintf("tok-%08d", maxOutcomeCache+over-1)); !ok {
		t.Fatal("the most recent entry must survive eviction")
	}
}

// Re-remembering a secret must refresh its value, not append a second eviction
// slot that would evict a live entry early.
func TestOutcomeCacheRefreshesWithoutDuplicating(t *testing.T) {
	resetOutcomeCache(t)

	rememberOutcome("tok-repeat", "OpenAI", verifiers.Outcome{})
	rememberOutcome("tok-repeat", "OpenAI", verifiers.Outcome{})

	outcomeCacheMu.RLock()
	size := len(outcomeCache)
	order := len(outcomeCacheOrder)
	outcomeCacheMu.RUnlock()
	if size != 1 || order != 1 {
		t.Fatalf("cache=%d order=%d, want 1 and 1", size, order)
	}
}
