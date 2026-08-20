package main

import (
	"fmt"
	"strings"
	"sync"
	"time"
)

// TUIMatch wraps a match for display
type TUIMatch struct {
	ID        string
	Timestamp time.Time
	Source    string
	URL       string
	File      string
	Signature string
	Matches   []string
	Stars     int
	Priority  int
}

// TUIState holds TUI state
type TUIState struct {
	matches  []*TUIMatch
	critical int
	high     int
	medium   int
	low      int
	mu       sync.RWMutex
}

var tuiState *TUIState

// StartTUI launches the terminal UI (simple text-based streaming)
func StartTUI() error {
	tuiState = &TUIState{
		matches: make([]*TUIMatch, 0),
	}

	// Print header
	fmt.Println("\n╔════════════════════════════════════════════════════════════════╗")
	fmt.Println("║         🔍 shhgit Terminal UI - Real-time Secret Scanner     ║")
	fmt.Println("╚════════════════════════════════════════════════════════════════╝\n")

	fmt.Println("📊 LIVE MATCH FEED")
	fmt.Println("━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━\n")

	fmt.Println("Time        │ Priority │ Source   │ Repository              │ Signature")
	fmt.Println("────────────┼──────────┼──────────┼─────────────────────────┼──────────────────────────")

	// Start display update loop
	go displayStatsLoop()

	// Keep running - scanner will call AddTUIMatch with real matches
	select {}
}

// AddTUIMatch adds a new match to the TUI
func AddTUIMatch(match *TUIMatch) {
	tuiState.mu.Lock()
	defer tuiState.mu.Unlock()

	// Update stats
	switch match.Priority {
	case 3:
		tuiState.critical++
	case 2:
		tuiState.high++
	case 1:
		tuiState.medium++
	default:
		tuiState.low++
	}

	// Add to history (keep last 1000)
	tuiState.matches = append([]*TUIMatch{match}, tuiState.matches...)
	if len(tuiState.matches) > 1000 {
		tuiState.matches = tuiState.matches[:1000]
	}

	// Display the match
	displayMatch(match)
}

// displayMatch displays a single match in real-time
func displayMatch(m *TUIMatch) {
	time := m.Timestamp.Format("15:04:05")
	priority := getPriorityDisplay(m.Priority)
	source := padRight(m.Source, 8)
	repo := strings.Split(m.URL, "/")
	repoName := "unknown"
	if len(repo) > 0 {
		repoName = strings.TrimSuffix(repo[len(repo)-1], ".git")
	}
	repoName = padRight(truncate(repoName, 23), 23)
	sig := truncate(m.Signature, 26)

	fmt.Printf("%s │ %s │ %s │ %s │ %s\n", time, priority, source, repoName, sig)
}

// displayStatsLoop periodically shows statistics
func displayStatsLoop() {
	ticker := time.NewTicker(30 * time.Second)
	defer ticker.Stop()

	for range ticker.C {
		showStats()
	}
}

// showStats displays current statistics
func showStats() {
	tuiState.mu.RLock()
	critical := tuiState.critical
	high := tuiState.high
	medium := tuiState.medium
	low := tuiState.low
	total := len(tuiState.matches)
	tuiState.mu.RUnlock()

	fmt.Println("\n" + strings.Repeat("─", 88))
	fmt.Printf("📈 STATISTICS  │  Total: %d  │  🔴 Critical: %d  │  🟠 High: %d  │  🟡 Medium: %d  │  🟢 Low: %d\n", total, critical, high, medium, low)
	fmt.Println(strings.Repeat("─", 88) + "\n")
}

// getPriorityDisplay returns formatted priority display
func getPriorityDisplay(priority int) string {
	switch priority {
	case 3:
		return "🔴 Critical"
	case 2:
		return "🟠 High   "
	case 1:
		return "🟡 Medium "
	default:
		return "🟢 Low    "
	}
}

// truncate truncates string to length
func truncate(s string, length int) string {
	if len(s) > length {
		return s[:length-3] + "..."
	}
	return s
}

// padRight pads string with spaces on the right
func padRight(s string, length int) string {
	if len(s) >= length {
		return s[:length]
	}
	return s + strings.Repeat(" ", length-len(s))
}
