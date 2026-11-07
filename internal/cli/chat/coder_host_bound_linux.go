//go:build linux

package chat

import (
	"errors"
	"os"
	"os/exec"
	"syscall"

	"golang.org/x/sys/unix"

	"github.com/monoes/mono-agent/internal/coderhost"
	"github.com/monoes/mono-agent/internal/procbind"
)

// osDelivery is what a Linux turn holds: the pidfd of its leader.
type osDelivery struct{ pidfd int }

// holdWorker is a handle of its own (a dup of the pidfd) for one registry entry.
func (d *boundDelivery) holdWorker() (procbind.Handle, error) {
	dupfd, err := unix.FcntlInt(uintptr(d.pidfd), unix.F_DUPFD_CLOEXEC, 3)
	if err != nil {
		return nil, err
	}
	return procbind.HandleFromPidfd(dupfd), nil
}

// attach prepares cmd for a bound launch: the frame pipe on descriptor 3 and its marker, nothing else.
func (b *boundLaunch) attach(cmd *exec.Cmd, missionToken string) (*boundDelivery, error) {
	r, w, err := os.Pipe() // close-on-exec both ends; the child gets r only through ExtraFiles
	if err != nil {
		return nil, err
	}
	if len(cmd.ExtraFiles) != 0 {
		r.Close()
		w.Close()
		return nil, errors.New("extra files already set")
	}
	cmd.ExtraFiles = []*os.File{r}
	cmd.Env = append(cmd.Env, coderhost.CredFDEnv+"=3")
	return &boundDelivery{b: b, r: r, w: w, osDelivery: osDelivery{pidfd: -1}, missionToken: missionToken}, nil
}

// start starts the process detached with a pidfd; the host's copy of the read end is closed whether it started or not.
//
// pre, when set, runs on cmd after its process attributes are built and before the process exists (a placement hook adds to them,
// which is why it cannot run earlier: the attributes are assigned here). Its error means nothing was started.
func (d *boundDelivery) start(cmd *exec.Cmd, pre func(*exec.Cmd) error) error {
	cmd.SysProcAttr = &syscall.SysProcAttr{Setsid: true, PidFD: &d.pidfd}
	if pre != nil {
		if err := pre(cmd); err != nil {
			d.r.Close()
			return err
		}
		// Only Setsid and the pidfd pointer are checked: the placement is trusted host code, not a boundary, and it is expected to add only
		// UseCgroupFD/CgroupFD. The pidfd is compared by pointer on purpose (the launcher reads the descriptor through it after the start).
		// What the launcher built must have survived: a placement that replaced the struct would start a child that is no session leader
		// or has no pidfd, so it is refused before anything exists.
		if a := cmd.SysProcAttr; a == nil || !a.Setsid || a.PidFD != &d.pidfd {
			d.r.Close()
			return errors.New("the placement replaced the launcher's process attributes, so the turn was not started")
		}
	}
	err := cmd.Start()
	d.r.Close()
	if err != nil {
		return err
	}
	if d.pidfd < 0 { // a kernel that gave no pidfd cannot run a bound turn
		_ = cmd.Process.Kill()
		return errors.New("the kernel gave no pidfd for the turn, so it cannot be bound")
	}
	return nil
}

// kill ends the turn's leader through its own pidfd (never by pid or group).
func (d *boundDelivery) kill() {
	if d.b != nil && d.b.beforeKill != nil {
		d.b.beforeKill()
	}
	if d.onKill != nil {
		d.onKill() // before the signal: the exit it causes must find the turn already marked as being stopped
	}
	if d.pidfd >= 0 {
		_ = unix.PidfdSendSignal(d.pidfd, unix.SIGKILL, nil, 0)
	}
	if d.b != nil && d.b.afterKill != nil {
		d.b.afterKill()
	}
}

// close releases the pipe and the pidfd; safe to call once.
func (d *boundDelivery) close() {
	d.w.Close()
	d.r.Close()
	if d.pidfd >= 0 {
		unix.Close(d.pidfd)
		d.pidfd = -1
	}
}
