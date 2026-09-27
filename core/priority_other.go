//go:build !windows && !unix

package core

// ReduceProcessPriority is a no-op on platforms without a supported way to
// lower the process's scheduling priority.
func ReduceProcessPriority() {}
