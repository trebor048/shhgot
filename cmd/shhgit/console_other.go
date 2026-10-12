//go:build !windows

package main

import (
	"errors"
	"os/exec"
	"runtime"
)

// restoreConsole is a no-op on non-Windows platforms.
func restoreConsole() {}

// openBrowser opens the default browser at url (non-Windows). It reports a
// launch failure instead of swallowing it, and reaps the child so a browser that
// exits while the scanner runs does not leave a zombie.
func openBrowser(url string) error {
	var cmd *exec.Cmd
	switch runtime.GOOS {
	case "darwin":
		cmd = exec.Command("open", url)
	case "linux":
		cmd = exec.Command("xdg-open", url)
	default:
		return errors.New("opening a browser is not supported on " + runtime.GOOS)
	}
	if err := cmd.Start(); err != nil {
		return err
	}
	go func() { _ = cmd.Wait() }()
	return nil
}
