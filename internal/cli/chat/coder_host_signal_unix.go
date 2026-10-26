//go:build !windows

package chat

import (
	"errors"
	"syscall"
)

// A host-started turn is its own session leader (clicore.DetachStart), so its
// pid is its process group: signalling -pid reaches the agents under it too.
// Both refuse a pid that is not the leader of its own group, and pid 1 or
// below, which kill(2) would read as "every process".
func terminateGroup(pid int) error { return signalGroup(pid, syscall.SIGTERM) }

func killGroup(pid int) error { return signalGroup(pid, syscall.SIGKILL) }

func signalGroup(pid int, sig syscall.Signal) error {
	if pid <= 1 {
		return errors.New("not a turn's pid")
	}
	// A group with no live leader (the leader exited, children remain) has no
	// process whose pgid can be asked; the group id alone is what kill takes.
	if pg, err := syscall.Getpgid(pid); err == nil && pg != pid {
		return errors.New("pid is no longer a turn's process group leader")
	}
	return syscall.Kill(-pid, sig)
}
