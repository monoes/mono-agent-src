//go:build !windows

package stop

import (
	"errors"
	"os/exec"
	"syscall"
)

// checkSessCmd refuses a command that would not start as a session leader of its own. The session unit changes nothing
// about a command (it joins nothing): the command must already be born with its own session and without a group of its
// own choosing, because a turn that is not a session leader is a process the unit would never see.
func checkSessCmd(cmd *exec.Cmd) error {
	if cmd == nil || cmd.SysProcAttr == nil || !cmd.SysProcAttr.Setsid {
		return errors.New("the command does not start a session of its own (Setsid is not set)")
	}
	if cmd.SysProcAttr.Setpgid || cmd.SysProcAttr.Pgid != 0 {
		return errors.New("the command is asked to join a process group, which a session leader cannot do")
	}
	return nil
}

// prepareSessCmd makes a command that has no process attributes of its own start as a session leader (a merge's git command is such
// a command: nothing in it chooses a group or a session). A command that already carries attributes is left as it is and checked.
func prepareSessCmd(cmd *exec.Cmd) error {
	if cmd != nil && cmd.SysProcAttr == nil {
		cmd.SysProcAttr = &syscall.SysProcAttr{Setsid: true}
	}
	return checkSessCmd(cmd)
}
