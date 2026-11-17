//go:build darwin

package chat

import (
	"golang.org/x/sys/unix"
)

// procArgsRaw is the kern.procargs2 buffer of pid: the argument vector with its NULs, which `ps` cannot give without losing
// where one argument ends (a path with spaces).
func procArgsRaw(pid int) ([]byte, error) { return unix.SysctlRaw("kern.procargs2", pid) }
