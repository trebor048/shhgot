package core

import (
	"crypto/sha256"
	"encoding/base64"
	"log"
	"regexp"
	"strings"
	"sync"
	"sync/atomic"
)

// RegexPattern represents a compiled regex pattern with metadata
type RegexPattern struct {
	Original    string
	Pattern     *regexp.Regexp
	Name        string
	Priority    int
	Part        string // "contents", "filename", "path"
	IsCompiled  bool
	Error       string
}

// RegexOptimizer handles compiled pattern caching and parallel matching
type RegexOptimizer struct {
	patterns         map[string]*RegexPattern
	patternsList     []*RegexPattern          // Pre-allocated slice for iteration
	priorityPatterns [][]*RegexPattern        // Patterns grouped by priority (0-3)
	cache            *FastLRUCache
	mu               sync.RWMutex
	maxWorkers       int
	timeoutMs        int
	cacheSize        int
	enableCache      bool
	hits             int64
	misses           int64
	compiledCount    int
	cacheSem         chan struct{} // Semaphore for cache operations
}

// cacheEntry represents an entry in the LRU cache
type cacheEntry struct {
	key   string
	value bool
	prev  *cacheEntry
	next  *cacheEntry
}

// FastLRUCache implements O(1) LRU cache with doubly-linked list
type FastLRUCache struct {
	cache map[string]*cacheEntry
	head  *cacheEntry
	tail  *cacheEntry
	size  int32
	max   int32
	mu    sync.RWMutex
}

// GlobalRegexOptimizer is the singleton regex optimizer instance
var GlobalRegexOptimizer *RegexOptimizer

// InitGlobalOptimizer initializes the global regex optimizer
func InitGlobalOptimizer(maxWorkers int, cacheSize int, timeoutMs int, enableCache bool) {
	if GlobalRegexOptimizer == nil {
		GlobalRegexOptimizer = NewRegexOptimizer(maxWorkers, cacheSize, timeoutMs, enableCache)
		log.Printf("[REGEX] Global optimizer initialized: %d workers, cache=%d, timeout=%dms", 
			maxWorkers, cacheSize, timeoutMs)
	}
}

// NewRegexOptimizer creates a new regex optimizer
func NewRegexOptimizer(maxWorkers int, cacheSize int, timeoutMs int, enableCache bool) *RegexOptimizer {
	ro := &RegexOptimizer{
		patterns:         make(map[string]*RegexPattern),
		patternsList:     make([]*RegexPattern, 0, 200), // Pre-allocate for ~200 patterns
		priorityPatterns: make([][]*RegexPattern, 4),     // Priority 0-3
		cache:            NewFastLRUCache(cacheSize),
		maxWorkers:       maxWorkers,
		timeoutMs:        timeoutMs,
		cacheSize:        cacheSize,
		enableCache:      enableCache,
		compiledCount:    0,
		cacheSem:         make(chan struct{}, 100), // Limit concurrent cache ops
	}

	// Initialize priority buckets
	for i := 0; i < 4; i++ {
		ro.priorityPatterns[i] = make([]*RegexPattern, 0, 50)
	}

	return ro
}

// NewFastLRUCache creates a new O(1) LRU cache
func NewFastLRUCache(maxSize int) *FastLRUCache {
	return &FastLRUCache{
		cache: make(map[string]*cacheEntry, maxSize),
		max:   int32(maxSize),
		size:  0,
	}
}

// CompilePattern compiles and caches a regex pattern
func (ro *RegexOptimizer) CompilePattern(patternStr string, name string, priority int, part string) (*RegexPattern, error) {
	ro.mu.Lock()
	defer ro.mu.Unlock()

	// Check if already compiled
	if cached, exists := ro.patterns[patternStr]; exists {
		return cached, nil
	}

	// Compile the pattern
	compiled, err := regexp.Compile(patternStr)
	if err != nil {
		log.Printf("[REGEX] Error compiling pattern %s: %v", name, err)
		pattern := &RegexPattern{
			Original:   patternStr,
			Name:       name,
			Priority:   priority,
			Part:       part,
			IsCompiled: false,
			Error:      err.Error(),
		}
		ro.patterns[patternStr] = pattern
		return pattern, err
	}

	// Cache the compiled pattern
	pattern := &RegexPattern{
		Original:   patternStr,
		Pattern:    compiled,
		Name:       name,
		Priority:   priority,
		Part:       part,
		IsCompiled: true,
	}

	ro.patterns[patternStr] = pattern
	ro.patternsList = append(ro.patternsList, pattern)
	
	// Add to priority bucket (clamp to 0-3)
	if priority < 0 {
		priority = 0
	} else if priority > 3 {
		priority = 3
	}
	ro.priorityPatterns[priority] = append(ro.priorityPatterns[priority], pattern)
	
	ro.compiledCount++

	return pattern, nil
}

// generateCacheKey generates a fast cache key using hash for long strings
func generateCacheKey(patternName string, text string) string {
	const maxLen = 64
	if len(text) <= maxLen {
		// For short strings, use direct concatenation
		var sb strings.Builder
		sb.Grow(len(patternName) + 1 + len(text))
		sb.WriteString(patternName)
		sb.WriteByte(':')
		sb.WriteString(text)
		return sb.String()
	}
	
	// For long strings, use hash
	h := sha256.New()
	h.Write([]byte(text))
	hash := base64.RawURLEncoding.EncodeToString(h.Sum(nil))[:16]
	
	var sb strings.Builder
	sb.Grow(len(patternName) + 1 + 16)
	sb.WriteString(patternName)
	sb.WriteByte(':')
	sb.WriteString(hash)
	return sb.String()
}

// MatchString performs optimized regex matching without goroutine overhead
func (ro *RegexOptimizer) MatchString(pattern *RegexPattern, text string) (bool, error) {
	if !pattern.IsCompiled {
		return false, nil
	}

	// Check cache first (fast path)
	if ro.enableCache {
		cacheKey := generateCacheKey(pattern.Name, text)
		if result, ok := ro.cache.Get(cacheKey); ok {
			atomic.AddInt64(&ro.hits, 1)
			return result, nil
		}
		atomic.AddInt64(&ro.misses, 1)

		// Perform match
		result := pattern.Pattern.MatchString(text)

		// Cache the result (non-blocking)
		ro.cache.Set(cacheKey, result)
		
		return result, nil
	}

	// No cache - direct match
	return pattern.Pattern.MatchString(text), nil
}

// FindAllString performs regex matching without goroutine overhead
func (ro *RegexOptimizer) FindAllString(pattern *RegexPattern, text string, n int) ([]string, error) {
	if !pattern.IsCompiled {
		return nil, nil
	}

	return pattern.Pattern.FindAllString(text, n), nil
}

// ParallelMatch matches patterns with priority-based early exit
func (ro *RegexOptimizer) ParallelMatch(patterns []*RegexPattern, text string) map[string]bool {
	results := make(map[string]bool, len(patterns))
	
	// Use pre-allocated patterns list for better performance
	if len(patterns) == 0 {
		ro.mu.RLock()
		patterns = ro.patternsList
		ro.mu.RUnlock()
	}

	// Priority-based matching: critical patterns first (priority 3 -> 0)
	for priority := 3; priority >= 0; priority-- {
		ro.mu.RLock()
		priorityPatterns := ro.priorityPatterns[priority]
		ro.mu.RUnlock()

		if len(priorityPatterns) == 0 {
			continue
		}

		// Batch process patterns at this priority level
		var wg sync.WaitGroup
		var mu sync.Mutex
		matchCount := 0

		for _, pattern := range priorityPatterns {
			wg.Add(1)
			go func(p *RegexPattern) {
				defer wg.Done()

				if matched, err := ro.MatchString(p, text); err == nil && matched {
					mu.Lock()
					results[p.Name] = true
					matchCount++
					mu.Unlock()
				}
			}(pattern)
		}

		wg.Wait()

		// Early exit if critical match found (priority 3)
		if priority == 3 && matchCount > 0 {
			// Critical secrets found, can skip lower priorities
			log.Printf("[REGEX] Early exit: %d critical matches found", matchCount)
			return results
		}
	}

	return results
}

// BatchMatch processes multiple patterns sequentially (no goroutines for small batches)
func (ro *RegexOptimizer) BatchMatch(patterns []*RegexPattern, text string) map[string]bool {
	results := make(map[string]bool, len(patterns))
	
	for _, pattern := range patterns {
		if matched, err := ro.MatchString(pattern, text); err == nil {
			results[pattern.Name] = matched
		}
	}
	
	return results
}

// Stats returns cache and compilation statistics
func (ro *RegexOptimizer) Stats() map[string]interface{} {
	ro.mu.RLock()
	defer ro.mu.RUnlock()

	hits := atomic.LoadInt64(&ro.hits)
	misses := atomic.LoadInt64(&ro.misses)
	
	hitRate := float64(0)
	totalHits := hits + misses
	if totalHits > 0 {
		hitRate = float64(hits) / float64(totalHits) * 100
	}

	return map[string]interface{}{
		"patterns_compiled": ro.compiledCount,
		"patterns_total":    len(ro.patternsList),
		"cache_size":        atomic.LoadInt32(&ro.cache.size),
		"cache_max":         ro.cache.max,
		"cache_hits":        hits,
		"cache_misses":      misses,
		"cache_hit_rate":    hitRate,
		"max_workers":       ro.maxWorkers,
		"priority_0":        len(ro.priorityPatterns[0]),
		"priority_1":        len(ro.priorityPatterns[1]),
		"priority_2":        len(ro.priorityPatterns[2]),
		"priority_3":        len(ro.priorityPatterns[3]),
	}
}

// Get retrieves a compiled pattern by name
func (ro *RegexOptimizer) Get(patternStr string) *RegexPattern {
	ro.mu.RLock()
	defer ro.mu.RUnlock()
	return ro.patterns[patternStr]
}

// GetPatternsList returns the pre-allocated patterns list
func (ro *RegexOptimizer) GetPatternsList() []*RegexPattern {
	ro.mu.RLock()
	defer ro.mu.RUnlock()
	return ro.patternsList
}

// FastLRUCache methods - O(1) operations

// Get retrieves a value from cache in O(1) time
func (c *FastLRUCache) Get(key string) (bool, bool) {
	c.mu.RLock()
	entry, ok := c.cache[key]
	c.mu.RUnlock()
	
	if !ok {
		return false, false
	}

	// Move to front (most recently used)
	c.mu.Lock()
	c.moveToFront(entry)
	c.mu.Unlock()
	
	return entry.value, true
}

// Set stores a value in cache in O(1) time
func (c *FastLRUCache) Set(key string, value bool) {
	c.mu.Lock()
	defer c.mu.Unlock()

	// Check if already exists
	if entry, ok := c.cache[key]; ok {
		entry.value = value
		c.moveToFront(entry)
		return
	}

	// Create new entry
	entry := &cacheEntry{
		key:   key,
		value: value,
	}

	// Add to front
	c.cache[key] = entry
	c.addToFront(entry)
	atomic.AddInt32(&c.size, 1)

	// Evict if over capacity
	if atomic.LoadInt32(&c.size) > c.max {
		c.evictLRU()
	}
}

// moveToFront moves an entry to the front of the list
func (c *FastLRUCache) moveToFront(entry *cacheEntry) {
	if entry == c.head {
		return
	}

	// Remove from current position
	if entry.prev != nil {
		entry.prev.next = entry.next
	}
	if entry.next != nil {
		entry.next.prev = entry.prev
	}
	if entry == c.tail {
		c.tail = entry.prev
	}

	// Add to front
	c.addToFront(entry)
}

// addToFront adds an entry to the front of the list
func (c *FastLRUCache) addToFront(entry *cacheEntry) {
	entry.next = c.head
	entry.prev = nil

	if c.head != nil {
		c.head.prev = entry
	}
	c.head = entry

	if c.tail == nil {
		c.tail = entry
	}
}

// evictLRU removes the least recently used entry
func (c *FastLRUCache) evictLRU() {
	if c.tail == nil {
		return
	}

	// Remove tail (LRU)
	delete(c.cache, c.tail.key)
	
	if c.tail.prev != nil {
		c.tail.prev.next = nil
		c.tail = c.tail.prev
	} else {
		c.head = nil
		c.tail = nil
	}

	atomic.AddInt32(&c.size, -1)
}

// Size returns current cache size
func (c *FastLRUCache) Size() int {
	return int(atomic.LoadInt32(&c.size))
}

// Clear empties the cache
func (c *FastLRUCache) Clear() {
	c.mu.Lock()
	defer c.mu.Unlock()

	c.cache = make(map[string]*cacheEntry, c.max)
	c.head = nil
	c.tail = nil
	atomic.StoreInt32(&c.size, 0)
}


