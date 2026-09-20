//go:build !windows

package main

import (
	"os/exec"
	"runtime"
)

// hideConsole is a no-op on non-Windows platforms.
func hideConsole() {}

// openBrowser opens the default browser at url (non-Windows).
func openBrowser(url string) {
	var cmd *exec.Cmd
	switch runtime.GOOS {
	case "darwin":
		cmd = exec.Command("open", url)
	case "linux":
		cmd = exec.Command("xdg-open", url)
	default:
		return
	}
	cmd.Start()
}
