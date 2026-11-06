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

// SystemHost is nil on macOS: there is no service manager; the Containment creates and ends the unit itself. ABSENT
// (known, not done): with no UnitHost, UnitCreator.Discard (the store refused the entry after the unit was created)
// refuses and leaves the anchor and the record, and the finish verification sandbox (finishunit.go) refuses outright.
// Both need a host whose StopUnit proves and ends the group by name; the mode cannot start on macOS before the pinned
// database binding exists, so neither is reachable yet.
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
