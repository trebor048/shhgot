package core

import (
	"container/list"
	"crypto/sha256"
	"encoding/hex"
	"net"
	"strings"
	"sync"
	"time"
)

// validationCacheShards is fixed at 16 so a handful of hot providers do not
// serialise every validation lookup behind a single mutex.
const validationCacheShards = 16

// cacheKeySeparator delimits the credential from the endpoint in the hashed
// cache key. It cannot appear in either part, so two distinct pairs never
// collapse onto the same hash input.
const cacheKeySeparator = "\x00"

const (
	defaultValidationTTL        = 5 * time.Minute
	defaultValidationMaxEntries = 4096
	defaultDomainCooldown       = 15 * time.Minute

	domainDegradedAt   = 2
	domainUnhealthyAt  = 5
	domainDeadAt       = 10
	domainRecoverAfter = 3
)

// defaultProtectedHosts are first-party AI endpoints. They are always treated
// as protected; CacheConfig.ProtectedHosts adds to this set rather than
// replacing it, so an operator can protect an extra host without losing the
// guarantee for the built-in ones.
var defaultProtectedHosts = []string{
	"api.openai.com",
	"api.anthropic.com",
	"generativelanguage.googleapis.com",
	"api.x.ai",
}

// CachedValidation is the outcome of one credential validation, keyed in the
// cache by the credential plus its base URL.
type CachedValidation struct {
	Status    string
	Provider  string
	Tier      string
	Balance   string
	Identity  string
	RPM       int
	HighValue bool
}

// CacheConfig configures a ValidationCache. The zero value disables caching;
// the operator opts in through the validators section of config.yaml.
type CacheConfig struct {
	Enabled        bool
	TTL            time.Duration
	MaxEntries     int
	Cooldown       time.Duration
	Now            func() time.Time
	ProtectedHosts []string
}

// DomainState is the health bucket for a validation host.
type DomainState int

const (
	DomainHealthy DomainState = iota
	DomainDegraded
	DomainUnhealthy
	DomainDead
)

func (s DomainState) String() string {
	switch s {
	case DomainHealthy:
		return "HEALTHY"
	case DomainDegraded:
		return "DEGRADED"
	case DomainUnhealthy:
		return "UNHEALTHY"
	case DomainDead:
		return "DEAD"
	default:
		return "UNKNOWN"
	}
}

type cacheEntry struct {
	key       string
	value     CachedValidation
	expiresAt time.Time
}

type cacheShard struct {
	mu    sync.Mutex
	cap   int
	items map[string]*list.Element
	lru   *list.List
}

type domainRecord struct {
	failures  int
	successes int
	state     DomainState
	deadSince time.Time
}

// ValidationCache is a sharded TTL + LRU cache of validation results with an
// accompanying per-host health tracker. Every time source goes through the
// injected clock so tests never sleep.
type ValidationCache struct {
	enabled  bool
	ttl      time.Duration
	cooldown time.Duration
	now      func() time.Time

	shards     [validationCacheShards]cacheShard
	shardCount int

	domainsMu sync.Mutex
	domains   map[string]*domainRecord
	protected map[string]bool
}

// NewValidationCache builds a cache from cfg, substituting defaults for any
// absent value. A nil Now falls back to time.Now.
func NewValidationCache(cfg CacheConfig) *ValidationCache {
	c := &ValidationCache{
		enabled: cfg.Enabled,
		ttl:     cfg.TTL,
		now:     cfg.Now,
	}
	if c.ttl <= 0 {
		c.ttl = defaultValidationTTL
	}
	c.cooldown = cfg.Cooldown
	if c.cooldown <= 0 {
		c.cooldown = defaultDomainCooldown
	}
	if c.now == nil {
		c.now = time.Now
	}

	maxEntries := cfg.MaxEntries
	if maxEntries <= 0 {
		maxEntries = defaultValidationMaxEntries
	}
	// Only shard as many ways as the cap can support: with MaxEntries below
	// validationCacheShards, a fixed 16-way split would give every shard a
	// capacity of one and let the cache hold 16 entries over a smaller cap.
	shardCount := maxEntries
	if shardCount > validationCacheShards {
		shardCount = validationCacheShards
	}
	perShard := maxEntries / shardCount
	remainder := maxEntries % shardCount
	c.shardCount = shardCount
	for i := 0; i < shardCount; i++ {
		cap := perShard
		if i < remainder {
			cap++
		}
		if cap < 1 {
			cap = 1
		}
		c.shards[i].cap = cap
		c.shards[i].items = make(map[string]*list.Element)
		c.shards[i].lru = list.New()
	}

	c.protected = make(map[string]bool, len(defaultProtectedHosts)+len(cfg.ProtectedHosts))
	for _, h := range defaultProtectedHosts {
		if n := normalizeHost(h); n != "" {
			c.protected[n] = true
		}
	}
	for _, h := range cfg.ProtectedHosts {
		if n := normalizeHost(h); n != "" {
			c.protected[n] = true
		}
	}

	c.domains = make(map[string]*domainRecord)
	return c
}

// Get returns the cached validation for key at base while it is within the
// TTL; an expired entry is removed and reported as a miss. A hit refreshes the
// entry's LRU position.
func (c *ValidationCache) Get(key, base string) (CachedValidation, bool) {
	if c == nil || !c.enabled {
		return CachedValidation{}, false
	}
	h := cacheKeyHash(key, base)
	sh := &c.shards[c.shardIndex(h)]
	k := hex.EncodeToString(h[:])

	sh.mu.Lock()
	defer sh.mu.Unlock()

	el, ok := sh.items[k]
	if !ok {
		return CachedValidation{}, false
	}
	ent := el.Value.(*cacheEntry)
	if !c.now().Before(ent.expiresAt) {
		sh.lru.Remove(el)
		delete(sh.items, k)
		return CachedValidation{}, false
	}
	sh.lru.MoveToFront(el)
	return ent.value, true
}

// Put stores a validation result for key at base, evicting the least recently
// used entry of the target shard when it is at capacity.
func (c *ValidationCache) Put(key, base string, v CachedValidation) {
	if c == nil || !c.enabled {
		return
	}
	h := cacheKeyHash(key, base)
	sh := &c.shards[c.shardIndex(h)]
	k := hex.EncodeToString(h[:])
	expires := c.now().Add(c.ttl)

	sh.mu.Lock()
	defer sh.mu.Unlock()

	if el, ok := sh.items[k]; ok {
		ent := el.Value.(*cacheEntry)
		ent.value = v
		ent.expiresAt = expires
		sh.lru.MoveToFront(el)
		return
	}

	el := sh.lru.PushFront(&cacheEntry{key: k, value: v, expiresAt: expires})
	sh.items[k] = el
	for len(sh.items) > sh.cap {
		back := sh.lru.Back()
		if back == nil {
			break
		}
		victim := back.Value.(*cacheEntry)
		sh.lru.Remove(back)
		delete(sh.items, victim.key)
	}
}

// Len reports how many entries are currently held across all shards,
// including entries whose TTL has elapsed but which have not yet been read or
// evicted.
func (c *ValidationCache) Len() int {
	if c == nil {
		return 0
	}
	total := 0
	for i := range c.shards {
		c.shards[i].mu.Lock()
		total += len(c.shards[i].items)
		c.shards[i].mu.Unlock()
	}
	return total
}

// maxDomainRecords caps the per-host health map: hosts derive from URLs found
// in scanned repos, so attacker-influenced distinct hosts would otherwise grow
// it without bound.
const maxDomainRecords = 4096

// RecordDomainFailure counts a failed validation against host and moves it
// down the health buckets. Protected hosts are never penalised.
func (c *ValidationCache) RecordDomainFailure(host string) {
	if c == nil {
		return
	}
	h := normalizeHost(host)
	if h == "" || c.protected[h] {
		return
	}

	c.domainsMu.Lock()
	defer c.domainsMu.Unlock()

	// Lazy-init for a zero-value cache (maps are otherwise made by
	// NewValidationCache): writing to a nil map panics in the scan path.
	if c.domains == nil {
		c.domains = make(map[string]*domainRecord)
	}
	now := c.now
	if now == nil {
		now = time.Now
	}

	rec := c.domains[h]
	if rec == nil {
		if len(c.domains) >= maxDomainRecords {
			c.evictDomainLocked(now)
			if len(c.domains) >= maxDomainRecords {
				// Still full of non-evictable hosts: skip tracking this one
				// rather than growing without bound.
				return
			}
		}
		rec = &domainRecord{}
		c.domains[h] = rec
	}
	rec.failures++
	rec.successes = 0

	switch {
	case rec.failures >= domainDeadAt:
		if rec.state != DomainDead {
			rec.deadSince = now()
		}
		rec.state = DomainDead
	case rec.failures >= domainUnhealthyAt:
		rec.state = DomainUnhealthy
	case rec.failures >= domainDegradedAt:
		rec.state = DomainDegraded
	}
}

// evictDomainLocked frees one health-map slot, preferring a healthy entry and
// then a cooldown-expired dead one. Callers must hold domainsMu.
func (c *ValidationCache) evictDomainLocked(now func() time.Time) {
	for h, rec := range c.domains {
		if rec.state == DomainHealthy {
			delete(c.domains, h)
			return
		}
	}
	for h, rec := range c.domains {
		if rec.state == DomainDead && now().Sub(rec.deadSince) >= c.cooldown {
			delete(c.domains, h)
			return
		}
	}
}

// RecordDomainSuccess counts a successful validation against host. After
// domainRecoverAfter consecutive successes a degraded or unhealthy host is
// returned to HEALTHY. A success on a dead host revives it immediately.
func (c *ValidationCache) RecordDomainSuccess(host string) {
	if c == nil {
		return
	}
	h := normalizeHost(host)
	if h == "" {
		return
	}

	c.domainsMu.Lock()
	defer c.domainsMu.Unlock()

	rec := c.domains[h]
	if rec == nil {
		return
	}
	if rec.state == DomainDead {
		rec.failures = 0
		rec.successes = 0
		rec.state = DomainHealthy
		rec.deadSince = time.Time{}
		return
	}
	rec.successes++
	if rec.successes >= domainRecoverAfter {
		rec.failures = 0
		rec.successes = 0
		rec.state = DomainHealthy
	}
}

// DomainDead reports whether host is currently short-circuited. Once the
// cooldown has elapsed the host becomes retryable again and its counters are
// reset. Protected hosts are never dead.
func (c *ValidationCache) DomainDead(host string) bool {
	if c == nil {
		return false
	}
	h := normalizeHost(host)
	if h == "" || c.protected[h] {
		return false
	}

	c.domainsMu.Lock()
	defer c.domainsMu.Unlock()

	rec := c.domains[h]
	if rec == nil || rec.state != DomainDead {
		return false
	}
	if c.now().Sub(rec.deadSince) >= c.cooldown {
		rec.failures = 0
		rec.successes = 0
		rec.state = DomainHealthy
		rec.deadSince = time.Time{}
		return false
	}
	return true
}

// DomainStatus reports the current health bucket for host without applying the
// cooldown. It is intended for logging and dashboards.
func (c *ValidationCache) DomainStatus(host string) DomainState {
	if c == nil {
		return DomainHealthy
	}
	h := normalizeHost(host)
	if h == "" || c.protected[h] {
		return DomainHealthy
	}

	c.domainsMu.Lock()
	defer c.domainsMu.Unlock()

	rec := c.domains[h]
	if rec == nil {
		return DomainHealthy
	}
	return rec.state
}

// shardIndex reports which shard the digest h maps to. Get and Put route
// through it so the sharding scheme has a single definition.
func (c *ValidationCache) shardIndex(h [sha256.Size]byte) int {
	return int(h[0]) % c.shardCount
}

func cacheKeyHash(key, base string) [sha256.Size]byte {
	return sha256.Sum256([]byte(key + cacheKeySeparator + base))
}

// normalizeHost lowercases a host and strips an optional port and IPv6
// brackets so equivalent spellings share one health record.
func normalizeHost(host string) string {
	h := strings.ToLower(strings.TrimSpace(host))
	if h == "" {
		return ""
	}
	if hostOnly, _, err := net.SplitHostPort(h); err == nil {
		h = hostOnly
	}
	return strings.Trim(h, "[]")
}
