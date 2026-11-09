//go:build windows

package chat

import (
	"errors"
	"fmt"
	"os"
	"os/exec"
	"strconv"
	"syscall"

	"github.com/monoes/mono-agent/internal/coderhost"
	"github.com/monoes/mono-agent/internal/procbind"
)

// workerProc is the launch's hold on the turn's process: a handle of its leader with the right to end it, pinned right after
// the start while the host's own child handle keeps the process object (and so its number) from being given away.
type workerProc struct{ p *procbind.ProcHandle }

func newWorkerProc() workerProc { return workerProc{} }

// kill ends the turn's leader through its own handle (never by number or group).
func (p *workerProc) kill() {
	if p.p != nil {
		_ = p.p.Terminate()
	}
}

func (p *workerProc) release() {
	if p.p != nil {
		_ = p.p.Close()
		p.p = nil
	}
}

// handle is a handle of its own (a duplicate) for one registry entry.
func (p *workerProc) handle() (procbind.Handle, error) {
	if p.p == nil {
		return nil, errors.New("the turn has no process handle")
	}
	return p.p.Dup()
}

// attachPipe makes the read end the only handle the child inherits, and puts its number (not a credential) in CredFDEnv.
// os.Pipe makes both ends inheritable: the write end, which stays with the host, must not be.
func attachPipe(cmd *exec.Cmd, r, w *os.File) error {
	if cmd.SysProcAttr != nil && len(cmd.SysProcAttr.AdditionalInheritedHandles) != 0 {
		return errors.New("inherited handles already set")
	}
	h := syscall.Handle(r.Fd())
	if err := syscall.SetHandleInformation(h, syscall.HANDLE_FLAG_INHERIT, syscall.HANDLE_FLAG_INHERIT); err != nil {
		return err
	}
	if err := syscall.SetHandleInformation(syscall.Handle(w.Fd()), syscall.HANDLE_FLAG_INHERIT, 0); err != nil {
		return err
	}
	if cmd.SysProcAttr == nil {
		cmd.SysProcAttr = &syscall.SysProcAttr{}
	}
	cmd.SysProcAttr.AdditionalInheritedHandles = []syscall.Handle{h}
	cmd.Env = append(cmd.Env, coderhost.CredFDEnv+"="+strconv.FormatUint(uint64(h), 10))
	return nil
}

const (
	detachedProcess       = 0x00000008
	createNewProcessGroup = 0x00000200
	createNoWindow        = 0x08000000
)

// start starts the process detached, like every other turn, and pins its leader with a handle; the host's copy of the
// read end is closed whether it started or not. pre, when set, runs on cmd after the launcher's attributes are built
// and before the process exists (a placement hook adds to them); its error means nothing was started.
func (d *boundDelivery) start(cmd *exec.Cmd, pre func(*exec.Cmd) error) error {
	inherited := cmd.SysProcAttr.AdditionalInheritedHandles
	const flags = detachedProcess | createNewProcessGroup | createNoWindow
	// the worker and its main thread are created with the locked access list: a process of the same user cannot name the worker
	// as its parent or write into it (SECURITY.md, "Binding service on Windows"); the host's own handle keeps full access
	pa, ta, err := procbind.WorkerAttributes()
	if err != nil {
		d.r.Close()
		return fmt.Errorf("the turn's process was not started, its access list could not be built: %w", err)
	}
	cmd.SysProcAttr = &syscall.SysProcAttr{HideWindow: true, CreationFlags: flags, AdditionalInheritedHandles: inherited, ProcessAttributes: pa, ThreadAttributes: ta}
	if pre != nil {
		if err := pre(cmd); err != nil {
			d.r.Close()
			return err
		}
		// what the launcher built must have survived: a placement that replaced the struct would start a child without
		// the frame pipe or detached from the host's console handling, so it is refused before anything exists
		a := cmd.SysProcAttr
		if a == nil || len(a.AdditionalInheritedHandles) != 1 || a.AdditionalInheritedHandles[0] != inherited[0] || a.CreationFlags&flags != flags || a.ProcessAttributes != pa || a.ThreadAttributes != ta {
			d.r.Close()
			return errors.New("the placement replaced the launcher's process attributes, so the turn was not started")
		}
	}
	err = cmd.Start()
	d.r.Close()
	if err != nil {
		return err
	}
	// cmd.Process holds the child's handle until Wait, so the number below cannot name another process
	p, err := procbind.OpenWorkerHandle(cmd.Process.Pid)
	if err != nil {
		_ = cmd.Process.Kill()
		return fmt.Errorf("the turn's process could not be held, so it cannot be bound: %w", err)
	}
	d.proc = workerProc{p: p}
	return nil
}
