//go:build darwin

package stop

import (
	"os"
	"os/exec"
	"regexp"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"

	"golang.org/x/sys/unix"
)

var zooNames = []string{"plain", "pgjob", "sidchild", "deep", "orphan", "escapee", "noterm"}

const zooScript = `` +
	`k{say("plain")};` +
	`k{setpgrp(0,0);say("pgjob")};` +
	`k{POSIX::setsid();say("sidchild");k{say("deep")}};` +
	`k{my $g=fork();if(!$g){say("orphan");sleep 60;POSIX::_exit(0)}POSIX::_exit(0)};` +
	`k{my $g=fork();if(!$g){POSIX::setsid();say("escapee");sleep 60;POSIX::_exit(0)}POSIX::_exit(0)};` +
	`k{$SIG{TERM}="IGNORE";$SIG{HUP}="IGNORE";say("noterm")};` +
	`print "ready $$\n";sleep 60;`

// The session unit against every shape of process the design names (the old "sidtest" matrix), as assertions: what it finds
// and ends, and what it does not (the best-effort limit stated in SessionBestEffortNote).
func TestDarwinSessionMembershipAndEscapeClasses(t *testing.T) {
	tr := startPerl(t, zooNames, zooScript)
	r := newRealRig(t)
	r.record(tr.info("leader"))
	g := r.group()
	mem := r.members(g)
	for _, name := range []string{"leader", "plain", "pgjob", "sidchild", "deep", "orphan", "noterm"} {
		if !mem[tr.pid(name)] {
			t.Errorf("%s (%d) is not a member of the unit", name, tr.pid(name))
		}
	}
	if mem[tr.pid("escapee")] {
		t.Errorf("the double-forked setsid daemon is found: the documented limit no longer holds, update the best-effort note")
	}
	if o := tr.info("orphan"); o.PPID == tr.pid("leader") || o.SID != tr.pid("leader") {
		t.Logf("orphan: %+v", o)
	}
	if e := tr.info("escapee"); e.SID != e.PID || e.PPID != 1 {
		t.Fatalf("the escapee is not an orphan with a session of its own: %+v", e)
	}
	if err := g.Kill(""); err != nil {
		t.Fatalf("Kill: %v", err)
	}
	for _, name := range []string{"leader", "plain", "pgjob", "sidchild", "deep", "orphan", "noterm"} {
		if !goneWithin(tr.pid(name), tr.stamps[tr.pid(name)], 5*time.Second) {
			t.Errorf("%s (%d) survived Stop", name, tr.pid(name))
		}
	}
	if !tr.alive("escapee") {
		t.Fatal("the escapee did not survive: a test for the limit must show it")
	}
	fact(t, "SURVIVES Stop: setsid + double fork (orphan in a session of its own) pid %d", tr.pid("escapee"))
	fact(t, "ENDED by Stop: plain child, setpgid (job control) child, setsid child of a member, its child, orphan left in the session, member ignoring TERM+HUP")
	_ = tr.cmd.Wait()
}

// The whole stop as the host calls it: UnitEnder over a session unit on the real OS ends the run with the best-effort
// outcome and its limit stated, the escapee is left alone and nothing is reported as a survivor.
func TestDarwinStopReportsBestEffortAndLeavesTheEscapee(t *testing.T) {
	tr := startPerl(t, zooNames, zooScript)
	r := newRealRig(t)
	r.record(tr.info("leader"))
	hostAs(t, "darwin")
	platformKindsAs(t, "darwin", KindSession)
	swapContainment(t, KindSession, r.c)
	u := UnitEnder{K: newFakeKernel(), Dir: r.dir, Witness: func(uint64) []UnitRecord { return []UnitRecord{r.rec} },
		Cfg: EndConfig{Known: func(string, int, string) bool { return true }}}
	out := u.End(bg, r.rec.Generation, nil)
	if out.Kind != UnitBestEffort || len(out.Survivors) != 0 {
		t.Fatalf("a session end is best effort with no survivor here: %+v", out)
	}
	if !strings.Contains(out.Note, "best effort") || !strings.Contains(out.Note, "launchd, open or XPC") {
		t.Fatalf("the limit is not stated: %q", out.Note)
	}
	for _, name := range []string{"leader", "plain", "pgjob", "sidchild", "deep", "orphan", "noterm"} {
		if !goneWithin(tr.pid(name), tr.stamps[tr.pid(name)], 5*time.Second) {
			t.Errorf("%s survived the end", name)
		}
	}
	if !tr.alive("escapee") {
		t.Fatal("the escapee was ended")
	}
	_ = tr.cmd.Wait()
	rep := Report{Generation: r.rec.Generation, Final: StateQuiescent, Unit: out}
	if rep.Stopped() || !rep.StoppedBestEffort() || rep.ExitCode() != 0 {
		t.Fatalf("the report must say best effort, not stopped: stopped=%v best=%v exit=%d", rep.Stopped(), rep.StoppedBestEffort(), rep.ExitCode())
	}
}

// A recycled session number: the ledger names a leader whose pid is now an unrelated live session leader (another start
// stamp). Nothing of that session is signalled; an orphan of a really dead leader is.
func TestDarwinRecycledSessionNumbers(t *testing.T) {
	// 1. the real dead leader: its orphan (own group, same session) is the unit's and is ended
	dead := startPerl(t, []string{"orphan"}, `k{setpgrp(0,0);say("orphan")};print "ready $$\n";sleep 60;`)
	r := newRealRig(t)
	leader := dead.info("leader")
	r.record(leader)
	dead.reapLeader()
	if p, err := bgOS.Info(dead.pid("orphan")); err != nil || p.SID != leader.PID {
		t.Fatalf("the orphan should keep the dead leader's session number: %+v %v", p, err)
	}
	g := r.group()
	if !r.members(g)[dead.pid("orphan")] {
		t.Fatal("the orphan of a dead leader is not found")
	}
	if err := g.Kill(""); err != nil {
		t.Fatal(err)
	}
	if !goneWithin(dead.pid("orphan"), dead.stamps[dead.pid("orphan")], 5*time.Second) {
		t.Fatal("the orphan of the dead leader survived")
	}
	// 2. a stranger holds the recorded number: a live session leader X with a child and a re-parented orphan of its own
	x := startPerl(t, []string{"kid", "orphan"}, `k{say("kid")};k{my $g=fork();if(!$g){say("orphan");sleep 60;POSIX::_exit(0)}POSIX::_exit(0)};print "ready $$\n";sleep 60;`)
	r2 := newRealRig(t)
	xl := x.info("leader")
	stale := xl
	stale.Stamp = fmtSessStamp(1_000_000_000, 1) // the stamp the number had when it was recorded, years before X started
	r2.record(stale)
	g2 := r2.group()
	if m := r2.members(g2); len(m) != 0 {
		t.Fatalf("members of a recycled number: %v", m)
	}
	if err := g2.Kill(""); err != nil {
		t.Fatalf("Kill: %v", err)
	}
	for _, name := range []string{"leader", "kid", "orphan"} {
		if !x.alive(name) {
			t.Fatalf("%s of an unrelated session was signalled through a recycled number", name)
		}
	}
	fact(t, "recycled number: a stranger's session (leader, child, orphan) is left alone")
}

// The pid allocator, measured: while a session outlives its leader, does the kernel hand the leader's number to a new
// process? Two cases: the survivor still in the leader's process group (group pinned) and the survivor that left it by job
// control (only the session holds the number). A bounded fork loop; MONOES_S2_FORKS raises the bound to wrap the pid space
// (opt-in: the wrap needs about 100000 forks, which is a few minutes).
func TestDarwinPidAllocatorAndALiveSessionNumber(t *testing.T) {
	n := 5000
	if s := os.Getenv("MONOES_S2_FORKS"); s != "" {
		n, _ = strconv.Atoi(s)
	}
	for _, c := range []struct {
		name, kid string
		mustNot   bool
		hold      bool
	}{
		{"group-pinned", `k{say("kid")}`, true, false},
		{"session-only", `k{setpgrp(0,0);say("kid")}`, false, false},
		{"session-only-held", `k{setpgrp(0,0);say("kid")}`, true, true},
	} {
		tr := startPerl(t, []string{"kid"}, `$ENV{S2SLEEP}=400;`+c.kid+`;print "ready $$\n";sleep 60;`)
		L := tr.pid("leader")
		kid := tr.info("kid")
		if c.hold {
			release, err := bgOS.Hold(L, tr.stamps[L])
			if err != nil {
				t.Fatal(err)
			}
			_ = bgOS.Signal(L, syscall.SIGKILL) // the leader dies and stays a zombie: nobody reaped it
			time.Sleep(100 * time.Millisecond)
			t.Cleanup(release)
		} else {
			tr.reapLeader()
		}
		if p, err := bgOS.Info(kid.PID); err != nil || p.SID != L {
			t.Fatalf("%s: the survivor lost the session: %+v %v", c.name, p, err)
		}
		cmd := exec.Command("/usr/bin/perl", "-e", `use POSIX ();my($N,$L)=@ARGV;my($prev,$wrapped,$hit,$skipped,$m)=(0,0,0,0,0);my $t0=time;`+
			`for(1..$N){my $p=fork();die "fork" unless defined $p;if(!$p){POSIX::_exit(0)}waitpid($p,0);$m++;`+
			`if($p==$L){$hit=1;last}if($p<$prev){$wrapped=1}if($wrapped&&$prev<$L&&$p>$L){$skipped=1;last}$prev=$p;last if time-$t0>170}`+
			`print "forks=$m wrapped=$wrapped handed=$hit skipped=$skipped last=$prev\n";`, strconv.Itoa(n), strconv.Itoa(L))
		out, err := cmd.Output()
		if err != nil {
			t.Fatalf("%s: %v", c.name, err)
		}
		if p, err := bgOS.Info(kid.PID); err != nil || p.Stamp != kid.Stamp || p.SID != L {
			t.Fatalf("%s: the survivor did not live through the loop, the measurement means nothing: %+v %v", c.name, p, err)
		}
		res := strings.TrimSpace(string(out))
		fact(t, "ALLOCATOR %s (leader number %d, survivor in session): %s", c.name, L, res)
		handed := regexp.MustCompile(`handed=1`).MatchString(res)
		if c.mustNot && handed {
			t.Fatalf("%s: the kernel handed out a number that a live process still has as its group and session: %s", c.name, res)
		}
		if c.hold && (handed || (n >= 90000 && !regexp.MustCompile(`skipped=1`).MatchString(res))) {
			t.Fatalf("%s: a held (zombie) leader's number was reused or never passed: %s", c.name, res)
		}
		if handed && !c.hold {
			fact(t, "the session number WAS reused while a session member lived (leader unreaped until the unit ends is required, S3)")
		}
	}
}

// Many members, one ignoring SIGTERM, children in groups and sessions of their own: one Kill ends all of them, and a second is harmless.
func TestDarwinStopOfAUnitWithManyMembers(t *testing.T) {
	tr := startPerl(t, []string{"noterm"}, `for(1..60){k{}}for(1..10){k{setpgrp(0,0)}}for(1..10){k{POSIX::setsid()}}`+
		`k{$SIG{TERM}="IGNORE";say("noterm")};print "ready $$\n";sleep 60;`)
	r := newRealRig(t)
	r.record(tr.info("leader"))
	g := r.group()
	if n := len(r.members(g)); n != 82 {
		t.Fatalf("%d members, want 82", n)
	}
	start := time.Now()
	if err := g.Kill(""); err != nil {
		t.Fatal(err)
	}
	for end := time.Now().Add(10 * time.Second); len(r.members(g)) > 0; time.Sleep(20 * time.Millisecond) {
		if time.Now().After(end) {
			t.Fatalf("%d members left after Kill: %v", len(r.members(g)), r.members(g))
		}
	}
	fact(t, "82 members (2 groups' worth of job control, 10 sessions, one ignoring TERM) ended in %v", time.Since(start).Round(time.Millisecond))
	_ = tr.cmd.Wait()
	if err := g.Kill(""); err != nil {
		t.Fatalf("a second Kill of an empty unit: %v", err)
	}
}

// A process started through launchd is outside every session of the run: the honest limit, measured.
func TestDarwinALaunchdJobSurvivesStop(t *testing.T) {
	label := "io.monoes.i1582s2.job" + strconv.Itoa(os.Getpid())
	tr := startPerl(t, []string{"kid"}, `k{say("kid")};print "ready $$\n";sleep 60;`)
	if out, err := exec.Command("/bin/launchctl", "submit", "-l", label, "--", "/bin/sleep", "60").CombinedOutput(); err != nil {
		t.Skipf("launchctl submit is not available here: %v %s", err, out)
	}
	t.Cleanup(func() { _ = exec.Command("/bin/launchctl", "remove", label).Run() })
	var job int
	for end := time.Now().Add(5 * time.Second); job == 0 && time.Now().Before(end); time.Sleep(50 * time.Millisecond) {
		out, _ := exec.Command("/bin/launchctl", "list", label).CombinedOutput()
		if m := regexp.MustCompile(`"PID" = (\d+);`).FindSubmatch(out); m != nil {
			job, _ = strconv.Atoi(string(m[1]))
		}
	}
	if job == 0 {
		t.Skip("the launchd job did not start")
	}
	st, _ := bgOS.Info(job)
	t.Cleanup(func() {
		if p, err := bgOS.Info(job); err == nil && p.Stamp == st.Stamp {
			_ = bgOS.Signal(job, syscall.SIGKILL)
		}
	})
	r := newRealRig(t)
	r.record(tr.info("leader"))
	g := r.group()
	if r.members(g)[job] {
		t.Fatal("a launchd job is found as a member")
	}
	if err := g.Kill(""); err != nil {
		t.Fatal(err)
	}
	time.Sleep(300 * time.Millisecond)
	if p, err := bgOS.Info(job); err != nil || p.Stamp != st.Stamp {
		t.Fatal("the launchd job did not survive")
	}
	fact(t, "SURVIVES Stop: a process started through launchd (launchctl submit) pid %d", job)
}

// A held leader keeps its pid after it dies (a zombie), is not listed as a live process, and release reaps it.
func TestDarwinAHeldLeaderKeepsItsPidAndReleaseReapsIt(t *testing.T) {
	cmd := exec.Command("/bin/sleep", "30")
	cmd.SysProcAttr = &syscall.SysProcAttr{Setsid: true}
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	pid := cmd.Process.Pid
	p, err := bgOS.Info(pid)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := bgOS.Hold(pid, p.Stamp+"1"); err == nil {
		t.Fatal("a hold with the wrong stamp was taken")
	}
	if _, err := bgOS.Hold(1, "1.000001"); err == nil {
		t.Fatal("launchd was held")
	}
	release, err := bgOS.Hold(pid, p.Stamp)
	if err != nil {
		t.Fatal(err)
	}
	_ = bgOS.Signal(pid, syscall.SIGKILL)
	time.Sleep(200 * time.Millisecond)
	k, err := kinfo2(pid)
	if err != nil || k != szomb {
		t.Fatalf("the dead held leader should be a zombie that keeps its pid: state %d %v", k, err)
	}
	if _, err := bgOS.Info(pid); err == nil {
		t.Fatal("a zombie is read as a live process")
	}
	all, _ := bgOS.Scan()
	for _, q := range all {
		if q.PID == pid {
			t.Fatal("the scan lists the zombie")
		}
	}
	release()
	release() // idempotent
	if _, err := kinfo2(pid); err == nil {
		t.Fatal("release did not reap the leader")
	}
	fact(t, "held leader: zombie keeps pid %d until release, then reaped", pid)
}

func kinfo2(pid int) (int8, error) {
	k, err := unix.SysctlKinfoProc("kern.proc.pid", pid)
	if err != nil || int(k.Proc.P_pid) != pid {
		return 0, errGone
	}
	return k.Proc.P_stat, nil
}
