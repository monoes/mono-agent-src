package stop

import (
	"errors"
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

// ----- membership: who is in the unit -----

// scene builds the processes of one membership case around the recorded leader 500 (a child of the signaller, its own
// session): the host 10, the leader 500, and whatever the case adds.
type scene struct {
	name    string
	build   func(r *sessRig)
	members []int // the pids that must be members, besides nobody else
}

func TestSessMembershipTable(t *testing.T) {
	cases := []scene{
		{"the leader alone", func(r *sessRig) {}, []int{500}},
		{"a plain child in the leader's session", func(r *sessRig) { r.w.put(501, 500, 500, 500, "sh") }, []int{500, 501}},
		{"a child that left the group with setpgid stays in the session", func(r *sessRig) { r.w.put(501, 500, 501, 500, "git") }, []int{500, 501}},
		{"a child that made a session of its own is still a child", func(r *sessRig) { r.w.put(501, 500, 501, 501, "node") }, []int{500, 501}},
		{"a grandchild of a child that made a session", func(r *sessRig) {
			r.w.put(501, 500, 501, 501, "node")
			r.w.put(502, 501, 502, 502, "npm")
		}, []int{500, 501, 502}},
		{"a daemonised process (parent init, own session) is not seen", func(r *sessRig) {
			r.w.put(501, 1, 501, 501, "daemon")
		}, []int{500}},
		{"a double-forked grandchild reparented to init is not seen", func(r *sessRig) {
			r.w.put(501, 500, 501, 501, "mid")
			r.w.put(502, 1, 502, 502, "grandchild")
		}, []int{500, 501}},
		{"an unrelated process of the same user is not a member", func(r *sessRig) {
			r.w.put(600, 1, 600, 600, "editor")
			r.w.put(601, 600, 600, 600, "shell")
		}, []int{500}},
		{"a child of a non-member is not a member", func(r *sessRig) {
			r.w.put(600, 1, 600, 600, "editor")
			r.w.put(601, 600, 601, 601, "helper")
		}, []int{500}},
		{"a zombie is not a member", func(r *sessRig) { r.w.put(501, 500, 500, 500, "dead").zombie = true }, []int{500}},
		{"a child of another user is a member (it is listed, and a signal may be refused)", func(r *sessRig) {
			r.w.put(501, 500, 501, 501, "sudo").UID = 0
		}, []int{500, 501}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			r := newSessRig(t)
			l := r.w.leader(500)
			r.record(l)
			tc.build(r)
			v, err := r.c.view(r.rec)
			if err != nil || v.why != "" {
				t.Fatalf("%v %q", err, v.why)
			}
			var got []int
			for _, m := range v.members {
				got = append(got, m.PID)
			}
			if !equalInts(got, tc.members) {
				t.Fatalf("members %v, want %v", got, tc.members)
			}
			p, err := r.c.Prove(r.rec)
			if err != nil || p.Gone || p.Cg == nil || !p.BestEffort || p.Note != SessionBestEffortNote {
				t.Fatalf("a unit with a live leader is proven best effort and not gone: %+v %v", p, err)
			}
		})
	}
}

func equalInts(a, b []int) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

func TestSessTwoRecordedLeadersAreBothSessions(t *testing.T) {
	r := newSessRig(t)
	a, b := r.w.leader(500), r.w.leader(520)
	r.record(a)
	r.record(b)
	r.w.put(521, 520, 520, 520, "git")
	v, _ := r.c.view(r.rec)
	if len(v.members) != 3 {
		t.Fatalf("%+v", v.members)
	}
}

func TestSessALeaderThatIsNotRecordedIsNeverAMember(t *testing.T) {
	r := newSessRig(t)
	r.w.leader(500) // started, never adopted: no ledger line
	r.w.put(501, 500, 500, 500, "sh")
	p, err := r.c.Prove(r.rec)
	if err != nil || !p.Gone {
		t.Fatalf("an empty ledger is an empty unit: %+v %v", p, err)
	}
	if out := r.end(nil); out.Kind != UnitBestEffort || r.w.count("") != 0 || !r.w.has(500) || !r.w.has(501) {
		t.Fatalf("an unrecorded session was signalled: %+v %v", out, r.w.log)
	}
}

func TestSessOrphansOfADeadLeaderAreStillTheUnits(t *testing.T) {
	r := newSessRig(t)
	l := r.w.leader(500)
	r.record(l)
	r.w.put(501, 1, 500, 500, "worker") // the leader exited; the worker keeps the session number
	delete(r.w.procs, 500)
	v, _ := r.c.view(r.rec)
	if v.why != "" || len(v.members) != 1 || v.members[0].PID != 501 {
		t.Fatalf("%+v", v)
	}
	if out := r.end(nil); out.Kind != UnitBestEffort || r.w.has(501) {
		t.Fatalf("the orphan survived the end: %+v", out)
	}
}

// ----- pid reuse and the start stamp -----

func TestSessReusedLeaderNumberIsNotProvenAndNothingIsSignalled(t *testing.T) {
	r := newSessRig(t)
	l := r.w.leader(500)
	r.record(l)
	delete(r.w.procs, 500)
	imp := r.w.put(500, 1, 500, 500, "stranger") // the number now belongs to another session leader
	imp.Stamp = "1900000000.000001"
	r.w.put(502, 500, 502, 502, "its-child")
	r.useSess()
	if _, err := r.c.Prove(r.rec); !errors.Is(err, ErrNotProven) {
		t.Fatalf("%v", err)
	}
	if out := r.end(nil); out.Kind != UnitNotProven || len(r.w.log) != 0 || !r.w.has(500) || !r.w.has(502) {
		t.Fatalf("a stranger's session was signalled: %+v %v", out, r.w.log)
	}
}

func TestSessALeaderNumberReusedByAnOrdinaryProcessMeansTheUnitIsGone(t *testing.T) {
	r := newSessRig(t)
	l := r.w.leader(500)
	r.record(l)
	delete(r.w.procs, 500)
	other := r.w.put(500, 1, 400, 400, "stranger") // not a session leader: no process has session 500
	other.Stamp = "1900000000.000001"
	p, err := r.c.Prove(r.rec)
	if err != nil || !p.Gone || !p.BestEffort {
		t.Fatalf("%+v %v", p, err)
	}
	if out := r.end(nil); out.Kind != UnitBestEffort || !r.w.has(500) || len(r.w.log) != 0 {
		t.Fatalf("%+v %v", out, r.w.log)
	}
}

func TestSessAProcessOlderThanTheLeaderIsNotProvablyTheUnits(t *testing.T) {
	r := newSessRig(t)
	l := r.w.leader(500)
	r.record(l)
	delete(r.w.procs, 500)
	old := r.w.put(501, 1, 500, 500, "old")
	old.Stamp = "1700000000.000000" // started before the leader did
	if _, err := r.c.Prove(r.rec); !errors.Is(err, ErrNotProven) {
		t.Fatalf("%v", err)
	}
}

func TestSessBootTimeDifferenceIsNotEvidenceButFailsClosed(t *testing.T) {
	r := newSessRig(t)
	l := r.w.leader(500)
	r.record(l)
	r.w.boot = "boot-other"
	if _, err := r.c.Prove(r.rec); !errors.Is(err, ErrNotProven) {
		t.Fatalf("a different boot time with the number in use: %v", err)
	}
	delete(r.w.procs, 500)
	p, err := r.c.Prove(r.rec)
	if err != nil || !p.Gone || !p.BestEffort {
		t.Fatalf("a different boot time with nothing in use is gone, best effort: %+v %v", p, err)
	}
}

func TestSessLedgerThatCannotBeCheckedIsNotProven(t *testing.T) {
	for name, bad := range map[string]Leader{
		"pid 1":          {PID: 1, Stamp: "1.000001"},
		"zero pid":       {PID: 0, Stamp: "1.000001"},
		"no stamp":       {PID: 500},
		"unparsable":     {PID: 500, Stamp: "yesterday"},
		"negative usec":  {PID: 500, Stamp: "5.-3"},
		"usec too large": {PID: 500, Stamp: "5.9999999"},
	} {
		r := newSessRig(t)
		r.w.leader(500)
		if err := r.led.AddLeader(7, bad); err != nil {
			t.Fatal(err)
		}
		if _, err := r.c.Prove(r.rec); !errors.Is(err, ErrNotProven) {
			t.Errorf("%s: %v", name, err)
		}
	}
}

// ----- the identity proof -----

func TestSessProveFailsClosedOnEveryUnreadableFact(t *testing.T) {
	for name, set := range map[string]func(r *sessRig){
		"ledger unreadable": func(r *sessRig) { r.led.readErr = errors.New("witness unreadable") },
		"no ledger wired":   func(r *sessRig) { r.c.led = nil },
		"scan fails":        func(r *sessRig) { r.w.errs["Scan"] = errors.New("denied") },
		"self unknown":      func(r *sessRig) { r.w.errs["Self"] = errors.New("no self") },
		"no OS":             func(r *sessRig) { r.c.os = nil },
		"no handle":         func(r *sessRig) { r.rec.Handle = "" },
		"name mismatch":     func(r *sessRig) { r.rec.Generation = 8 },
		"nonce mismatch":    func(r *sessRig) { r.rec.Nonce = strings.Repeat("a", 32) },
	} {
		r := newSessRig(t)
		l := r.w.leader(500)
		r.record(l)
		set(r)
		if p, err := r.c.Prove(r.rec); !errors.Is(err, ErrNotProven) || p.Cg != nil || p.Gone {
			t.Errorf("%s: %+v %v", name, p, err)
		}
	}
}

func TestSessProveRefusesAUnitThatContainsTheSignaller(t *testing.T) {
	r := newSessRig(t)
	l := r.w.leader(500)
	r.record(l)
	r.w.self = 501
	r.w.put(501, 500, 501, 501, "stop-called-from-a-turn") // the signaller is a descendant of a member
	if _, err := r.c.Prove(r.rec); !errors.Is(err, ErrNotProven) || !strings.Contains(err.Error(), "signaller") {
		t.Fatalf("%v", err)
	}
	r.w.self = 10
	r.w.procs[10].SID = 500 // the signaller sits in the session
	if _, err := r.c.Prove(r.rec); !errors.Is(err, ErrNotProven) {
		t.Fatalf("%v", err)
	}
}

func TestSessGoneOnlyWhenEveryRecordedLeaderIsDead(t *testing.T) {
	r := newSessRig(t)
	l := r.w.leader(500)
	r.record(l)
	if p, _ := r.c.Prove(r.rec); p.Gone {
		t.Fatal("a live leader's unit is gone")
	}
	delete(r.w.procs, 500)
	if p, err := r.c.Prove(r.rec); err != nil || !p.Gone || !p.BestEffort {
		t.Fatalf("%+v %v", p, err)
	}
	// a second leader, alive: the unit is not gone although the first one is
	r.record(r.w.leader(520))
	if p, _ := r.c.Prove(r.rec); p.Gone {
		t.Fatal("a unit with one live recorded leader is gone")
	}
}

func TestSessPresentFollowsTheMembers(t *testing.T) {
	r := newSessRig(t)
	l := r.w.leader(500)
	r.record(l)
	if ok, err := r.c.Present(r.rec); err != nil || !ok {
		t.Fatalf("%v %v", ok, err)
	}
	delete(r.w.procs, 500)
	if ok, err := r.c.Present(r.rec); err != nil || ok {
		t.Fatalf("%v %v", ok, err)
	}
	r.led.readErr = errors.New("x")
	if _, err := r.c.Present(r.rec); err == nil {
		t.Fatal("an unreadable ledger is not 'absent'")
	}
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
	last := map[string]int{}
	first := map[string]int{}
	for i, e := range r.w.log {
		k := e[:strings.IndexByte(e, ':')]
		last[k] = i
		if _, ok := first[k]; !ok {
			first[k] = i
		}
	}
	if !(last["term"] < first["stop"]) || !(last["stop"] < first["kill"]+0 || last["stop"] < first["killgrp"]) {
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

func TestSessS1ChangesNoPlatformChoiceAndNoRealOS(t *testing.T) {
	if platformKinds["darwin"] != KindPGroup || platformKinds["linux"] != KindCgroup2 || platformKinds["windows"] != KindJobObject {
		t.Fatalf("a platform choice changed in S1: %v", platformKinds)
	}
	if _, ok := containments[KindSession]; !ok {
		t.Fatal("the session kind is not registered")
	}
	if realSessionOS() != nil {
		t.Fatal("S1 adds no OS binding")
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

// After a real reboot the old pids are mostly handed out again: a number held by an ordinary process (not the leader of a
// session with that number) must not keep the unit "not proven" for ever, or a stop after a reboot could never finish.
func TestSessAfterARebootANumberInUseByAnOrdinaryProcessIsStillGone(t *testing.T) {
	r := newSessRig(t)
	l := r.w.leader(500)
	r.record(l)
	delete(r.w.procs, 500)
	r.w.boot = "boot-other"
	nc := r.w.put(500, 1, 400, 400, "newcomer") // the number is in use again, in another session
	nc.Stamp = "1900000000.000003"
	p, err := r.c.Prove(r.rec)
	if err != nil || !p.Gone || !p.BestEffort {
		t.Fatalf("%+v %v", p, err)
	}
}
