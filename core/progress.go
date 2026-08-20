package core

import (
	"sync/atomic"
)

// ProgressManager tracks aggregate counters for the secret scan.
// Rendering was removed - use these counters for any future reporting.
type ProgressManager struct {
	rateLimited atomic.Int64
	totalFailed atomic.Int64
	disabled    bool

	// Deprecated callbacks (cleared after TUI removal)
	OnRateLimited func()
	OnFailed      func()
}

// NewProgressManager creates a ProgressManager (always disabled).
func NewProgressManager() *ProgressManager {
	return &ProgressManager{disabled: true}
}

// IncrementRateLimited bumps the rate-limited counter.
func (pm *ProgressManager) IncrementRateLimited() {
	pm.rateLimited.Add(1)
}

// IncrementFailed bumps the failed clone counter.
func (pm *ProgressManager) IncrementFailed() {
	pm.totalFailed.Add(1)
}

// RateLimitedCount returns the current rate-limited count.
func (pm *ProgressManager) RateLimitedCount() int64 {
	return pm.rateLimited.Load()
}

// IsDisabled returns true (always disabled after TUI removal).
func (pm *ProgressManager) IsDisabled() bool {
	return pm.disabled
}

// NewGitProgressWriter returns nil (progress consumption no longer needed).
func (pm *ProgressManager) NewGitProgressWriter(workerID int) *GitProgressWriter {
	return nil
}

// GitProgressWriter is a no-op progress writer.
type GitProgressWriter struct{}

func (w *GitProgressWriter) Write(p []byte) (n int, err error) {
	return len(p), nil
}
