//go:build windows

package chat

import (
	"os/exec"
	"syscall"

	"github.com/monoes/mono-agent/internal/procbind"
)

// prepareHostStart gives the host process the locked access list of procbind/procacl.go at creation, so there is no moment
// between its start and the LockSelf of `coder host run` in which a process of the user can open it with the right to write into
// it or take a handle of it (a handle opened then would survive the lock). Only the process list: the host locks itself the same
// way. LockSelf stays as the backstop for a host started by hand.
func prepareHostStart(cmd *exec.Cmd) error {
	proc, _, err := procbind.WorkerAttributes()
	if err != nil {
		return err
	}
	if cmd.SysProcAttr == nil {
		cmd.SysProcAttr = &syscall.SysProcAttr{}
	}
	cmd.SysProcAttr.ProcessAttributes = proc
	return nil
}
