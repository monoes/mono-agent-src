//go:build darwin

package stop

import (
	"errors"
	"fmt"
	"os"
	"os/exec"
	"strconv"
	"syscall"

	"golang.org/x/sys/unix"
)

// NOT VERIFIED on a real macOS host: this file compiles (GOOS=darwin go vet) and its logic is tested through the
// PGroupOS fake on Linux; it has not run against the macOS kernel.

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

func (darwinPG) SelfGroup() (int, int, error) {
	sid, err := unix.Getsid(0)
	return unix.Getpgrp(), sid, err
}

// StartAnchor runs a blocking system binary as the leader of a new process group (not a new session: see
// pgroup.go), with no input, no output and no ties to this process.
func (d darwinPG) StartAnchor() (int, string, error) {
	cmd := exec.Command("/bin/sleep", "2147483647")
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	if err := cmd.Start(); err != nil {
		return 0, "", err
	}
	pid := cmd.Process.Pid
	_ = cmd.Process.Release()
	_, stamp, err := d.stamp(pid)
	return pid, stamp, err
}

func (d darwinPG) KillAnchor(pid int, stamp string) error {
	if _, s, err := d.stamp(pid); err != nil || s != stamp {
		return nil // gone, or the pid is another process now: nothing of ours to end
	}
	return ignoreGone(unix.Kill(pid, unix.SIGKILL))
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

// pgroupHostSteps are what the rest of the mode still needs on macOS and does not have yet: each is a named refusal
// so the probe says exactly why the mode does not start. The host lock (flock) and the emergency channel's peer
// credentials (LOCAL_PEERCRED) are implemented; the pinned database binding watches the database file's names with
// inotify, which macOS does not have.
func pgroupHostSteps() []ProbeStep {
	return []ProbeStep{{Name: "pinned database binding", Check: func() error { return ErrPinUnsupported }}}
}

// SystemHost is nil on macOS: there is no service manager; the Containment creates and ends the unit itself.
func SystemHost() UnitHost { return nil }

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
