//go:build !windows

package main

// enableVT is a no-op everywhere except Windows, where ANSI escape sequences
// have to be switched on for the console handle before they are interpreted.
//
// Unix terminals always understand the sequences the TUI emits, and the caller
// has already established that it is talking to a terminal.
func enableVT() bool { return true }

// repairNewlineMode is a no-op outside Windows: a newline always returns the
// cursor to column 0 there.
func repairNewlineMode() {}
