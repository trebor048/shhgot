package core

// ProgressManager tracks aggregate counters for the secret scan.
type ProgressManager struct {
	// OnRateLimited, when set, runs every time the GitHub API rate limits a
	// token. The dashboard's activity counter is the single source of truth the
	// UIs read ("Rate limited", the TUI's "limited N"), so the count lives there
	// rather than in a second field here that nothing ever read.
	OnRateLimited func()
}

// NewProgressManager creates a ProgressManager.
func NewProgressManager() *ProgressManager {
	return &ProgressManager{}
}

// IncrementRateLimited notifies the rate-limited hook.
func (pm *ProgressManager) IncrementRateLimited() {
	if pm.OnRateLimited != nil {
		pm.OnRateLimited()
	}
}
