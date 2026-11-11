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
			wpid, err := unix.Wait4(pid, &ws, unix.WNOHANG, nil)
			for errors.Is(err, unix.EINTR) {
				wpid, err = unix.Wait4(pid, &ws, unix.WNOHANG, nil)
			}
			if err == nil && wpid == 0 {
				go func() {
					for {
						if _, err := unix.Wait4(pid, &ws, 0, nil); !errors.Is(err, unix.EINTR) {
							return
						}
					}
				}()
			}
		})
	}, nil
}

func (darwinSess) Self() (int, string, error) {
	p, err := darwinSess{}.Info(os.Getpid())
	return os.Getpid(), p.Stamp, err
}

func (darwinSess) CurrentUID() uint32 { return uint32(os.Getuid()) }

// The session comes from getsid(2) and everything else from the process table: two reads that are not one atomic read. The
// session is what makes them consistent: it is read before and after the table entry and the read is accepted only when the
// two agree. A process changes its session at most once in its life (setsid needs a process that leads no group, and makes
// it the leader of one), so a retry always ends; pgid and ppid are data, not the key, because a member can change its group as
// often as it likes (job control inside its session) and must not be able to starve the read this way.
const sessReadTries = 8

// kinfoSettled is kinfo for a process that may be exiting: the kernel answers EIO for the table entry of a process that is
// past its last instruction in user space but not yet a zombie (measured on macOS 27.2 while members were being killed, and it
// can last a few milliseconds). Such a process is shortly gone and cannot run or fork any more, so after a brief wait it reads
// as gone, not as a failure of the whole read.
func kinfoSettled(pid int) (*unix.KinfoProc, error) {
	for try := 0; ; try++ {
		k, err := kinfo(pid)
		if err == nil || !errors.Is(err, unix.EIO) {
			return k, err
		}
		if try >= 20 {
			return nil, errGone
		}
		time.Sleep(time.Millisecond)
	}
}

// sidOf is getsid for one process: ok false when it exited, -1 for a process of another user whose session cannot be read
// (never a member by session, still a candidate child); any other failure for a process of this user is an error.
func sidOf(pid int, uid uint32) (sid int, ok bool, err error) {
	sid, err = unix.Getsid(pid)
	switch {
	case err == nil:
		return sid, true, nil
	case errors.Is(err, unix.ESRCH):
		return 0, false, nil
	case uid != uint32(os.Getuid()):
		return -1, true, nil
	}
	return 0, false, fmt.Errorf("the session of process %d cannot be read: %w", pid, err)
}

func (darwinSess) Scan() ([]SessProc, error) {
	ks, err := unix.SysctlKinfoProcSlice("kern.proc.all")
	if err != nil {
		return nil, err
	}
	out := make([]SessProc, 0, len(ks))
	for i := range ks {
		pid := int(ks[i].Proc.P_pid)
		if pid <= 0 || ks[i].Proc.P_stat == szomb {
			continue
		}
		p, err := (darwinSess{}).Info(pid) // each process is read the consistent way, the table only names them
		switch {
		case err == nil:
			out = append(out, p)
		case errors.Is(err, errGone):
		default:
			return nil, err
		}
	}
	if len(out) == 0 {
		return nil, errors.New("the kernel listed no process")
	}
	return out, nil
}

func (darwinSess) Info(pid int) (SessProc, error) {
	for try := 0; try < sessReadTries; try++ {
		k, err := kinfoSettled(pid)
		if err != nil {
			return SessProc{}, fmt.Errorf("process %d (table read): %w", pid, err)
		}
		uid := k.Eproc.Ucred.Uid
		sid1, ok, err := sidOf(pid, uid)
		if err != nil {
			return SessProc{}, err
		}
		if !ok {
			return SessProc{}, errGone
		}
		k, err = kinfoSettled(pid) // the entry is read between the two session reads
		if err != nil {
			return SessProc{}, fmt.Errorf("process %d (second table read): %w", pid, err)
		}
		sid2, ok, err := sidOf(pid, uid)
		if err != nil {
			return SessProc{}, err
		}
		if !ok {
			return SessProc{}, errGone
		}
		if sid1 != sid2 {
			continue // the process made its session in between (once in its life): read again
		}
		return SessProc{
			PID: pid, PPID: int(k.Eproc.Ppid), PGID: int(k.Eproc.Pgid), SID: sid1,
			Stamp: stampOf(k), UID: k.Eproc.Ucred.Uid, Comm: commOf(k),
		}, nil
	}
	return SessProc{}, errors.New("the session of the process kept changing while it was read")
}

// StampOf is the plain start stamp of a live process from the table alone: the identity a signal falls back to when Info
// cannot give a consistent read.
func (darwinSess) StampOf(pid int) (string, error) {
	k, err := kinfoSettled(pid)
	if err != nil {
		return "", err
	}
	return stampOf(k), nil
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
	cmd.Env = []string{"PATH=/usr/bin:/bin"} // nothing of the caller's environment (PERL5OPT, PERL5LIB) reaches the script
	out, err := cmd.StdoutPipe()
	if err != nil {
		return err
	}
	if err := cmd.Start(); err != nil {
		return fmt.Errorf("the self-test leader cannot be started: %w", err)
	}
	leaderPID := cmd.Process.Pid
	var seen []SessProc // every process identity whose end is checked
	reaped := false     // once the leader is waited for its number is free to be reused: nothing is swept by it
	defer func() {
		// whatever the verdict, nothing the probe made is left: its own session and children are swept first (a failure
		// before the children were recorded must not leave them sleeping), stamps verified before each signal
		if all, e := d.Scan(); e == nil && !reaped {
			for _, p := range all {
				if p.PID != leaderPID && (p.SID == leaderPID || p.PPID == leaderPID) {
					_ = d.Signal(p.PID, syscall.SIGKILL)
				}
			}
		}
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
	for _, p := range []SessProc{plain, grp, own} {
		// the pids came from the child's own output: only a child of the leader is ever ended on that word
		if p.PPID != leaderPID {
			return fmt.Errorf("process %d, named by the self-test leader, is not its child", p.PID)
		}
	}
	seen = append(seen, plain, grp, own)
	if err := sessProbeShape(self, leader, plain, grp, own); err != nil {
		return err
	}
	// the layout of the process table entry is read by offset: a wrong uid or start time (another architecture's struct) shows here
	if leader.UID != uint32(os.Getuid()) {
		return fmt.Errorf("the process table reports uid %d for a process of uid %d: its entry layout is not what this build reads", leader.UID, os.Getuid())
	}
	if us, ok := parseSessStamp(leader.Stamp); !ok || absInt64(time.Now().UnixMicro()-us) > 60_000_000 {
		return fmt.Errorf("the process table reports start time %q for a process started just now: its entry layout is not what this build reads", leader.Stamp)
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
	reaped = true
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

func absInt64(n int64) int64 {
	if n < 0 {
		return -n
	}
	return n
}
