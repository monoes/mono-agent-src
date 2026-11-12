//go:build darwin

package stop

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"strconv"
	"syscall"

	"golang.org/x/sys/unix"
)

// The logic is tested through the PGroupOS fake on Linux, and the create, place, prove and end of a unit run against the real
// macOS kernel in pgroup_darwin_e2e_test.go (the creator and the host in two sessions, as in the app).

const szomb = 5 // p_stat of a zombie: gone for our purposes (it holds no code, only a table slot)

var errGone = errors.New("no such process")

func realPGroupOS() PGroupOS { return darwinPG{} }

type darwinPG struct{}

func kinfo(pid int) (*unix.KinfoProc, error) {
	k, err := unix.SysctlKinfoProc("kern.proc.pid", pid)
	if err != nil {
		if errors.Is(err, unix.ENOENT) || errors.Is(err, unix.ESRCH) {
			return nil, errGone
		}
		return nil, err
	}
	if int(k.Proc.P_pid) != pid || k.Proc.P_stat == szomb {
		return nil, errGone
	}
	return k, nil
}

func stampOf(k *unix.KinfoProc) string {
	return strconv.FormatInt(int64(k.Proc.P_starttime.Sec), 10) + "." + fmt.Sprintf("%06d", k.Proc.P_starttime.Usec)
}

func commOf(k *unix.KinfoProc) string {
	b := k.Proc.P_comm[:]
	n := 0
	for n < len(b) && b[n] != 0 {
		n++
	}
	out := make([]byte, n)
	for i := range out {
		out[i] = byte(b[i])
	}
	return string(out)
}

func (darwinPG) BootID() (string, error) {
	tv, err := unix.SysctlTimeval("kern.boottime")
	if err != nil {
		return "", err
	}
	return strconv.FormatInt(int64(tv.Sec), 10), nil
}

func (d darwinPG) Self() (int, string, error) {
	_, stamp, err := d.stamp(os.Getpid())
	return os.Getpid(), stamp, err
}

func (darwinPG) stamp(pid int) (string, string, error) {
	k, err := kinfo(pid)
	if err != nil {
		return "", "", err
	}
	return commOf(k), stampOf(k), nil
}

// SessionOf is the session of a process of the caller's own session (getsid is refused for any other, which is the same
// wall the placement meets): the host reads its anchor's.
func (darwinPG) SessionOf(pid int) (int, error) { return unix.Getsid(pid) }

func (darwinPG) SelfGroup() (int, int, error) {
	sid, err := unix.Getsid(0)
	return unix.Getpgrp(), sid, err
}

// StartAnchor runs a blocking system binary as the leader of a new process group (not a new session: see
// pgroup.go), with no input, no output and no ties to this process. It runs under argv0 (the unit's name), which the
// proof reads back. The start time is read BEFORE the child is released: while it is unreaped its pid cannot be reused.
// When the start time cannot be read the anchor is ended here and the pid is still returned, so a caller that also
// ends it (KillAnchor with an empty stamp) finds nothing left.
func (d darwinPG) StartAnchor(argv0 string) (int, string, error) {
	cmd := exec.Command("/bin/sleep", "2147483647")
	cmd.Args[0] = argv0
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	if err := cmd.Start(); err != nil {
		return 0, "", err
	}
	pid := cmd.Process.Pid
	_, stamp, err := d.stamp(pid)
	if err != nil || stamp == "" {
		_ = cmd.Process.Kill()
		_, _ = cmd.Process.Wait()
		if err == nil {
			err = errors.New("the anchor's start time is empty")
		}
		return pid, "", err
	}
	// reaped in the background once it ends, so a killed anchor does not stay a zombie of a long-lived host
	go func() { _, _ = cmd.Process.Wait() }()
	return pid, stamp, nil
}

// KillAnchor ends the anchor with that start stamp. An empty stamp names the child this process has just started and
// not reaped: it is accepted only for a process whose parent is this process and that leads a group of its own.
func (d darwinPG) KillAnchor(pid int, stamp string) error {
	if pid <= 1 {
		return fmt.Errorf("refusing to kill process %d", pid)
	}
	k, err := kinfo(pid)
	if err != nil {
		return nil // gone: nothing of ours to end
	}
	if stamp == "" {
		if int(k.Eproc.Ppid) != os.Getpid() || int(k.Eproc.Pgid) != pid {
			return nil // not the child we started
		}
	} else if stampOf(k) != stamp {
		return nil // the pid is another process now
	}
	return ignoreGone(unix.Kill(pid, unix.SIGKILL))
}

// Argv0 reads the name the process was started under from kern.procargs2.
func (darwinPG) Argv0(pid int) (string, error) {
	if _, err := kinfo(pid); err != nil {
		return "", err
	}
	b, err := unix.SysctlRaw("kern.procargs2", pid)
	if err != nil {
		return "", err
	}
	return parseProcArgs2(b)
}

func (d darwinPG) ProcInfo(pid int) (string, string, error) {
	comm, stamp, err := d.stamp(pid)
	return stamp, comm, err
}

func (darwinPG) PgidOf(pid int) (int, error) {
	k, err := kinfo(pid)
	if err != nil {
		return 0, err
	}
	return int(k.Eproc.Pgid), nil
}

func (darwinPG) Members(pgid int) ([]int, error) {
	ks, err := unix.SysctlKinfoProcSlice("kern.proc.pgrp", pgid)
	if err != nil {
		return nil, err
	}
	var out []int
	for i := range ks {
		if ks[i].Proc.P_stat != szomb && int(ks[i].Eproc.Pgid) == pgid {
			out = append(out, int(ks[i].Proc.P_pid))
		}
	}
	return out, nil
}

func ignoreGone(err error) error {
	if errors.Is(err, unix.ESRCH) {
		return nil
	}
	return err
}

func (darwinPG) SignalGroup(pgid int, sig syscall.Signal) error {
	if pgid <= 1 {
		return fmt.Errorf("refusing to signal group %d", pgid)
	}
	return ignoreGone(unix.Kill(-pgid, sig))
}

func (darwinPG) SignalPid(pid int, sig syscall.Signal) error {
	if pid <= 1 {
		return fmt.Errorf("refusing to signal process %d", pid)
	}
	return ignoreGone(unix.Kill(pid, sig))
}

// Prepare makes the child join the anchor's group at fork (setpgid before exec).
func (darwinPG) Prepare(cmd *exec.Cmd, pgid int) error {
	if cmd.SysProcAttr == nil {
		cmd.SysProcAttr = &syscall.SysProcAttr{}
	}
	cmd.SysProcAttr.Setpgid = true
	cmd.SysProcAttr.Pgid = pgid
	return nil
}

// SystemHost is the macOS UnitHost: there is no service manager; the Containment creates the unit itself. Its only job is
// the convenience stop (end the group of the unit of that name), never the proof that a unit is gone: UnitCreator.Discard
// uses it when the store refused the entry after the unit was made. ABSENT (known, not done): the finish verification
// sandbox (finishunit.go) needs StartUnit and refuses on macOS, so a run there cannot be finished.
func SystemHost() UnitHost { return darwinHost{} }

type darwinHost struct{}

func (darwinHost) StartUnit(context.Context, string, string) error {
	return errors.New("stop: a macOS unit is created by its Containment, not by a service manager")
}

// StopUnit ends the group led by the anchor of this operator's that was started under the unit's name. A unit with no
// such anchor is gone already.
func (d darwinHost) StopUnit(_ context.Context, name string) error {
	if _, _, ok := UnitGeneration(name); !ok {
		return fmt.Errorf("stop: %q is not a run unit's name", name)
	}
	ks, err := unix.SysctlKinfoProcSlice("kern.proc.uid", os.Getuid())
	if err != nil {
		return err
	}
	for i := range ks {
		pid := int(ks[i].Proc.P_pid)
		if ks[i].Proc.P_stat == szomb || int(ks[i].Eproc.Pgid) != pid {
			continue
		}
		if a0, err := (darwinPG{}).Argv0(pid); err == nil && a0 == name {
			if err := ignoreGone(unix.Kill(-pid, unix.SIGKILL)); err != nil {
				return err
			}
		}
	}
	return nil
}

// DarwinKernel is the Kernel of a macOS host. Only the boot id answers; every cgroup operation refuses, so a
// cgroup record is never proven here.
type DarwinKernel struct{}

func (DarwinKernel) BootID() (string, error)        { return darwinPG{}.BootID() }
func (DarwinKernel) CurrentUID() uint32             { return uint32(os.Getuid()) }
func (DarwinKernel) DelegatedRoot() (string, error) { return "", errors.New("no cgroups on macOS") }
func (DarwinKernel) SelfCgroup() (string, error)    { return "", errors.New("no cgroups on macOS") }
func (DarwinKernel) OpenCgroup(string) (Cgroup, error) {
	return nil, errors.New("no cgroups on macOS")
}
func (DarwinKernel) IsAbsent(error) bool                       { return false }
func (DarwinKernel) ManagerControlGroup(string) (string, bool) { return "", false }
func (DarwinKernel) ListUnitNames(string) ([]string, error)    { return nil, nil }

// ServeAnchors starts the anchor service in this process, the coder host: the run's process group is made in its session.
func ServeAnchors(ctx context.Context, dir RunDir) error {
	return (&AnchorServer{Dir: dir, UID: uint32(os.Getuid()), OS: darwinPG{}}).Start(ctx)
}

// UnitNeedsHost says the run's unit is made by the coder host (so the host must run before the run starts).
func UnitNeedsHost() bool { return true }
