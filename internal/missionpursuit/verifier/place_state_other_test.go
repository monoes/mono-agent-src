//go:build !linux && !windows

package verifier

import "syscall"

func readState(pid int) (byte, error) { return 0, syscall.Kill(pid, 0) }
