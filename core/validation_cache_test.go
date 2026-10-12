package core

import (
	"fmt"
	"sync"
	"testing"
	"time"
)

type fakeClock struct {
	mu sync.Mutex
	t  time.Time
}

func newFakeClock() *fakeClock {
	return &fakeClock{t: time.Unix(1_700_000_000, 0)}
}

func (f *fakeClock) Now() time.Time {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.t
}

func (f *fakeClock) Advance(d time.Duration) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.t = f.t.Add(d)
}

func TestValidationCacheTTLExpiry(t *testing.T) {
	clock := newFakeClock()
	c := NewValidationCache(CacheConfig{
		Enabled:    true,
		TTL:        30 * time.Second,
		MaxEntries: 64,
		Now:        clock.Now,
	})
	const base = "https://api.example.test"

	c.Put("sk-repeat", base, CachedValidation{Status: "valid", Provider: "example"})
	got, ok := c.Get("sk-repeat", base)
	if !ok {
		t.Fatal("fresh entry missed")
	}
	if got.Provider != "example" || got.Status != "valid" {
		t.Fatalf("cached value = %+v, want status=valid provider=example", got)
	}

	clock.Advance(29 * time.Second)
	if _, ok := c.Get("sk-repeat", base); !ok {
		t.Error("entry expired before its TTL")
	}

	clock.Advance(2 * time.Second)
	if _, ok := c.Get("sk-repeat", base); ok {
		t.Error("entry served after its TTL elapsed")
	}
	if c.Len() != 0 {
		t.Errorf("Len = %d after expiry, want 0", c.Len())
	}

	// The base URL is part of the key, so the same credential against a
	// different endpoint must not be served the cached value.
	c.Put("sk-repeat", base, CachedValidation{Status: "valid"})
	if _, ok := c.Get("sk-repeat", "https://other.example.test"); ok {
		t.Error("entry leaked across base URLs")
	}
	if c.Len() != 1 {
		t.Errorf("Len = %d, want 1", c.Len())
	}
}

func TestValidationCacheLRUEviction(t *testing.T) {
	clock := newFakeClock()
	const base = "https://api.example.test"
	c := NewValidationCache(CacheConfig{
		Enabled:    true,
		TTL:        time.Minute,
		MaxEntries: validationCacheShards * 2,
		Now:        clock.Now,
	})

	keys := keysForShard(c, base, 0, 3)
	c.Put(keys[0], base, CachedValidation{Status: "valid"})
	c.Put(keys[1], base, CachedValidation{Status: "valid"})
	if _, ok := c.Get(keys[0], base); !ok {
		t.Fatal("key0 missing before eviction")
	}
	// key0 was just touched, so key1 is now the least recently used.
	c.Put(keys[2], base, CachedValidation{Status: "valid"})

	if _, ok := c.Get(keys[1], base); ok {
		t.Error("least recently used key was not evicted")
	}
	if _, ok := c.Get(keys[0], base); !ok {
		t.Error("refreshed key was evicted")
	}
	if _, ok := c.Get(keys[2], base); !ok {
		t.Error("newest key missing")
	}
	if got := c.Len(); got != 2 {
		t.Errorf("Len = %d, want 2 (only the populated shard holds entries)", got)
	}
}

func TestValidationCacheConcurrentGetPut(t *testing.T) {
	clock := newFakeClock()
	const base = "https://api.example.test"
	c := NewValidationCache(CacheConfig{
		Enabled:    true,
		TTL:        time.Minute,
		MaxEntries: 512,
		Now:        clock.Now,
	})

	var wg sync.WaitGroup
	for g := 0; g < 8; g++ {
		wg.Add(1)
		go func(g int) {
			defer wg.Done()
			for i := 0; i < 500; i++ {
				key := fmt.Sprintf("key-%d", (g*500+i)%97)
				c.Put(key, base, CachedValidation{Status: "valid"})
				_, _ = c.Get(key, base)
				_ = c.Len()
				c.RecordDomainFailure("flaky.example.test")
				c.RecordDomainSuccess("flaky.example.test")
				_ = c.DomainDead("flaky.example.test")
				_ = c.DomainStatus("flaky.example.test")
			}
		}(g)
	}
	wg.Wait()
}

func TestValidationCacheDisabledStoresNothing(t *testing.T) {
	c := NewValidationCache(CacheConfig{Enabled: false, TTL: time.Minute, MaxEntries: 16})
	c.Put("k", "https://api.example.test", CachedValidation{Status: "valid"})
	if _, ok := c.Get("k", "https://api.example.test"); ok {
		t.Error("disabled cache served a value")
	}
	if c.Len() != 0 {
		t.Errorf("Len = %d, want 0", c.Len())
	}
}

func TestDomainStateTransitions(t *testing.T) {
	c := NewValidationCache(CacheConfig{Enabled: true, Now: newFakeClock().Now})
	const host = "flaky.example.test"

	if got := c.DomainStatus(host); got != DomainHealthy {
		t.Errorf("fresh host = %v, want HEALTHY", got)
	}
	c.RecordDomainFailure(host)
	if got := c.DomainStatus(host); got != DomainHealthy {
		t.Errorf("after 1 failure = %v, want HEALTHY", got)
	}
	c.RecordDomainFailure(host)
	if got := c.DomainStatus(host); got != DomainDegraded {
		t.Errorf("after 2 failures = %v, want DEGRADED", got)
	}
	for i := 0; i < 3; i++ {
		c.RecordDomainFailure(host)
	}
	if got := c.DomainStatus(host); got != DomainUnhealthy {
		t.Errorf("after 5 failures = %v, want UNHEALTHY", got)
	}
	for i := 0; i < 5; i++ {
		c.RecordDomainFailure(host)
	}
	if got := c.DomainStatus(host); got != DomainDead {
		t.Errorf("after 10 failures = %v, want DEAD", got)
	}
	if !c.DomainDead(host) {
		t.Error("host at the dead threshold was not short-circuited")
	}
	if got := c.DomainStatus("other.example.test"); got != DomainHealthy {
		t.Errorf("unrelated host = %v, want HEALTHY", got)
	}
}

func TestDomainRecoveryAfterSuccesses(t *testing.T) {
	c := NewValidationCache(CacheConfig{Enabled: true, Now: newFakeClock().Now})
	const host = "recovering.example.test"

	c.RecordDomainFailure(host)
	c.RecordDomainFailure(host)
	if got := c.DomainStatus(host); got != DomainDegraded {
		t.Fatalf("setup state = %v, want DEGRADED", got)
	}

	c.RecordDomainSuccess(host)
	c.RecordDomainSuccess(host)
	if got := c.DomainStatus(host); got != DomainDegraded {
		t.Fatalf("state after 2 successes = %v, want DEGRADED", got)
	}
	c.RecordDomainSuccess(host)
	if got := c.DomainStatus(host); got != DomainHealthy {
		t.Fatalf("state after 3 successes = %v, want HEALTHY", got)
	}
}

func TestDomainDeadShortCircuitThenRecovers(t *testing.T) {
	clock := newFakeClock()
	const cooldown = 2 * time.Minute
	c := NewValidationCache(CacheConfig{Enabled: true, Cooldown: cooldown, Now: clock.Now})
	const host = "dead.example.test"

	for i := 0; i < domainDeadAt; i++ {
		c.RecordDomainFailure(host)
	}
	if !c.DomainDead(host) {
		t.Fatal("host at the dead threshold was not short-circuited")
	}

	clock.Advance(cooldown - time.Second)
	if !c.DomainDead(host) {
		t.Error("host retried before the cooldown elapsed")
	}

	clock.Advance(2 * time.Second)
	if c.DomainDead(host) {
		t.Fatal("host still short-circuited after the cooldown elapsed")
	}
	if got := c.DomainStatus(host); got != DomainHealthy {
		t.Errorf("cooldown recovery left state = %v, want HEALTHY", got)
	}

	c.RecordDomainFailure(host)
	if c.DomainDead(host) {
		t.Error("a single failure after recovery re-deadened the host")
	}
}

func TestDomainProtectedHostNeverDead(t *testing.T) {
	c := NewValidationCache(CacheConfig{
		Enabled:        true,
		Now:            newFakeClock().Now,
		ProtectedHosts: []string{"internal.example.test"},
	})

	hosts := []string{
		"api.openai.com",
		"api.anthropic.com",
		"generativelanguage.googleapis.com",
		"api.x.ai",
		"api.anthropic.com:443",
		"internal.example.test",
	}
	for _, host := range hosts {
		for i := 0; i < domainDeadAt*2; i++ {
			c.RecordDomainFailure(host)
		}
		if c.DomainDead(host) {
			t.Errorf("protected host %q was reported dead", host)
		}
		if got := c.DomainStatus(host); got != DomainHealthy {
			t.Errorf("protected host %q = %v, want HEALTHY", host, got)
		}
	}
}

// TestValidationCacheGlobalCapWithSmallMaxEntries proves the cache never holds
// more than MaxEntries even when the cap is below the shard count. One key per
// fixed 16-way shard is distinct pre-fix, so a fixed 16-shard split would keep
// all 16 and exceed an 8-entry cap.
func TestValidationCacheGlobalCapWithSmallMaxEntries(t *testing.T) {
	clock := newFakeClock()
	const base = "https://api.example.test"
	const max = 8
	c := NewValidationCache(CacheConfig{
		Enabled:    true,
		TTL:        time.Hour,
		MaxEntries: max,
		Now:        clock.Now,
	})

	for shard := 0; shard < validationCacheShards; shard++ {
		c.Put(keyForFixedShard(base, shard), base, CachedValidation{Status: "valid"})
	}
	if got := c.Len(); got > max {
		t.Fatalf("Len = %d, exceeds configured MaxEntries %d", got, max)
	}
}

// keyForFixedShard returns a key whose digest maps to the requested fixed
// 16-way shard, independent of the cache's active shard count.
func keyForFixedShard(base string, shard int) string {
	for i := 0; ; i++ {
		k := fmt.Sprintf("boundary-key-%d", i)
		if int(cacheKeyHash(k, base)[0])%validationCacheShards == shard {
			return k
		}
	}
}

// keysForShard returns n distinct keys that all hash into the requested shard
// for base, so LRU behaviour can be exercised on a single shard.
func keysForShard(c *ValidationCache, base string, shard, n int) []string {
	keys := make([]string, 0, n)
	for i := 0; len(keys) < n; i++ {
		k := fmt.Sprintf("probe-key-%d", i)
		if c.shardIndex(cacheKeyHash(k, base)) == shard {
			keys = append(keys, k)
		}
		if i > 1_000_000 {
			panic("no shard collision found")
		}
	}
	return keys
}

func TestValidationCacheRemainderCapacity(t *testing.T) {
	clock := newFakeClock()
	const base = "https://api.example.test"
	const maxEntries = 17
	c := NewValidationCache(CacheConfig{
		Enabled:    true,
		TTL:        time.Hour,
		MaxEntries: maxEntries,
		Now:        clock.Now,
	})

	// Distribute keys across shards to exercise the full capacity.
	// We need to put at most `cap` entries per shard.
	// For 17 entries with 16 shards: 1 shard gets 2, 15 shards get 1.
	// So we can put 2 keys in shard 0, and 1 key in shards 1-15.
	put := 0
	for shard := 0; shard < validationCacheShards && put < maxEntries; shard++ {
		cap := c.shards[shard].cap
		keys := keysForShard(c, base, shard, cap)
		for i, k := range keys {
			if put >= maxEntries {
				break
			}
			c.Put(k, base, CachedValidation{Status: "valid"})
			put++
			if c.Len() != put {
				t.Errorf("after putting key %d (shard %d, idx %d): Len = %d, want %d", put, shard, i, c.Len(), put)
			}
		}
	}

	if got := c.Len(); got != maxEntries {
		t.Errorf("Len = %d, want %d (all entries should fit with remainder distribution)", got, maxEntries)
	}
}

// TestDomainMapZeroValueSafe proves a zero-value cache never panics on the
// domain path: RecordDomainFailure lazy-inits the map instead of writing to
// nil.
func TestDomainMapZeroValueSafe(t *testing.T) {
	var c ValidationCache
	c.RecordDomainFailure("evil.example.test")
	c.RecordDomainSuccess("evil.example.test")
	if c.DomainDead("evil.example.test") {
		t.Error("zero-value cache reports a host dead, want always-retryable")
	}
	if got := c.DomainStatus("evil.example.test"); got != DomainHealthy {
		t.Errorf("zero-value status = %v, want healthy", got)
	}
}

// TestDomainMapBounded proves attacker-influenced distinct hosts cannot grow
// the health map without limit.
func TestDomainMapBounded(t *testing.T) {
	c := NewValidationCache(CacheConfig{Enabled: true})
	for i := 0; i < maxDomainRecords+500; i++ {
		c.RecordDomainFailure(fmt.Sprintf("host-%d.example.test", i))
	}
	c.domainsMu.Lock()
	n := len(c.domains)
	c.domainsMu.Unlock()
	if n > maxDomainRecords {
		t.Errorf("domains = %d entries, want at most %d", n, maxDomainRecords)
	}
}
