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
	ReposScanned      int64
	FilesProcessed    int64
	MatchesFound      int64
	ErrorsEncountered int64

	// Current status
	CurrentRepo    string
	CurrentFile    string
	IsScanning     bool
	StartTime      time.Time
	LastUpdateTime time.Time

	// Speed metrics
	FilesPerSecond float64
	ReposPerSecond float64

	// Progress percentage (0-100)
	ProgressPercent int
}

// GlobalScanProgress is the singleton scan progress tracker
var GlobalScanProgress = &ScanProgress{
	StartTime:      time.Now(),
	LastUpdateTime: time.Now(),
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
		"is_scanning":              sp.IsScanning,
		"progress":                 sp.ProgressPercent,
		"repos_scanned":            atomic.LoadInt64(&sp.ReposScanned),
		"files_processed":          atomic.LoadInt64(&sp.FilesProcessed),
		"matches_found":            atomic.LoadInt64(&sp.MatchesFound),
		"errors":                   atomic.LoadInt64(&sp.ErrorsEncountered),
		"current_repo":             sp.CurrentRepo,
		"current_file":             sp.CurrentFile,
		"speed":                    sp.FilesPerSecond,
		"elapsed_seconds":          int(elapsedSeconds),
		"estimated_time_remaining": estimatedTimeRemaining,
	}
}
