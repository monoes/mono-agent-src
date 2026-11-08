//go:build darwin

package chat

import (
	"errors"
	"os/exec"
	"syscall"

	"golang.org/x/sys/unix"

	"github.com/monoes/mono-agent/internal/procbind"
)

// workerProc is the launch's hold on the turn's process. macOS has no pidfd, so the hold is the pid with the start time
// the process had right after it was started, read while the host had not yet waited for it: the child is unreaped then,
// so its number cannot belong to anyone else. Afterwards the hold is alive only while the kernel reports that pair.
type workerProc struct {
	pid    int
	worker procbind.Handle
}

func newWorkerProc() workerProc { return workerProc{} }

// kill ends the turn's leader (never by group): only while the pinned pair still names a live process, so a number the
// kernel has since given to another process is never signalled. The check and the signal are not atomic (a pidfd's are);
// the window is a few instructions, and the number is the host's own child until it is waited for.
func (p *workerProc) kill() {
	if p.worker != nil && p.worker.Alive() {
		_ = unix.Kill(p.pid, unix.SIGKILL)
	}
}

func (p *workerProc) release() {
	if p.worker != nil {
		_ = p.worker.Close()
		p.worker = nil
	}
}

// handle is a handle of its own on the turn's process.
func (p *workerProc) handle() (procbind.Handle, error) {
	if p.worker == nil {
		return nil, errors.New("the turn has no process hold")
	}
	return procbind.CloneProcHandle(p.worker)
}

// start starts the process detached; the host's copy of the read end is closed whether it started or not. The process
// is pinned (pid and start time) before anything else can reap it; one that cannot be pinned is killed and not bound.
//
// pre, when set, runs on cmd after its process attributes are built and before the process exists. Its error means
// nothing was started.
func (d *boundDelivery) start(cmd *exec.Cmd, pre func(*exec.Cmd) error) error {
	cmd.SysProcAttr = &syscall.SysProcAttr{Setsid: true}
	if pre != nil {
		if err := pre(cmd); err != nil {
			d.r.Close()
			return err
		}
		// What the launcher built must have survived: a placement that replaced the struct would start a child that is no
		// session leader, so it is refused before anything exists.
		if a := cmd.SysProcAttr; a == nil || !a.Setsid {
			d.r.Close()
			return errors.New("the placement replaced the launcher's process attributes, so the turn was not started")
		}
	}
	err := cmd.Start()
	d.r.Close()
	if err != nil {
		return err
	}
	h, herr := procbind.NewProcHandle(cmd.Process.Pid)
	if herr != nil { // gone already (or a zombie): it cannot run a bound turn
		_ = cmd.Process.Kill()
		return errors.New("the turn's process could not be pinned, so it cannot be bound")
	}
	d.proc = workerProc{pid: cmd.Process.Pid, worker: h}
	return nil
}
