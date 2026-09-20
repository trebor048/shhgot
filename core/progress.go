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

// GitProgressWriter is a no-op progress writer.
type GitProgressWriter struct{}

func (w *GitProgressWriter) Write(p []byte) (n int, err error) {
	return len(p), nil
}
