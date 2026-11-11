//go:build darwin

package stop

import (
	"bufio"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"

	"golang.org/x/sys/unix"
)

// darwinSess is the macOS binding of the session unit (SessionOS, sessunit.go): the process table through sysctl
// (kern.proc.all and kern.proc.pid, unprivileged, no cgo), the session of a process through getsid(2), and signals through
// kill(2). Every start stamp is the one contain_darwin.go reads (kinfo/stampOf), the same time the host pins a turn by
// (procchain.EntryOf), written "<seconds>.<microseconds>".
//
// Verified on a real Mac (macOS 27.2, Apple silicon) by sessunit_darwin_test.go; an Intel Mac is allowed only when the
// on-machine probe (SelfTest) passes there, and is unverified until someone runs it on one.
type darwinSess struct{}

func (darwinSess) BootID() (string, error) {
	// kern.bootsessionuuid, never kern.boottime: a stepped clock moves boottime, and a boot id that moves with the clock would
	// make resolveSession take a clock step for a reboot (and leave the orphans of a dead leader alone).
	id, err := unix.Sysctl("kern.bootsessionuuid")
	if err != nil {
		return "", err
	}
	if id == "" {
		return "", errors.New("kern.bootsessionuuid is empty")
	}
	return id, nil
}

// Hold keeps a leader's number reserved: the leader must be this process's own unreaped child with that start stamp, and
// from here on only release reaps it, so a leader that dies stays a zombie, which keeps its pid (and with it the session
// number) from being handed to another process. The caller must not wait for the process itself (S3: the host's turn wait
// for a session leader is non-reaping). release reaps a leader that has ended; one still running is reaped by a goroutine
// when it ends, since nothing needs the number after the unit let go of it.
func (d darwinSess) Hold(pid int, stamp string) (func(), error) {
	p, err := d.Info(pid)
	switch {
	case err != nil:
		return nil, fmt.Errorf("the leader %d cannot be held: %w", pid, err)
	case p.Stamp != stamp:
		return nil, fmt.Errorf("process %d is not the leader that was started (start stamp differs)", pid)
	case p.PPID != os.Getpid():
		return nil, fmt.Errorf("process %d is not an unreaped child of this process", pid)
	}
	var once sync.Once
	return func() {
		once.Do(func() {
			var ws unix.WaitStatus
			if wpid, err := unix.Wait4(pid, &ws, unix.WNOHANG, nil); err == nil && wpid == 0 {
				go func() { _, _ = unix.Wait4(pid, &ws, 0, nil) }()
			}
		})
	}, nil
}

func (darwinSess) Self() (int, string, error) {
	p, err := darwinSess{}.Info(os.Getpid())
	return os.Getpid(), p.Stamp, err
}

func (darwinSess) CurrentUID() uint32 { return uint32(os.Getuid()) }

// sessProcOf reads the session of a process listed by the kernel; a session that cannot be read is -1 (such a process
// is still a candidate child of a member, and is never taken for a member of a session).
func sessProcOf(k *unix.KinfoProc) (SessProc, bool, error) {
	pid := int(k.Proc.P_pid)
	if pid <= 0 || k.Proc.P_stat == szomb {
		return SessProc{}, false, nil
	}
	sid, err := unix.Getsid(pid)
	if err != nil {
		if errors.Is(err, unix.ESRCH) {
			return SessProc{}, false, nil // exited since the table was read
		}
		sid = -1
	}
	return SessProc{
		PID: pid, PPID: int(k.Eproc.Ppid), PGID: int(k.Eproc.Pgid), SID: sid,
		Stamp: fmtSessStamp(int64(k.Proc.P_starttime.Sec), int64(k.Proc.P_starttime.Usec)),
		UID:   k.Eproc.Ucred.Uid, Comm: commOf(k),
	}, true, nil
}

// The session comes from getsid(2) and everything else from the process table, two reads that are not one atomic read: a
// process that calls setsid between them has its new session next to its old group (measured on macOS 27.2). A read is
// accepted only when the table says the same before and after the session was asked; otherwise it is read again.
const sessReadTries = 8

func (darwinSess) Scan() ([]SessProc, error) {
	ks, err := unix.SysctlKinfoProcSlice("kern.proc.all")
	if err != nil {
		return nil, err
	}
	out := make([]SessProc, 0, len(ks))
	for i := range ks {
		p, ok, err := sessProcOf(&ks[i])
		if err != nil {
			return nil, err
		}
		if ok {
			out = append(out, p)
		}
	}
	after, err := unix.SysctlKinfoProcSlice("kern.proc.all")
	if err != nil {
		return nil, err
	}
	type shape struct{ ppid, pgid int }
	was := make(map[int]shape, len(after))
	for i := range after {
		was[int(after[i].Proc.P_pid)] = shape{int(after[i].Eproc.Ppid), int(after[i].Eproc.Pgid)}
	}
	kept := out[:0]
	for _, p := range out {
		if s, ok := was[p.PID]; ok && s.ppid == p.PPID && s.pgid == p.PGID {
			kept = append(kept, p)
			continue
		}
		// changed (or gone) while the session was asked: read that one process again, consistently
		if q, err := (darwinSess{}).Info(p.PID); err == nil {
			kept = append(kept, q)
		}
	}
	if len(kept) == 0 {
		return nil, errors.New("the kernel listed no process")
	}
	return kept, nil
}

func (darwinSess) Info(pid int) (SessProc, error) {
	for try := 0; ; try++ {
		k, err := kinfo(pid)
		if err != nil {
			return SessProc{}, err
		}
		p, ok, err := sessProcOf(k)
		if err != nil {
			return SessProc{}, err
		}
		if !ok {
			return SessProc{}, errGone
		}
		k2, err := kinfo(pid)
		if err != nil {
			return SessProc{}, err
		}
		if k2.Eproc.Pgid == k.Eproc.Pgid && k2.Eproc.Ppid == k.Eproc.Ppid && k2.Proc.P_starttime == k.Proc.P_starttime {
			return p, nil
		}
		if try+1 >= sessReadTries {
			return SessProc{}, errors.New("the process keeps changing its group or parent while it is read")
		}
	}
}

func (darwinSess) Signal(pid int, sig syscall.Signal) error {
	if pid <= 1 {
		return fmt.Errorf("refusing to signal process %d", pid)
	}
	return ignoreGone(unix.Kill(pid, sig))
}

func (darwinSess) SignalGroup(pgid int, sig syscall.Signal) error {
	if pgid <= 1 {
		return fmt.Errorf("refusing to signal group %d", pgid)
	}
	return ignoreGone(unix.Kill(-pgid, sig))
}

func (darwinSess) CheckCmd(cmd *exec.Cmd) error { return checkSessCmd(cmd) }

// sessProbeScript makes the shapes the probe judges: a plain child, a child that made a process group of its own (job
// control) and a child that made a session of its own, and prints their pids. Everything sleeps a bounded time, so a probe
// that is itself killed leaves nothing for long. /usr/bin/perl ships with macOS; a Mac without it refuses the mode with
// that reason, since no other tool there can make a session of its own from a script.
const sessProbeScript = `use POSIX ();$|=1;` +
	`sub kid{my $m=shift;my $p=fork();die "fork" unless defined $p;` +
	`if(!$p){if($m eq "g"){setpgrp(0,0)}if($m eq "s"){POSIX::setsid()}sleep 60;POSIX::_exit(0)}$p}` +
	`my($x,$y,$z)=(kid("p"),kid("g"),kid("s"));print "$x $y $z\n";sleep 60;`

// SelfTest is the on-machine probe of the session unit: it starts a session leader with two children and a grandchild-shape
// of each escape class that the unit claims to cover, reads them through every path (Info, Scan, the unit's own membership
// rule), checks the assumptions the unit relies on (a session number is its leader's pid, job control does not leave a
// session, the stamps agree) and ends them, checking they are gone. A failure names the assumption that does not hold on
// this machine, and the mode is then not offered.
func (d darwinSess) SelfTest() (err error) {
	if _, e := os.Stat("/usr/bin/perl"); e != nil {
		return errors.New("/usr/bin/perl is missing: the session self-test cannot make its test processes")
	}
	self, _, err := d.Self()
	if err != nil {
		return fmt.Errorf("this process cannot be read: %w", err)
	}
	cmd := exec.Command("/usr/bin/perl", "-e", sessProbeScript)
	cmd.SysProcAttr = &syscall.SysProcAttr{Setsid: true}
	out, err := cmd.StdoutPipe()
	if err != nil {
		return err
	}
	if err := cmd.Start(); err != nil {
		return fmt.Errorf("the self-test leader cannot be started: %w", err)
	}
	leaderPID := cmd.Process.Pid
	var seen []SessProc // every process identity whose end is checked
	defer func() {
		// whatever the verdict, nothing the probe made is left: stamps are verified before each signal
		for _, p := range seen {
			if cur, e := d.Info(p.PID); e == nil && cur.Stamp == p.Stamp {
				_ = d.Signal(p.PID, syscall.SIGKILL)
			}
		}
		_ = cmd.Process.Kill()
		_ = cmd.Wait()
	}()
	line := make(chan string, 1)
	go func() {
		s, _ := bufio.NewReader(out).ReadString('\n')
		line <- s
	}()
	var pids []int
	select {
	case s := <-line:
		for _, f := range strings.Fields(s) {
			if n, e := strconv.Atoi(f); e == nil && n > 1 {
				pids = append(pids, n)
			}
		}
	case <-time.After(10 * time.Second):
	}
	if len(pids) != 3 {
		return errors.New("the self-test leader did not report its three children")
	}
	leader, err := d.Info(leaderPID)
	if err != nil {
		return fmt.Errorf("the self-test leader cannot be read: %w", err)
	}
	seen = append(seen, leader)
	// the children change their group and session shortly after the fork: wait for the state under test, bounded
	var plain, grp, own SessProc
	deadline := time.Now().Add(10 * time.Second)
	for {
		var e1, e2, e3 error
		plain, e1 = d.Info(pids[0])
		grp, e2 = d.Info(pids[1])
		own, e3 = d.Info(pids[2])
		if e1 == nil && e2 == nil && e3 == nil && grp.PGID == grp.PID && own.SID == own.PID {
			break
		}
		if time.Now().After(deadline) {
			return fmt.Errorf("the self-test children did not reach their group and session (%v %v %v)", e1, e2, e3)
		}
		time.Sleep(10 * time.Millisecond)
	}
	seen = append(seen, plain, grp, own)
	if err := sessProbeShape(self, leader, plain, grp, own); err != nil {
		return err
	}
	scan, err := d.Scan()
	if err != nil {
		return fmt.Errorf("the process scan fails: %w", err)
	}
	if err := sessProbeFinds(scan, leader, leader, plain, grp, own); err != nil {
		return err
	}
	// end them the way a unit's end does: freeze, then kill, each only while the identity read just now still holds
	for _, sig := range []syscall.Signal{syscall.SIGSTOP, syscall.SIGKILL} {
		for _, p := range seen {
			if cur, e := d.Info(p.PID); e == nil && cur.Stamp == p.Stamp {
				if e := d.Signal(p.PID, sig); e != nil {
					return fmt.Errorf("process %d cannot be signalled: %w", p.PID, e)
				}
			}
		}
	}
	_ = cmd.Wait() // reaps the leader (a zombie reads as gone, but the table slot must not stay)
	for _, p := range seen {
		for end := time.Now().Add(5 * time.Second); ; time.Sleep(10 * time.Millisecond) {
			cur, e := d.Info(p.PID)
			if e != nil || cur.Stamp != p.Stamp {
				break
			}
			if time.Now().After(end) {
				return fmt.Errorf("process %d is still there after SIGKILL", p.PID)
			}
		}
	}
	return nil
}
