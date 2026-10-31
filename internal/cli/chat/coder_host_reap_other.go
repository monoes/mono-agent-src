//go:build !linux

package chat

// awaitExitNoReap is a no-op where a child cannot be waited for without reaping it.
func awaitExitNoReap(pid int) {}
