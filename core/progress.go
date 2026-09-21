package core

import (
	"sync/atomic"
)

// ProgressManager tracks aggregate counters for the secret scan.
type ProgressManager struct {
	rateLimited atomic.Int64

	// OnRateLimited, when set, runs every time the GitHub API rate limits a
	// token. The main package points it at the dashboard's activity counter:
	// this counter is only ever written here, and the number both UIs display
	// ("Rate limited", the TUI's "limited N") is read from that one, so without
	// the hook they reported zero for the whole run.
	OnRateLimited func()
}

// NewProgressManager creates a ProgressManager.
func NewProgressManager() *ProgressManager {
	return &ProgressManager{}
}

// IncrementRateLimited bumps the rate-limited counter and notifies the hook.
func (pm *ProgressManager) IncrementRateLimited() {
	pm.rateLimited.Add(1)
	if pm.OnRateLimited != nil {
		pm.OnRateLimited()
	}
}

// GitProgressWriter is a no-op progress writer.
type GitProgressWriter struct{}

func (w *GitProgressWriter) Write(p []byte) (n int, err error) {
	return len(p), nil
}
