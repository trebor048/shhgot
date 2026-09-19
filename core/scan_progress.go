package core

import (
	"sync"
	"sync/atomic"
	"time"
)

// ScanProgress tracks the progress of an active scan
type ScanProgress struct {
	mu sync.RWMutex

	// Counters
	ReposScanned    int64
	FilesProcessed  int64
	MatchesFound    int64
	ErrorsEncountered int64

	// Current status
	CurrentRepo     string
	CurrentFile     string
	IsScanning      bool
	StartTime       time.Time
	LastUpdateTime  time.Time

	// Speed metrics
	FilesPerSecond  float64
	ReposPerSecond  float64

	// Cache metrics
	CacheHitRate    float64
	PatternsCompiled int

	// Progress percentage (0-100)
	ProgressPercent int
}

// GlobalScanProgress is the singleton scan progress tracker
var GlobalScanProgress = &ScanProgress{
	StartTime:      time.Now(),
	LastUpdateTime: time.Now(),
}

// StartScan marks the beginning of a scan
func (sp *ScanProgress) StartScan() {
	sp.mu.Lock()
	defer sp.mu.Unlock()

	sp.IsScanning = true
	sp.StartTime = time.Now()
	sp.LastUpdateTime = time.Now()
	sp.ReposScanned = 0
	sp.FilesProcessed = 0
	sp.MatchesFound = 0
	sp.ErrorsEncountered = 0
	sp.ProgressPercent = 0
	sp.CurrentRepo = ""
	sp.CurrentFile = ""
}

// EndScan marks the end of a scan
func (sp *ScanProgress) EndScan() {
	sp.mu.Lock()
	defer sp.mu.Unlock()

	sp.IsScanning = false
	sp.ProgressPercent = 100
	sp.LastUpdateTime = time.Now()
}

// UpdateProgress updates scan statistics
func (sp *ScanProgress) UpdateProgress(repo string, file string, matchCount int) {
	sp.mu.Lock()
	defer sp.mu.Unlock()

	sp.CurrentRepo = repo
	sp.CurrentFile = file
	atomic.AddInt64(&sp.FilesProcessed, 1)
	atomic.AddInt64(&sp.MatchesFound, int64(matchCount))
	sp.LastUpdateTime = time.Now()

	// Update speed
	elapsed := time.Since(sp.StartTime).Seconds()
	if elapsed > 0 {
		sp.FilesPerSecond = float64(atomic.LoadInt64(&sp.FilesProcessed)) / elapsed
	}
}

// IncrementReposScanned increments the repo counter
func (sp *ScanProgress) IncrementReposScanned() {
	sp.mu.Lock()
	defer sp.mu.Unlock()

	atomic.AddInt64(&sp.ReposScanned, 1)

	elapsed := time.Since(sp.StartTime).Seconds()
	if elapsed > 0 {
		sp.ReposPerSecond = float64(atomic.LoadInt64(&sp.ReposScanned)) / elapsed
	}
}

// RecordError increments error counter
func (sp *ScanProgress) RecordError() {
	atomic.AddInt64(&sp.ErrorsEncountered, 1)
}

// SetProgress sets the progress percentage
func (sp *ScanProgress) SetProgress(percent int) {
	sp.mu.Lock()
	defer sp.mu.Unlock()

	if percent > 100 {
		percent = 100
	}
	if percent < 0 {
		percent = 0
	}
	sp.ProgressPercent = percent
}

// SetCacheHitRate sets the regex cache hit rate
func (sp *ScanProgress) SetCacheHitRate(rate float64) {
	sp.mu.Lock()
	defer sp.mu.Unlock()

	sp.CacheHitRate = rate
}

// SetPatternsCompiled sets the compiled patterns count
func (sp *ScanProgress) SetPatternsCompiled(count int) {
	sp.mu.Lock()
	defer sp.mu.Unlock()

	sp.PatternsCompiled = count
}

// GetSnapshot returns a snapshot of current progress
func (sp *ScanProgress) GetSnapshot() map[string]interface{} {
	sp.mu.RLock()
	defer sp.mu.RUnlock()

	elapsedSeconds := time.Since(sp.StartTime).Seconds()
	estimatedTimeRemaining := 0
	if sp.FilesPerSecond > 0 && sp.ProgressPercent > 0 {
		totalEstimated := int(float64(sp.FilesProcessed) / (float64(sp.ProgressPercent) / 100))
		estimatedTimeRemaining = int((float64(totalEstimated) - float64(sp.FilesProcessed)) / sp.FilesPerSecond)
	}

	return map[string]interface{}{
		"is_scanning":                sp.IsScanning,
		"progress":                   sp.ProgressPercent,
		"repos_scanned":              atomic.LoadInt64(&sp.ReposScanned),
		"files_processed":            atomic.LoadInt64(&sp.FilesProcessed),
		"matches_found":              atomic.LoadInt64(&sp.MatchesFound),
		"errors":                     atomic.LoadInt64(&sp.ErrorsEncountered),
		"current_repo":               sp.CurrentRepo,
		"current_file":               sp.CurrentFile,
		"speed":                      sp.FilesPerSecond,
		"elapsed_seconds":            int(elapsedSeconds),
		"estimated_time_remaining":   estimatedTimeRemaining,
		"cache_hit_rate":             sp.CacheHitRate,
		"patterns_compiled":          sp.PatternsCompiled,
	}
}

// Reset clears all statistics
func (sp *ScanProgress) Reset() {
	sp.mu.Lock()
	defer sp.mu.Unlock()

	sp.IsScanning = false
	sp.ReposScanned = 0
	sp.FilesProcessed = 0
	sp.MatchesFound = 0
	sp.ErrorsEncountered = 0
	sp.ProgressPercent = 0
	sp.CurrentRepo = ""
	sp.CurrentFile = ""
	sp.FilesPerSecond = 0
	sp.ReposPerSecond = 0
	sp.CacheHitRate = 0
	sp.PatternsCompiled = 0
}
