package main

import (
	"testing"
	"time"
)

// TestPublishGatesTUIBufferOnMode is a regression test for the always-true
// `tuiState != nil` gate: tuiState is never nil, so every terminal/web run
// retained every finding in the TUI buffer forever. The buffer must only be fed
// while the interactive TUI is the active mode.
func TestPublishGatesTUIBufferOnMode(t *testing.T) {
	hub := ensureWebHub()

	prev := modeConfig
	t.Cleanup(func() { modeConfig = prev })

	resetTUI := func() {
		tuiState.mu.Lock()
		tuiState.matches = nil
		tuiState.mu.Unlock()
	}
	tuiCount := func() int {
		tuiState.mu.Lock()
		defer tuiState.mu.Unlock()
		return len(tuiState.matches)
	}

	// Terminal mode: publish must not touch the TUI buffer.
	resetHubForTest(hub)
	resetTUI()
	modeConfig = &ModeConfig{Mode: ModeDefault}
	newStoreTestSession(t, false)
	publish(testMatchEvent())
	waitForCond(t, 2*time.Second, func() bool { return hubMatchCount(t) == 1 })
	if got := tuiCount(); got != 0 {
		t.Fatalf("terminal mode retained %d finding(s) in the TUI buffer, want 0", got)
	}

	// TUI mode: the buffer still receives findings.
	resetHubForTest(hub)
	resetTUI()
	modeConfig = &ModeConfig{Mode: ModeTUI}
	publish(testMatchEvent())
	waitForCond(t, 2*time.Second, func() bool { return tuiCount() == 1 })
}
