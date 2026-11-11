//go:build darwin

package stop

import (
	"bufio"
	"os"
	"os/exec"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"testing"
	"time"

	"golang.org/x/sys/unix"

	"github.com/monoes/mono-agent/internal/procbind"
	"github.com/monoes/mono-agent/internal/procchain"
)

// REAL macOS tests of the session unit's OS layer (sessunit_darwin.go). They start real processes (perl, sleeping at most 60
// seconds, every one tracked and ended by pid + start stamp in Cleanup) and touch nothing else: no network, no profile.

var bgOS = darwinSess{}

// tree is a process tree started by a perl script: the leader is a child of the test, started as a session leader.
type tree struct {
	t      *testing.T
	cmd    *exec.Cmd
	pids   map[string]int
	stamps map[int]string
	once   sync.Once
}

const perlKid = `use POSIX ();$|=1;` +
	`sub k(&){my $c=shift;my $p=fork();die "fork" unless defined $p;return $p if $p;$c->();sleep($ENV{S2SLEEP}||60);POSIX::_exit(0)}` +
	`sub say{print "$_[0] $$\n"}`

// startPerl runs the script as a session leader and reads "name pid" lines until "ready" and every wanted name were seen
// (the children report asynchronously); a name that repeats keeps its first pid.
func startPerl(t *testing.T, want []string, script string, args ...string) *tree {
	t.Helper()
	cmd := exec.Command("/usr/bin/perl", append([]string{"-e", perlKid + script}, args...)...)
	cmd.SysProcAttr = &syscall.SysProcAttr{Setsid: true}
	out, err := cmd.StdoutPipe()
	if err != nil {
		t.Fatal(err)
	}
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	tr := &tree{t: t, cmd: cmd, pids: map[string]int{"leader": cmd.Process.Pid}, stamps: map[int]string{}}
	t.Cleanup(tr.end)
	tr.note(cmd.Process.Pid)
	lines := make(chan string, 1000)
	go func() {
		sc := bufio.NewScanner(out)
		for sc.Scan() {
			lines <- sc.Text()
		}
		close(lines)
	}()
	ready := false
	for deadline := time.After(20 * time.Second); ; {
		select {
		case l, ok := <-lines:
			if !ok {
				t.Fatal("the script ended before it was ready")
			}
			f := strings.Fields(l)
			if len(f) != 2 {
				continue
			}
			pid, _ := strconv.Atoi(f[1])
			if f[0] == "ready" {
				ready = true
			} else if _, dup := tr.pids[f[0]]; !dup {
				tr.pids[f[0]] = pid
			}
			tr.note(pid)
			if ready && tr.has(want) {
				return tr
			}
		case <-deadline:
			t.Fatal("the script was not ready in 20 s")
		}
	}
}

func (tr *tree) has(names []string) bool {
	for _, n := range names {
		if _, ok := tr.pids[n]; !ok {
			return false
		}
	}
	return true
}

func (tr *tree) note(pid int) {
	if p, err := bgOS.Info(pid); err == nil {
		tr.stamps[pid] = p.Stamp
	}
}

func (tr *tree) pid(name string) int {
	tr.t.Helper()
	p, ok := tr.pids[name]
	if !ok {
		tr.t.Fatalf("the script reported no %q (have %v)", name, tr.pids)
	}
	return p
}

func (tr *tree) info(name string) SessProc {
	tr.t.Helper()
	p, err := bgOS.Info(tr.pid(name))
	if err != nil {
		tr.t.Fatalf("%s (%d): %v", name, tr.pid(name), err)
	}
	return p
}

// alive says whether the process this tree started under that name is still there (same pid and stamp).
func (tr *tree) alive(name string) bool {
	p, err := bgOS.Info(tr.pid(name))
	return err == nil && p.Stamp == tr.stamps[tr.pid(name)]
}

// end kills everything the tree started, each only while its pid still has the stamp it had, and reaps the leader.
func (tr *tree) end() {
	tr.once.Do(func() {
		for pid, st := range tr.stamps {
			if p, err := bgOS.Info(pid); err == nil && p.Stamp == st {
				_ = bgOS.Signal(pid, syscall.SIGKILL)
			}
		}
		_ = tr.cmd.Process.Kill()
		_ = tr.cmd.Wait()
	})
}

// reapLeader kills the leader alone and waits for it, so the session has no leader any more.
func (tr *tree) reapLeader() {
	_ = bgOS.Signal(tr.pid("leader"), syscall.SIGKILL)
	_ = tr.cmd.Wait()
}

func goneWithin(pid int, stamp string, d time.Duration) bool {
	for end := time.Now().Add(d); time.Now().Before(end); time.Sleep(10 * time.Millisecond) {
		if p, err := bgOS.Info(pid); err != nil || p.Stamp != stamp {
			return true
		}
	}
	return false
}

// realRig is a session unit over the real OS layer with an in-memory ledger.
type realRig struct {
	t   *testing.T
	led *fakeLedger
	c   sessContainment
	rec UnitRecord
	dir RunDir
}

func newRealRig(t *testing.T) *realRig {
	t.Helper()
	dir, err := os.MkdirTemp("", "s2")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.RemoveAll(dir) })
	led := newFakeLedger()
	c := sessContainment{os: bgOS, led: led, hostSteps: hostStepsOK}
	rec, err := c.Create(bg, CreateArgs{Dir: RunDir{Dir: dir}, Journal: &jlog{}}, 7)
	if err != nil {
		t.Fatal(err)
	}
	return &realRig{t: t, led: led, c: c, rec: rec, dir: RunDir{Dir: dir}}
}

// record puts a process in the ledger as a leader (the stamp the real OS gave it).
func (r *realRig) record(p SessProc) {
	r.t.Helper()
	if err := r.led.AddLeader(r.rec.Generation, Leader{PID: p.PID, Stamp: p.Stamp, Unit: r.rec.CgroupID}); err != nil {
		r.t.Fatal(err)
	}
}

func (r *realRig) group() *sessGroup { return &sessGroup{c: r.c, rec: r.rec} }

func (r *realRig) members(g *sessGroup) map[int]bool {
	r.t.Helper()
	v, err := g.members()
	if err != nil {
		r.t.Fatal(err)
	}
	out := map[int]bool{}
	for _, m := range v.members {
		out[m.PID] = true
	}
	return out
}

func fact(t *testing.T, f string, a ...any) { t.Logf("S2FACT "+f, a...) }

// The BSD numbering the session unit relies on, on this machine: a started session leader's session and group are its own
// pid; job control leaves the session; a session of its own is its own pid.
func TestDarwinAllocatorRuleSessionAndGroupNumbers(t *testing.T) {
	v, _ := unix.Sysctl("kern.osproductversion")
	arch, _ := unix.Sysctl("hw.machine")
	fact(t, "macOS %s %s", v, arch)
	for i := 0; i < 20; i++ {
		cmd := exec.Command("/bin/sleep", "30")
		cmd.SysProcAttr = &syscall.SysProcAttr{Setsid: true}
		if err := cmd.Start(); err != nil {
			t.Fatal(err)
		}
		pid := cmd.Process.Pid
		p, err := bgOS.Info(pid)
		_ = cmd.Process.Kill()
		_ = cmd.Wait()
		if err != nil || p.SID != pid || p.PGID != pid || p.PPID != os.Getpid() {
			t.Fatalf("leader %d: %+v %v", pid, p, err)
		}
	}
	tr := startPerl(t, []string{"plain", "pgjob", "own"}, `k{say("plain")};k{setpgrp(0,0);say("pgjob")};k{POSIX::setsid();say("own")};print "ready $$\n";sleep 60;`)
	l := tr.info("leader")
	if l.SID != l.PID || l.PGID != l.PID {
		t.Fatalf("leader %+v", l)
	}
	if p := tr.info("plain"); p.SID != l.PID || p.PGID != l.PID {
		t.Fatalf("a plain child is not in the leader's session and group: %+v", p)
	}
	if p := tr.info("pgjob"); p.SID != l.PID || p.PGID != p.PID {
		t.Fatalf("job control must change the group and keep the session: %+v", p)
	}
	if p := tr.info("own"); p.SID != p.PID || p.PGID != p.PID || p.PPID != l.PID {
		t.Fatalf("setsid must make session and group the child's own pid: %+v", p)
	}
}

func TestDarwinBootIDIsTheBootSessionUUIDNotTheBootTime(t *testing.T) {
	id, err := bgOS.BootID()
	if err != nil {
		t.Fatal(err)
	}
	out, err := exec.Command("/usr/sbin/sysctl", "-n", "kern.bootsessionuuid").Output()
	if err != nil || strings.TrimSpace(string(out)) != id {
		t.Fatalf("BootID %q, sysctl says %q (%v)", id, out, err)
	}
	if !regexp.MustCompile(`^[0-9A-Fa-f]{8}(-[0-9A-Fa-f]{4}){3}-[0-9A-Fa-f]{12}$`).MatchString(id) {
		t.Fatalf("not a uuid: %q", id)
	}
	for i := 0; i < 3; i++ {
		if again, _ := bgOS.BootID(); again != id {
			t.Fatalf("the boot id changed within a boot: %q -> %q", id, again)
		}
	}
	if old, _ := (darwinPG{}).BootID(); old == id {
		t.Fatal("the session unit's boot id is the boot time (it moves with a stepped clock)")
	}
}

// Scan, Info, the unit's old path (darwinPG), the process-chain reader and the host's pin all give one start stamp.
func TestDarwinScanInfoAndPinStampsAreEqualForTheSameProcess(t *testing.T) {
	tr := startPerl(t, []string{"kid"}, `k{say("kid")};print "ready $$\n";sleep 60;`)
	for _, pid := range []int{1, os.Getpid(), tr.pid("leader"), tr.pid("kid")} {
		info, err := bgOS.Info(pid)
		if err != nil {
			t.Fatal(pid, err)
		}
		var scanned *SessProc
		all, err := bgOS.Scan()
		if err != nil {
			t.Fatal(err)
		}
		for i := range all {
			if all[i].PID == pid {
				scanned = &all[i]
			}
		}
		if scanned == nil {
			t.Fatalf("the scan does not list %d", pid)
		}
		_, pg, err := darwinPG{}.stamp(pid)
		if err != nil {
			t.Fatal(err)
		}
		e, ok := procchain.ReadEntry(pid)
		if !ok {
			t.Fatal("procchain cannot read", pid)
		}
		chain := fmtSessStamp(int64(e.Start/1_000_000), int64(e.Start%1_000_000))
		h, err := procbind.NewProcHandle(pid)
		if err != nil {
			t.Fatal(err)
		}
		pinned, err := procbind.DarwinProcs{}.Start(h)
		if err != nil {
			t.Fatal(err)
		}
		pin := fmtSessStamp(int64(pinned/1_000_000), int64(pinned%1_000_000))
		for name, got := range map[string]string{"scan": scanned.Stamp, "pgroup": pg, "procchain": chain, "pin": pin} {
			if got != info.Stamp {
				t.Fatalf("pid %d: %s stamp %q != Info %q", pid, name, got, info.Stamp)
			}
		}
		if _, ok := parseSessStamp(info.Stamp); !ok {
			t.Fatalf("unusable stamp %q", info.Stamp)
		}
	}
	// stable over time and re-reads, and the signaller's own identity is the same one
	self, st, err := bgOS.Self()
	if again, _ := bgOS.Info(self); err != nil || again.Stamp != st {
		t.Fatalf("Self %q vs Info %q (%v)", st, again.Stamp, err)
	}
}

func TestDarwinSelfTestPassesAndLeavesNothingBehind(t *testing.T) {
	for i := 0; i < 10; i++ {
		if err := bgOS.SelfTest(); err != nil {
			t.Fatalf("run %d: %v", i, err)
		}
	}
	if err := (sessContainment{os: bgOS, hostSteps: hostStepsOK}).Probe(); err != nil {
		t.Fatal(err)
	}
	all, _ := bgOS.Scan()
	for _, p := range all {
		if p.PPID == os.Getpid() {
			t.Fatalf("the probe left a child behind: %+v", p)
		}
	}
}

// A unit built on the real OS layer is proven, a started leader is placed and adopted, and the placement refuses what is not a leader.
func TestDarwinPlaceAndAdoptOnTheRealOS(t *testing.T) {
	r := newRealRig(t)
	pl, err := r.c.Place(r.rec)
	if err != nil {
		t.Fatal(err)
	}
	bad := exec.Command("/bin/sleep", "30")
	if _, err := pl.Place(bad); err == nil {
		t.Fatal("a command with no Setsid was placed")
	}
	cmd := exec.Command("/bin/sleep", "30")
	cmd.SysProcAttr = &syscall.SysProcAttr{Setsid: true}
	if _, err := pl.Place(cmd); err != nil {
		t.Fatal(err)
	}
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = cmd.Process.Kill(); _ = cmd.Wait() })
	p, _ := bgOS.Info(cmd.Process.Pid)
	ad := pl.(Adopter)
	if err := ad.Adopt(p.PID, p.Stamp+"1"); err == nil {
		t.Fatal("a leader with a wrong stamp was adopted")
	}
	if err := ad.Adopt(1, "1.000001"); err == nil {
		t.Fatal("launchd was adopted")
	}
	tr := startPerl(t, []string{"kid"}, `k{say("kid")};print "ready $$\n";sleep 60;`)
	kid := tr.info("kid")
	if err := ad.Adopt(kid.PID, kid.Stamp); err == nil {
		t.Fatal("a process that is no child of the adopter, and no session leader, was adopted")
	}
	if err := ad.Adopt(p.PID, p.Stamp); err != nil {
		t.Fatal(err)
	}
	if ls, _ := r.led.Leaders(r.rec.Generation); len(ls) != 1 || ls[0].PID != p.PID {
		t.Fatalf("ledger %+v", ls)
	}
	if !r.members(r.group())[p.PID] {
		t.Fatal("the adopted leader is not a member of its unit")
	}
	// the adopt took the hold: a leader that dies stays a zombie (its number stays taken) until the unit's holds are released
	_ = bgOS.Signal(p.PID, syscall.SIGKILL)
	time.Sleep(200 * time.Millisecond)
	if st, err := kinfo2(p.PID); err != nil || st != szomb {
		t.Fatalf("an adopted leader that died is not held as a zombie: %d %v", st, err)
	}
	ReleaseUnitHolds(r.rec.CgroupID)
	if _, err := kinfo2(p.PID); err == nil {
		t.Fatal("ReleaseUnitHolds did not reap the leader")
	}
}
