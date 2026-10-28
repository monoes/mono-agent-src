//go:build windows

package chat

import (
	"fmt"
	"syscall"
)

type signalNumber = syscall.Signal

// pinProcessOS: there is no way to tell a process from a later one that got its pid here, so no
// recorded process is stopped (monomind reports none on Windows).
func pinProcessOS(int) (pinnedProcess, error) {
	return nil, fmt.Errorf("%w: not supported on Windows", errCannotPin)
}

// groupMembers: no way to list a group here.
func groupMembers(int) []int { return nil }
