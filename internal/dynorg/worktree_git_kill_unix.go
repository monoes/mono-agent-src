//go:build !windows

package dynorg

import (
	"os/exec"
	"syscall"
)

// killPlaced ends a command whose adoption failed. A placed command is a session leader (its process group is its pid), so the
// group is signalled and with it whatever it forked; the leader is not waited for yet, so its number is still its own.
func killPlaced(cmd *exec.Cmd) error {
	if cmd.SysProcAttr != nil && cmd.SysProcAttr.Setsid {
		if err := syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL); err != nil {
			_ = cmd.Process.Kill()
			return err
		}
		return nil
	}
	return cmd.Process.Kill()
}
