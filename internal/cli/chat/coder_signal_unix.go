//go:build !windows

package chat

import "syscall"

// processAlive: only ESRCH means a pid is gone. EPERM (a process of another user) is a process that exists.
func processAlive(pid int) bool {
	err := syscall.Kill(pid, 0)
	return err == nil || err == syscall.EPERM
}

func terminateProcess(pid int) error { return syscall.Kill(pid, syscall.SIGTERM) }

func killProcess(pid int) error { return syscall.Kill(pid, syscall.SIGKILL) }
