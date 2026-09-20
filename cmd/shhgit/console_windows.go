//go:build windows

package main

import (
	"os/exec"
	"syscall"
)

var (
	kernel32             = syscall.NewLazyDLL("kernel32.dll")
	user32               = syscall.NewLazyDLL("user32.dll")
	procGetConsoleWindow = kernel32.NewProc("GetConsoleWindow")
	procShowWindow       = user32.NewProc("ShowWindow")
)

const swHide = 0

// hideConsole hides the console window (used in --web mode so the dashboard
// runs headless while the browser shows the UI).
func hideConsole() {
	if hwnd, _, _ := procGetConsoleWindow.Call(); hwnd != 0 {
		procShowWindow.Call(hwnd, swHide)
	}
}

// openBrowser opens the default browser at url (Windows).
func openBrowser(url string) {
	exec.Command("rundll32", "url.dll,FileProtocolHandler", url).Start()
}
