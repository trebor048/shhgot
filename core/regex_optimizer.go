package core

import (
	"fmt"
	"os"
	"regexp"
	"sync"
)

// RegexPattern represents a compiled regex pattern with metadata
type RegexPattern struct {
	Original   string
	Pattern    *regexp.Regexp
	Name       string
	Priority   int
	Part       string // "contents", "filename", "path"
	IsCompiled bool
	Error      string
}

// RegexOptimizer compiles each signature pattern once and keeps it, so the same
// pattern is never compiled twice.
type RegexOptimizer struct {
	patterns         map[string]*RegexPattern
	patternsList     []*RegexPattern   // Pre-allocated slice for iteration
	priorityPatterns [][]*RegexPattern // Patterns grouped by priority (0-3)
	mu               sync.RWMutex
	maxWorkers       int
	timeoutMs        int
	compiledCount    int
}

// GlobalRegexOptimizer is the singleton regex optimizer instance
var GlobalRegexOptimizer *RegexOptimizer

// InitGlobalOptimizer initializes the global regex optimizer
func InitGlobalOptimizer(maxWorkers int, timeoutMs int) {
	if GlobalRegexOptimizer == nil {
		GlobalRegexOptimizer = NewRegexOptimizer(maxWorkers, timeoutMs)
	}
}

// NewRegexOptimizer creates a new regex optimizer
func NewRegexOptimizer(maxWorkers int, timeoutMs int) *RegexOptimizer {
	ro := &RegexOptimizer{
		patterns:         make(map[string]*RegexPattern),
		patternsList:     make([]*RegexPattern, 0, 200), // Pre-allocate for ~200 patterns
		priorityPatterns: make([][]*RegexPattern, 4),    // Priority 0-3
		maxWorkers:       maxWorkers,
		timeoutMs:        timeoutMs,
		compiledCount:    0,
	}

	// Initialize priority buckets
	for i := 0; i < 4; i++ {
		ro.priorityPatterns[i] = make([]*RegexPattern, 0, 50)
	}

	return ro
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
		// stderr rather than the stdlib logger: log.Printf prefixes a timestamp
		// that no other shhgit output carries, which breaks the one-line-per-event
		// format the minimal preset promises.
		fmt.Fprintf(os.Stderr, "[REGEX] Error compiling pattern %s: %v\n", name, err)
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

// Stats returns compilation statistics
func (ro *RegexOptimizer) Stats() map[string]interface{} {
	ro.mu.RLock()
	defer ro.mu.RUnlock()

	return map[string]interface{}{
		"patterns_compiled": ro.compiledCount,
		"patterns_total":    len(ro.patternsList),
		"max_workers":       ro.maxWorkers,
		"priority_0":        len(ro.priorityPatterns[0]),
		"priority_1":        len(ro.priorityPatterns[1]),
		"priority_2":        len(ro.priorityPatterns[2]),
		"priority_3":        len(ro.priorityPatterns[3]),
	}
}
