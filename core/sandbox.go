package core

import (
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
)

var DangerousExtensions = []string{
	".exe", ".dll", ".ps1", ".bat", ".cmd", ".vbs", ".scr",
	".com", ".msi", ".jar", ".psm1", ".vbe", ".jse",
	".wsf", ".wsh", ".msc", ".gadget", ".msp", ".mst", ".cpl",
	".appref-ms", ".ps2", ".psc1", ".sct", ".hta",
}

const QuarantineExt = ".quarantine"

// ApplySandboxToRepo strips execute permissions from every file in dir
// (recursively) and renames files with dangerous extensions to
// <name>.ext.quarantine so they can never be launched.
//
// Directory permissions are left untouched on Windows (denying (X) on a
// directory would block traverse for all processes, breaking the scanner).
// On Unix, directories get 0755 (execute stripped for group/other but
// preserved for owner).
func ApplySandboxToRepo(dir string) {
	filepath.Walk(dir, func(path string, info os.FileInfo, err error) error {
		if err != nil || path == dir {
			return nil
		}

		if info.IsDir() {
			DenyDirExecute(path)
			return nil
		}

		ext := strings.ToLower(filepath.Ext(path))
		for _, dangerous := range DangerousExtensions {
			if ext == dangerous {
				newPath := path + QuarantineExt
				if err := os.Rename(path, newPath); err == nil {
					path = newPath
				}
				break
			}
		}

		DenyFileExecute(path)
		return nil
	})
}

// DenyFileExecute strips execute permission from a regular file.
//   - Windows: icacls denies FILE_EXECUTE (X) for Everyone.
//   - Unix:    mode set to 0644.
func DenyFileExecute(path string) {
	if runtime.GOOS != "windows" {
		os.Chmod(path, 0644)
		return
	}
	exec.Command("icacls", path, "/deny", "Everyone:(X)").Run()
}

// DenyDirExecute strips execute from a directory.
//   - Windows: skipped (denying (X) on directories blocks traverse for
//     all processes including the scanner).
//   - Unix:    mode set to 0755 (owner retains access).
func DenyDirExecute(path string) {
	if runtime.GOOS != "windows" {
		os.Chmod(path, 0755)
	}
}
