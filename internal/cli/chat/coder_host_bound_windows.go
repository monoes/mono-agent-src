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

// osDelivery is what a Windows turn holds: a handle of its leader with the right to end it, pinned right after the
// start while the host's own child handle keeps the process object (and so its number) from being given away.
type osDelivery struct{ proc *procbind.ProcHandle }

// holdWorker is a handle of its own (a duplicate) for one registry entry.
func (d *boundDelivery) holdWorker() (procbind.Handle, error) {
	if d.proc == nil {
		return nil, errors.New("the turn has no process handle")
	}
	return d.proc.Dup()
}

// attach prepares cmd for a bound launch: the frame pipe's read end is the only handle the child inherits, and its
// number (not a credential) goes to the child in CredFDEnv. The write end stays with the host and is not inheritable.
func (b *boundLaunch) attach(cmd *exec.Cmd, missionToken string) (*boundDelivery, error) {
	if cmd.SysProcAttr != nil && len(cmd.SysProcAttr.AdditionalInheritedHandles) != 0 {
		return nil, errors.New("inherited handles already set")
	}
	r, w, err := os.Pipe()
	if err != nil {
		return nil, err
	}
	h := syscall.Handle(r.Fd())
	if err := syscall.SetHandleInformation(h, syscall.HANDLE_FLAG_INHERIT, syscall.HANDLE_FLAG_INHERIT); err != nil {
		r.Close()
		w.Close()
		return nil, err
	}
	if cmd.SysProcAttr == nil {
		cmd.SysProcAttr = &syscall.SysProcAttr{}
	}
	cmd.SysProcAttr.AdditionalInheritedHandles = []syscall.Handle{h}
	cmd.Env = append(cmd.Env, coderhost.CredFDEnv+"="+strconv.FormatUint(uint64(h), 10))
	return &boundDelivery{b: b, r: r, w: w, missionToken: missionToken}, nil
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
	cmd.SysProcAttr = &syscall.SysProcAttr{HideWindow: true, CreationFlags: flags, AdditionalInheritedHandles: inherited}
	if pre != nil {
		if err := pre(cmd); err != nil {
			d.r.Close()
			return err
		}
		// what the launcher built must have survived: a placement that replaced the struct would start a child without
		// the frame pipe or detached from the host's console handling, so it is refused before anything exists
		a := cmd.SysProcAttr
		if a == nil || len(a.AdditionalInheritedHandles) != 1 || a.AdditionalInheritedHandles[0] != inherited[0] || a.CreationFlags&flags != flags {
			d.r.Close()
			return errors.New("the placement replaced the launcher's process attributes, so the turn was not started")
		}
	}
	err := cmd.Start()
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
	if ppid, err := (procbind.WinProcs{}).Parent(p); err != nil || ppid != os.Getpid() {
		_ = p.Close()
		_ = cmd.Process.Kill()
		return errors.New("the turn's process is not this host's child, so it cannot be bound")
	}
	d.proc = p
	return nil
}

// kill ends the turn's leader through its own handle (never by number or group).
func (d *boundDelivery) kill() {
	if d.b != nil && d.b.beforeKill != nil {
		d.b.beforeKill()
	}
	if d.onKill != nil {
		d.onKill() // before the end: the exit it causes must find the turn already marked as being stopped
	}
	if d.proc != nil {
		_ = d.proc.Terminate()
	}
	if d.b != nil && d.b.afterKill != nil {
		d.b.afterKill()
	}
}

// close releases the pipe and the process handle; safe to call once.
func (d *boundDelivery) close() {
	d.w.Close()
	d.r.Close()
	if d.proc != nil {
		_ = d.proc.Close()
		d.proc = nil
	}
}
