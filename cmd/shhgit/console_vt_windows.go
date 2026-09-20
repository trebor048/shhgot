//go:build windows

package main

import (
	"os"
	"syscall"
	"unsafe"
)

// Windows consoles do not interpret ANSI escape sequences unless virtual
// terminal processing is switched on for the handle, so the TUI asks for it
// before it writes its first frame.

var (
	tuiVtKernel32       = syscall.NewLazyDLL("kernel32.dll")
	tuiVtGetConsoleMode = tuiVtKernel32.NewProc("GetConsoleMode")
	tuiVtSetConsoleMode = tuiVtKernel32.NewProc("SetConsoleMode")
)

const (
	// Output mode flags (see the SetConsoleMode documentation).
	tuiVtEnableProcessedOutput = 0x0001
	tuiVtEnableWrapAtEOL       = 0x0002
	tuiVtEnableVTProcessing    = 0x0004
	tuiVtDisableNewlineAutoRet = 0x0008
	// Input mode flag: makes the console report arrow keys and friends as the
	// ANSI escape sequences the TUI's key decoder already understands.
	tuiVtEnableVTInput = 0x0200

	// NOTE: DISABLE_NEWLINE_AUTO_RETURN (0x0008) must NOT be set here.
	// When set, "\n" moves down without returning to column 0, so every
	// subsequent line starts where the previous one ended (staircase
	// effect). The plain terminal scanner prints with "\n" only, while the
	// TUI writes explicit "\r\n" (which renders correctly either way), so
	// leaving the bit clear is correct for both.
	tuiVtOutFlags = tuiVtEnableProcessedOutput | tuiVtEnableWrapAtEOL |
		tuiVtEnableVTProcessing
)

// tuiVtConsoleMode reads the current mode of a handle.
func tuiVtConsoleMode(f *os.File) (uint32, bool) {
	if f == nil {
		return 0, false
	}
	var mode uint32
	r, _, _ := tuiVtGetConsoleMode.Call(uintptr(syscall.Handle(f.Fd())), uintptr(unsafe.Pointer(&mode)))
	if r == 0 {
		return 0, false
	}
	return mode, true
}

// tuiVtSetMode applies extra mode bits to a handle. It also clears
// DISABLE_NEWLINE_AUTO_RETURN so "\n" keeps its carriage return (without
// that, every line starts where the previous one ended - the staircase
// effect seen in plain terminal output).
func tuiVtSetMode(f *os.File, bits uint32) bool {
	mode, ok := tuiVtConsoleMode(f)
	if !ok {
		return false
	}
	mode &^= tuiVtDisableNewlineAutoRet
	r, _, _ := tuiVtSetConsoleMode.Call(uintptr(syscall.Handle(f.Fd())), uintptr(mode|bits))
	return r != 0
}

// repairNewlineMode clears DISABLE_NEWLINE_AUTO_RETURN on the console screen
// buffer if it is set. The bit is a property of the console, not of the
// process, so it survives the run that set it: while it is set, "\n" advances
// the cursor without returning it to column 0, and every subsequent line starts
// where the previous one ended. Clearing it at startup keeps that from leaking
// into this run (and into the shell prompt after it). Best effort - a redirected
// stream has no console mode to change.
func repairNewlineMode() {
	for _, f := range []*os.File{os.Stdout, os.Stderr} {
		mode, ok := tuiVtConsoleMode(f)
		if !ok || mode&tuiVtDisableNewlineAutoRet == 0 {
			continue
		}
		tuiVtSetConsoleMode.Call(uintptr(syscall.Handle(f.Fd())), uintptr(mode&^tuiVtDisableNewlineAutoRet))
	}
}

// enableVT turns on virtual terminal processing for the console the TUI writes
// to. It reports false only when stdout really is a classic Windows console and
// the mode change was refused - in that case the caller must not print escape
// sequences, because they would show up as garbage on screen.
//
// Handles that are not consoles (a redirected stream, or a third-party terminal
// such as mintty or a ConPTY pipe) pass ANSI through unchanged, so there is
// nothing to enable and the UI is allowed to proceed.
func enableVT() bool {
	out := tuiStdout
	if out == nil {
		out = os.Stdout
	}
	if _, isConsole := tuiVtConsoleMode(out); !isConsole {
		return true
	}
	if !tuiVtSetMode(out, tuiVtOutFlags) {
		return false
	}
	// Secondary streams are best effort: the UI only draws on stdout.
	tuiVtSetMode(os.Stderr, tuiVtOutFlags)
	tuiVtSetMode(tuiStdin, tuiVtEnableVTInput)
	return true
}
