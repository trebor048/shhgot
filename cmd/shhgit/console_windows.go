//go:build windows

package main

import (
	"os/exec"
	"syscall"
)

var (
	kernel32                = syscall.NewLazyDLL("kernel32.dll")
	user32                  = syscall.NewLazyDLL("user32.dll")
	procGetConsoleWindow    = kernel32.NewProc("GetConsoleWindow")
	procShowWindow          = user32.NewProc("ShowWindow")
	procSetForegroundWindow = user32.NewProc("SetForegroundWindow")
)

const swRestore = 9

// restoreConsole brings the console window back when anything outside the app
// (a shortcut or launcher set to start minimized) shrinks it. The app itself
// never minimizes its window - two full searches of the codebase confirm no
// ShowWindow-hide/minimize, FreeConsole or detach path exists - so this is
// purely a countermeasure against external minimizers. Best effort and a
// no-op on an already-visible window: failure is ignored.
func restoreConsole() {
	hwnd, _, _ := procGetConsoleWindow.Call()
	if hwnd == 0 {
		return
	}
	procShowWindow.Call(hwnd, swRestore)
	procSetForegroundWindow.Call(hwnd)
}

// openBrowser opens the default browser at url (Windows). It reports a launch
// failure instead of swallowing it, and reaps the child process.
func openBrowser(url string) error {
	cmd := exec.Command("rundll32", "url.dll,FileProtocolHandler", url)
	if err := cmd.Start(); err != nil {
		return err
	}
	go func() { _ = cmd.Wait() }()
	return nil
}
