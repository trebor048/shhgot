//go:build windows

package core

import "syscall"

var (
	kernel32             = syscall.NewLazyDLL("kernel32.dll")
	procSetPriorityClass = kernel32.NewProc("SetPriorityClass")
)

const (
	// belowNormalPriorityClass is BELOW_NORMAL_PRIORITY_CLASS: below normal,
	// above idle, so the scheduler favours interactive work over the scan.
	belowNormalPriorityClass = 0x00004000
	// getCurrentProcess is the pseudo-handle (-1) kernel32 accepts in place of a
	// real process handle.
	getCurrentProcess = ^uintptr(0)
)

// ReduceProcessPriority drops the process to below-normal CPU priority so a
// scan does not make the machine unusable. Best effort: failure is ignored.
func ReduceProcessPriority() {
	_, _, _ = procSetPriorityClass.Call(getCurrentProcess, belowNormalPriorityClass)
}
