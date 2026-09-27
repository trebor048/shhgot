//go:build unix

package core

import (
	"os"
	"syscall"
)

// ReduceProcessPriority raises the process's nice value so a scan yields to
// interactive work. Best effort: failure is ignored.
func ReduceProcessPriority() {
	_ = syscall.Setpriority(syscall.PRIO_PROCESS, os.Getpid(), 10)
}
