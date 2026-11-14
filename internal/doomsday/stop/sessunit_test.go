package stop

import (
	"errors"
	"runtime"
	"strings"
	"syscall"
	"testing"
)

// ----- create -----

func TestSessCreateRecordsAUnitAndStartsNoProcess(t *testing.T) {
	r := newSessRig(t)
	if r.rec.Kind != KindSession || r.rec.Generation != 7 {
		t.Fatalf("%+v", r.rec)
	}
	if g, n, ok := UnitGeneration(r.rec.Name); !ok || g != 7 || n != r.rec.Nonce {
		t.Fatalf("the name does not carry the generation and nonce: %q", r.rec.Name)
	}
	if r.rec.AnchorPID != 0 || r.rec.AnchorStamp != "" {
		t.Fatalf("a session unit has no anchor process: %+v", r.rec)
	}
	if got := strings.Join(r.j.ev, ","); got != "intent,bound" {
		t.Fatalf("journal order %q", got)
	}
	if file, ok, err := r.dir.ReadUnitFile(7); err != nil || !ok || file != r.rec {
		t.Fatalf("the key-free record is not on disk: %+v %v %v", file, ok, err)
	}
	if len(r.w.procs) != 2 || r.w.count("") != 0 {
		t.Fatalf("Create started or signalled a process: %d procs, log %v", len(r.w.procs), r.w.log)
	}
	rec2, err := r.c.Create(bg, CreateArgs{Dir: newRunDir(t, nil)}, 7)
	if err != nil || rec2.Nonce == r.rec.Nonce || rec2.Name == r.rec.Name {
		t.Fatalf("two units of one generation share an identity: %+v %v", rec2, err)
	}
}

func TestSessCreateRefusesAndLeavesNoUnit(t *testing.T) {
	for name, tc := range map[string]struct {
		setup func(r *sessRig)
		args  func(r *sessRig) CreateArgs
	}{
		"boot id unreadable":         {setup: func(r *sessRig) { r.w.errs["BootID"] = errors.New("no boot") }},
		"creator unknown":            {setup: func(r *sessRig) { r.w.errs["Self"] = errors.New("no self") }},
		"processes cannot be listed": {setup: func(r *sessRig) { r.w.errs["Scan"] = errors.New("denied") }},
		"witness refuses the intent": {args: func(r *sessRig) CreateArgs {
			return CreateArgs{Dir: newRunDir(t, nil), Journal: sessFailJournal{intent: true}}
		}},
		"witness refuses the binding": {args: func(r *sessRig) CreateArgs {
			return CreateArgs{Dir: newRunDir(t, nil), Journal: sessFailJournal{bound: true}}
		}},
		"unit file cannot be written": {args: func(r *sessRig) CreateArgs {
			f := newFaultFS()
			f.failCreate["8.unit"] = true
			return CreateArgs{Dir: newRunDir(t, f)}
		}},
	} {
		t.Run(name, func(t *testing.T) {
			r := newSessRig(t)
			if tc.setup != nil {
				tc.setup(r)
			}
			a := CreateArgs{Dir: newRunDir(t, nil)}
			if tc.args != nil {
				a = tc.args(r)
			}
			if _, err := r.c.Create(bg, a, 8); !errors.Is(err, ErrUnitNotCreated) {
				t.Fatalf("%v", err)
			}
			if _, ok, _ := a.Dir.ReadUnitFile(8); ok {
				t.Fatal("a refused unit left a record behind")
			}
		})
	}
	if _, err := (sessContainment{}).Create(bg, CreateArgs{Dir: newRunDir(t, nil)}, 8); !errors.Is(err, ErrUnitNotCreated) {
		t.Fatalf("no OS: %v", err)
	}
}

type sessFailJournal struct{ intent, bound bool }

func (f sessFailJournal) Intent(uint64, string, string, string) error {
	if f.intent {
		return errInjected
	}
	return nil
}
func (f sessFailJournal) Bound(UnitRecord) error {
	if f.bound {
		return errInjected
	}
	return nil
}

// ----- the end -----

func TestSessEndTermsThenFreezesThenKillsAndIsBestEffort(t *testing.T) {
	r := newSessRig(t)
	l := r.w.leader(500)
	r.record(l)
	for pid := 501; pid <= 503; pid++ {
		p := r.w.put(pid, 500, pid, 500, "w")
		p.noTerm = true
	}
	r.w.procs[500].noTerm = true
	out := r.end(nil)
	if out.Kind != UnitBestEffort || len(out.Survivors) != 0 {
		t.Fatalf("a session end is never a proven end: %+v", out)
	}
	if !strings.Contains(out.Note, "launchd, open or XPC") || !strings.Contains(out.Note, "best effort") {
		t.Fatalf("the end does not state its limit: %q", out.Note)
	}
	for pid := range r.w.procs {
		if pid != 1 && pid != 10 {
			t.Fatalf("process %d is still alive", pid)
		}
	}
	// order: every term, then every freeze, then the kills; each kill follows its own freeze
	idx := func(match func(string) bool) (first, last int) {
		first, last = -1, -1
		for i, e := range r.w.log {
			if match(e) {
				if first < 0 {
					first = i
				}
				last = i
			}
		}
		return
	}
	_, lastTerm := idx(func(e string) bool { return strings.HasPrefix(e, "term:") })
	firstStop, lastStop := idx(func(e string) bool { return strings.HasPrefix(e, "stop:") })
	firstKill, _ := idx(func(e string) bool { return strings.HasPrefix(e, "kill") })
	if lastTerm < 0 || firstStop < 0 || firstKill < 0 || lastTerm > firstStop || lastStop > firstKill {
		t.Fatalf("order of term, stop, kill is wrong: %v", r.w.log)
	}
}

func TestSessKillStopsEveryMemberBeforeTheFirstKill(t *testing.T) {
	r := newSessRig(t)
	l := r.w.leader(500)
	l.noTerm = true
	r.record(l)
	for pid := 501; pid <= 504; pid++ {
		r.w.put(pid, 500, pid, 500, "w").noTerm = true
	}
	g := &sessGroup{c: r.c, rec: r.rec}
	if err := g.Kill(""); err != nil {
		t.Fatal(err)
	}
	stops, killAt := 0, -1
	for i, e := range r.w.log {
		if strings.HasPrefix(e, "stop:") {
			if killAt >= 0 {
				t.Fatalf("a freeze after a kill began: %v", r.w.log)
			}
			stops++
		} else if killAt < 0 {
			killAt = i
		}
	}
	if stops != 5 || killAt < 0 {
		t.Fatalf("stops %d, log %v", stops, r.w.log)
	}
}

func TestSessKillUsesTheGroupNumberOnlyWhenEveryProcessOfTheGroupIsAMember(t *testing.T) {
	r := newSessRig(t)
	l := r.w.leader(500)
	l.noTerm = true
	r.record(l)
	r.w.put(501, 500, 501, 500, "pipeline-a").noTerm = true // group 501: only members
	r.w.put(502, 500, 501, 500, "pipeline-b").noTerm = true
	r.w.put(503, 500, 503, 500, "shared-a").noTerm = true // group 503 also holds an outsider
	r.w.put(900, 1, 503, 900, "outsider")
	g := &sessGroup{c: r.c, rec: r.rec}
	if err := g.Kill(""); err != nil {
		t.Fatal(err)
	}
	if !r.w.has(900) {
		t.Fatal("the outsider that shares a group number with a member was killed")
	}
	for _, e := range r.w.log {
		if e == "killgrp:503" {
			t.Fatalf("a group with an outsider was killed by number: %v", r.w.log)
		}
	}
	if r.w.count("killgrp:501") != 1 {
		t.Fatalf("a group of members only is killed whole: %v", r.w.log)
	}
	if r.w.has(503) || r.w.has(501) || r.w.has(502) || r.w.has(500) {
		t.Fatalf("members survived: %v", r.w.procs)
	}
}

func TestSessSurvivorsAreListedWithTheirOwnReason(t *testing.T) {
	r := newSessRig(t)
	l := r.w.leader(500)
	r.record(l)
	imm := r.w.put(501, 500, 501, 500, "unkillable")
	imm.immune, imm.noTerm = true, true
	su := r.w.put(502, 500, 502, 502, "sudo")
	su.UID, su.deniedSig, su.noTerm = 0, true, true
	out := r.end(nil)
	if out.Kind != UnitStragglers {
		t.Fatalf("a survivor is never hidden behind a best-effort end: %+v", out)
	}
	if !strings.Contains(out.Note, "launchd, open or XPC") {
		t.Fatalf("the limit is stated for an end that is not clean too: %q", out.Note)
	}
	why := map[int]string{}
	for _, s := range out.Survivors {
		why[s.PID] = s.Why
	}
	if !strings.Contains(why[501], "stayed in the session after KILL") {
		t.Errorf("immune survivor: %v", why)
	}
	if !strings.Contains(why[502], "could not be signalled") || !strings.Contains(why[502], "uid 0") {
		t.Errorf("other-user survivor: %v", why)
	}
}

func TestSessASurvivorStaysListedAfterItsParentDied(t *testing.T) {
	r := newSessRig(t)
	l := r.w.leader(500)
	r.record(l)
	kid := r.w.put(501, 500, 501, 501, "sudo-child") // a child with a session of its own: a member through its parent
	kid.UID, kid.deniedSig, kid.noTerm = 0, true, true
	out := r.end(nil)
	if r.w.has(500) {
		t.Fatal("the leader survived")
	}
	if out.Kind != UnitStragglers || len(out.Survivors) != 1 || out.Survivors[0].PID != 501 {
		t.Fatalf("a survivor whose parent was ended first dropped out of the list: %+v", out)
	}
}

func TestSessEscapedDaemonIsNotKilledAndTheEndSaysBestEffort(t *testing.T) {
	r := newSessRig(t)
	l := r.w.leader(500)
	r.record(l)
	r.w.put(501, 500, 501, 501, "mid")
	r.w.put(502, 1, 502, 502, "escaped") // detached: parent init, session of its own
	out := r.end(nil)
	if out.Kind != UnitBestEffort || len(out.Survivors) != 0 {
		t.Fatalf("%+v", out)
	}
	if !r.w.has(502) {
		t.Fatal("a process outside the unit was killed")
	}
	rep := Report{Generation: 7, Final: StateQuiescent, Unit: out}
	if rep.Stopped() || !rep.StoppedBestEffort() || rep.ExitCode() != 0 {
		t.Fatal("the answer is the best-effort stop")
	}
	lines := strings.Join(rep.Lines(), "\n")
	if !strings.Contains(lines, "stopped (best effort:") || strings.Contains(lines, "(quiescent)") {
		t.Fatalf("the stop must say best effort, never a bare stopped:\n%s", lines)
	}
}

func TestSessEndDoesNotActOnARecordOnlyTheRunDirectoryHolds(t *testing.T) {
	r := newSessRig(t)
	l := r.w.leader(500)
	r.record(l)
	r.useSess()
	for name, witness := range map[string]func(uint64) []UnitRecord{
		"no witness wired":      nil,
		"witness holds nothing": func(uint64) []UnitRecord { return nil },
	} {
		u := UnitEnder{K: newFakeKernel(), Dir: r.dir, Witness: witness, Cfg: EndConfig{Clock: newStepClock()}}
		out := u.End(bg, 7, nil)
		if out.Kind != UnitNotProven || len(r.w.log) != 0 || !r.w.has(500) {
			t.Fatalf("%s: %+v %v", name, out, r.w.log)
		}
	}
}

func TestSessFlatUnitWithABegunOperationKeepsTheRunPending(t *testing.T) {
	r := newSessRig(t)
	l := r.w.leader(500)
	r.record(l)
	out := r.end([]uint64{3})
	if out.Kind != UnitStragglers || len(r.w.log) != 0 || !r.w.has(500) {
		t.Fatalf("%+v %v", out, r.w.log)
	}
}

func TestSessSignalRefusesAPidThatIsNotAMember(t *testing.T) {
	r := newSessRig(t)
	l := r.w.leader(500)
	r.record(l)
	r.w.put(600, 1, 600, 600, "bystander")
	g := &sessGroup{c: r.c, rec: r.rec}
	if err := g.Signal("", 600, syscall.SIGKILL); err == nil || !r.w.has(600) || len(r.w.log) != 0 {
		t.Fatalf("%v %v", err, r.w.log)
	}
	if err := g.Signal("", 500, syscall.SIGTERM); err != nil || r.w.has(500) {
		t.Fatalf("%v", err)
	}
}

func TestSessRmdirRefusesWhileAMemberIsListed(t *testing.T) {
	r := newSessRig(t)
	l := r.w.leader(500)
	r.record(l)
	g := &sessGroup{c: r.c, rec: r.rec}
	if err := g.Rmdir(""); err == nil {
		t.Fatal("Rmdir passed with a live member")
	}
	delete(r.w.procs, 500)
	if err := g.Rmdir(""); err != nil {
		t.Fatal(err)
	}
}

// ----- the seam: dispatch, platform, probe -----

func TestSessPlatformChoicesAndTheOSBinding(t *testing.T) {
	if platformKinds["darwin"] != KindSession || platformKinds["linux"] != KindCgroup2 || platformKinds["windows"] != KindJobObject {
		t.Fatalf("unexpected platform choice: %v", platformKinds)
	}
	if _, ok := containments[KindSession]; !ok {
		t.Fatal("the session kind is not registered")
	}
	if runtime.GOOS != "darwin" && realSessionOS() != nil { // the macOS binding exists since S2
		t.Fatal("only macOS has an OS binding")
	}
	if err := (sessContainment{}).Probe(); !errors.Is(err, ErrContainmentUnavailable) {
		t.Fatalf("with no OS binding the kind refuses: %v", err)
	}
	// a session record is not proven on a host whose kind it is not (every GOOS today)
	rec := UnitRecord{Kind: KindSession}
	for _, goos := range []string{"linux", "darwin", "windows"} {
		hostAs(t, goos)
		if _, err := ProveUnit(newFakeKernel(), rec); !errors.Is(err, ErrNotProven) {
			t.Fatalf("%s: %v", goos, err)
		}
	}
}

func TestSessDispatchByPlatformAndProbeRunsTheSelfTest(t *testing.T) {
	r := newSessRig(t)
	r.useSess()
	rec, err := createUnit(bg, "darwin", nil, CreateArgs{Dir: newRunDir(t, nil)}, 9)
	if err != nil || rec.Kind != KindSession {
		t.Fatalf("%+v %v", rec, err)
	}
	if err := probe("darwin", nil); err != nil {
		t.Fatalf("%v", err)
	}
	r.w.errs["SelfTest"] = errors.New("setsid child not found")
	if err := probe("darwin", nil); !errors.Is(err, ErrContainmentUnavailable) || !strings.Contains(err.Error(), "session create, find and kill") {
		t.Fatalf("%v", err)
	}
	failing := hostStepsOK
	r.w.errs["SelfTest"] = nil
	r.c.hostSteps = func() []ProbeStep {
		return append(failing(), ProbeStep{"host pin", func() error { return errInjected }})
	}
	if err := r.c.Probe(); !errors.Is(err, ErrContainmentUnavailable) || !strings.Contains(err.Error(), "host pin") {
		t.Fatalf("a failing host step must fail the probe: %v", err)
	}
}

// Without a leader ledger no turn could ever be recorded, so no unit is made ("no unit, no run").
func TestSessCreateNeedsALeaderLedger(t *testing.T) {
	r := newSessRig(t)
	r.c.led = nil
	a := CreateArgs{Dir: newRunDir(t, nil)}
	if _, err := r.c.Create(bg, a, 8); !errors.Is(err, ErrUnitNotCreated) {
		t.Fatalf("%v", err)
	}
	if _, ok, _ := a.Dir.ReadUnitFile(8); ok {
		t.Fatal("a unit with no ledger left a record behind")
	}
}
