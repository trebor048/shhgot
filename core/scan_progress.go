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

	// repoFilesProcessed and repoTotalFiles describe only the repository being
	// scanned right now, which is what ProgressPercent is derived from;
	// FilesProcessed above stays cumulative across the whole run. They are
	// atomics because several repository workers share this singleton, exactly
	// as they share CurrentRepo and CurrentFile below - the percentage is
	// therefore the latest worker's view, not a sum over concurrent scans.
	repoFilesProcessed atomic.Int64
	repoTotalFiles     atomic.Int64

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

// The fields below are read by GetSnapshot under mu, so every writer takes the
// same lock. They exist because nothing wrote this struct at all: the dashboard
// polls /api/scan/progress and rendered a panel that could only ever show zero.

// BeginScan records that a repository scan started.
func (sp *ScanProgress) BeginScan(repo string) {
	sp.repoFilesProcessed.Store(0)
	sp.repoTotalFiles.Store(0)
	sp.mu.Lock()
	sp.IsScanning = true
	sp.CurrentRepo = repo
	sp.CurrentFile = ""
	sp.ProgressPercent = 0
	sp.LastUpdateTime = time.Now()
	sp.mu.Unlock()
}

// SetTotalFiles records how many files the current repository scan will
// process. It is what lets FileScanned turn its counter into a percentage:
// nothing ever set ProgressPercent, so the dashboard's scan bar and the
// "scanning N%" status line sat at zero for the life of the process.
func (sp *ScanProgress) SetTotalFiles(n int) {
	if n < 0 {
		n = 0
	}
	sp.repoTotalFiles.Store(int64(n))
}

// EndScan records that a repository scan finished.
func (sp *ScanProgress) EndScan() {
	sp.mu.Lock()
	sp.IsScanning = false
	sp.CurrentRepo = ""
	sp.CurrentFile = ""
	sp.LastUpdateTime = time.Now()
	elapsed := time.Since(sp.StartTime).Seconds()
	if elapsed > 0 {
		sp.FilesPerSecond = float64(atomic.LoadInt64(&sp.FilesProcessed)) / elapsed
	}
	sp.mu.Unlock()
	atomic.AddInt64(&sp.ReposScanned, 1)
}

// FileScanned records one processed file.
func (sp *ScanProgress) FileScanned(file string) {
	done := sp.repoFilesProcessed.Add(1)
	total := sp.repoTotalFiles.Load()

	sp.mu.Lock()
	sp.CurrentFile = file
	sp.LastUpdateTime = time.Now()
	if total > 0 {
		pct := int(done * 100 / total)
		if pct > 100 {
			pct = 100
		}
		sp.ProgressPercent = pct
	}
	sp.mu.Unlock()

	atomic.AddInt64(&sp.FilesProcessed, 1)
}

// MatchFound bumps the match counter.
func (sp *ScanProgress) MatchFound() {
	atomic.AddInt64(&sp.MatchesFound, 1)
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
