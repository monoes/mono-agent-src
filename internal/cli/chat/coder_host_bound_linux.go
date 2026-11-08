//go:build linux

package chat

import (
	"errors"
	"os/exec"
	"syscall"

	"golang.org/x/sys/unix"

	"github.com/monoes/mono-agent/internal/procbind"
)

// workerProc is the launch's hold on the turn's process: its pidfd.
type workerProc struct{ pidfd int }

func newWorkerProc() workerProc { return workerProc{pidfd: -1} }

// kill ends the turn's leader through its own pidfd (never by pid or group).
func (p *workerProc) kill() {
	if p.pidfd >= 0 {
		_ = unix.PidfdSendSignal(p.pidfd, unix.SIGKILL, nil, 0)
	}
}

func (p *workerProc) release() {
	if p.pidfd >= 0 {
		unix.Close(p.pidfd)
		p.pidfd = -1
	}
}

// handle is a handle of its own on the turn's process: a dup of the pidfd.
func (p *workerProc) handle() (procbind.Handle, error) {
	dupfd, err := unix.FcntlInt(uintptr(p.pidfd), unix.F_DUPFD_CLOEXEC, 3)
	if err != nil {
		return nil, err
	}
	return procbind.HandleFromPidfd(dupfd), nil
}

// start starts the process detached with a pidfd; the host's copy of the read end is closed whether it started or not.
//
// pre, when set, runs on cmd after its process attributes are built and before the process exists (a placement hook adds to them,
// which is why it cannot run earlier: the attributes are assigned here). Its error means nothing was started.
func (d *boundDelivery) start(cmd *exec.Cmd, pre func(*exec.Cmd) error) error {
	cmd.SysProcAttr = &syscall.SysProcAttr{Setsid: true, PidFD: &d.proc.pidfd}
	if pre != nil {
		if err := pre(cmd); err != nil {
			d.r.Close()
			return err
		}
		// Only Setsid and the pidfd pointer are checked: the placement is trusted host code, not a boundary, and it is expected to add only
		// UseCgroupFD/CgroupFD. The pidfd is compared by pointer on purpose (the launcher reads the descriptor through it after the start).
		// What the launcher built must have survived: a placement that replaced the struct would start a child that is no session leader
		// or has no pidfd, so it is refused before anything exists.
		if a := cmd.SysProcAttr; a == nil || !a.Setsid || a.PidFD != &d.proc.pidfd {
			d.r.Close()
			return errors.New("the placement replaced the launcher's process attributes, so the turn was not started")
		}
	}
	err := cmd.Start()
	d.r.Close()
	if err != nil {
		return err
	}
	if d.proc.pidfd < 0 { // a kernel that gave no pidfd cannot run a bound turn
		_ = cmd.Process.Kill()
		return errors.New("the kernel gave no pidfd for the turn, so it cannot be bound")
	}
	return nil
}
