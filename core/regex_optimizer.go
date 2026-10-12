package core

import (
	"errors"
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

// RegexOptimizer compiles each distinct regex string once and keeps the result,
// while tracking one RegexPattern entry per signature. A compiled regex is
// shared between signatures that use the same expression, but each signature's
// name/priority/part is kept separate: keying the pattern entries by the regex
// string alone merged two signatures' metadata.
type RegexOptimizer struct {
	compiled         map[string]*regexp.Regexp // regex string -> compiled form
	compileErrs      map[string]error          // regex string -> compile failure
	patterns         map[string]*RegexPattern  // signature key -> pattern entry
	patternsList     []*RegexPattern           // Pre-allocated slice for iteration
	priorityPatterns [][]*RegexPattern         // Patterns grouped by priority (0-3)
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
		compiled:         make(map[string]*regexp.Regexp),
		compileErrs:      make(map[string]error),
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

// patternKey identifies one signature's pattern entry. The regex string alone is
// not enough: two signatures can share it while carrying different names and
// priorities, and each needs its own entry and priority bucket.
func patternKey(patternStr, name string, priority int, part string) string {
	return fmt.Sprintf("%s\x00%s\x00%d\x00%s", patternStr, name, priority, part)
}

// compileRegex compiles patternStr once and remembers both the result and any
// error, so signatures that share an expression do not recompile it.
func (ro *RegexOptimizer) compileRegex(patternStr string) (*regexp.Regexp, error) {
	if compiled, ok := ro.compiled[patternStr]; ok {
		return compiled, nil
	}
	if err, ok := ro.compileErrs[patternStr]; ok {
		return nil, err
	}
	compiled, err := regexp.Compile(patternStr)
	if err != nil {
		ro.compileErrs[patternStr] = err
		return nil, err
	}
	ro.compiled[patternStr] = compiled
	return compiled, nil
}

// CompilePattern compiles and caches a regex pattern for one signature. The
// compiled regex is shared by every signature with the same expression, but the
// returned entry carries this signature's own name, priority and part.
func (ro *RegexOptimizer) CompilePattern(patternStr string, name string, priority int, part string) (*RegexPattern, error) {
	ro.mu.Lock()
	defer ro.mu.Unlock()

	// Check if this exact signature was already compiled.
	key := patternKey(patternStr, name, priority, part)
	if cached, exists := ro.patterns[key]; exists {
		if cached.IsCompiled {
			return cached, nil
		}
		return cached, errors.New(cached.Error)
	}

	compiled, err := ro.compileRegex(patternStr)
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
		ro.patterns[key] = pattern
		return pattern, err
	}

	pattern := &RegexPattern{
		Original:   patternStr,
		Pattern:    compiled,
		Name:       name,
		Priority:   priority,
		Part:       part,
		IsCompiled: true,
	}

	ro.patterns[key] = pattern
	ro.patternsList = append(ro.patternsList, pattern)

	// Add to priority bucket (clamp to 0-3)
	bucket := priority
	if bucket < 0 {
		bucket = 0
	} else if bucket > 3 {
		bucket = 3
	}
	ro.priorityPatterns[bucket] = append(ro.priorityPatterns[bucket], pattern)

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
