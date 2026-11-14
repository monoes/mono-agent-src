package stop

import (
	"errors"
	"strings"
	"testing"
)

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

// The number of a dead leader was taken by a stranger that leads a session of that number: the stranger and its
// descendants are its own and are left alone (no member, nothing signalled), and the unit is gone.
func TestSessReusedLeaderNumberLeavesTheStrangerAndItsDescendantsAlone(t *testing.T) {
	r := newSessRig(t)
	l := r.w.leader(500)
	r.record(l)
	delete(r.w.procs, 500)
	imp := r.w.put(500, 1, 500, 500, "stranger") // the number now belongs to another session leader
	imp.Stamp = "1900000000.000001"
	r.w.put(502, 500, 502, 502, "its-child")
	r.w.put(503, 502, 502, 500, "its-grandchild")
	p, err := r.c.Prove(r.rec)
	if err != nil || !p.Gone {
		t.Fatalf("%+v %v", p, err)
	}
	if out := r.end(nil); out.Kind != UnitBestEffort || len(r.w.log) != 0 || !r.w.has(500) || !r.w.has(502) || !r.w.has(503) {
		t.Fatalf("a stranger's session was signalled: %+v %v", out, r.w.log)
	}
}

// An orphan of the old session that is older than the stranger holding the number cannot be the stranger's: it is ended.
// One started after the stranger, outside its descendants, cannot be told apart: it is listed and never signalled.
func TestSessOrphansOfAReusedNumberAreSplitByStartTime(t *testing.T) {
	r := newSessRig(t)
	l := r.w.leader(500)
	r.record(l)
	delete(r.w.procs, 500)
	old := r.w.put(501, 1, 501, 500, "old-orphan") // started before the stranger: ours
	old.Stamp = "1850000000.000001"
	old.noTerm = true
	imp := r.w.put(500, 1, 500, 500, "stranger")
	imp.Stamp = "1900000000.000001"
	newer := r.w.put(504, 1, 504, 500, "newer-orphan") // started after it, not its descendant: cannot be told apart
	newer.Stamp = "1900000001.000001"
	newer.noTerm = true
	r.w.put(502, 500, 502, 500, "its-child")
	out := r.end(nil)
	if r.w.has(501) {
		t.Fatal("the old orphan was not ended")
	}
	if !r.w.has(500) || !r.w.has(502) || !r.w.has(504) {
		t.Fatalf("a process that is not provably the unit's was signalled: %v", r.w.log)
	}
	if out.Kind != UnitStragglers || len(out.Survivors) != 1 || out.Survivors[0].PID != 504 || !strings.Contains(out.Survivors[0].Why, "was not signalled") {
		t.Fatalf("the process that could not be tied must be listed, not hidden: %+v", out)
	}
}

// One ambiguous number never blocks the others: the healthy leader's session is ended, the ambiguous process is listed.
func TestSessAnAmbiguousNumberDoesNotBlockTheOtherLeaders(t *testing.T) {
	r := newSessRig(t)
	good := r.w.leader(520)
	good.noTerm = true
	r.record(good)
	r.w.put(521, 520, 521, 520, "git").noTerm = true
	bad := r.w.leader(500)
	r.record(bad)
	delete(r.w.procs, 500)
	older := r.w.put(501, 1, 500, 500, "older-than-leader")
	older.Stamp = "1700000000.000000"
	older.noTerm = true
	out := r.end(nil)
	if r.w.has(520) || r.w.has(521) {
		t.Fatalf("the healthy session was not ended: %v", r.w.procs)
	}
	if !r.w.has(501) || out.Kind != UnitStragglers || len(out.Survivors) != 1 || out.Survivors[0].PID != 501 {
		t.Fatalf("%+v", out)
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

// The boot time is not evidence (the clock can be stepped): a leader alive with its exact recorded start stamp rules a
// reboot out, so the unit is still proven. With the leader gone, the processes that carry its number after a change of
// boot time are another boot's and are left alone.
func TestSessBootTimeDifferenceDoesNotOutweighALiveLeader(t *testing.T) {
	r := newSessRig(t)
	l := r.w.leader(500)
	r.record(l)
	r.w.boot = "boot-other"
	if p, err := r.c.Prove(r.rec); err != nil || p.Cg == nil {
		t.Fatalf("a live recorded leader with a stepped clock: %+v %v", p, err)
	}
	delete(r.w.procs, 500)
	r.w.put(501, 1, 501, 500, "after-the-boot")
	p, err := r.c.Prove(r.rec)
	if err != nil || !p.Gone || !p.BestEffort {
		t.Fatalf("a different boot with the leader gone is gone, best effort: %+v %v", p, err)
	}
	if out := r.end(nil); !r.w.has(501) || len(r.w.log) != 0 {
		t.Fatalf("another boot's process was signalled: %+v %v", out, r.w.log)
	}
}

func TestSessTwoLedgerEntriesForOneNumberAreOneSession(t *testing.T) {
	r := newSessRig(t)
	first := r.w.leader(500) // an earlier turn's leader had this number and its own stamp
	r.record(first)
	delete(r.w.procs, 500)
	second := r.w.leader(500) // a later turn leader got the same number
	second.Stamp = "1900000000.000007"
	r.record(second)
	r.w.put(501, 500, 501, 500, "git")
	v, err := r.c.view(r.rec)
	if err != nil || len(v.members) != 2 || len(v.unproven) != 0 {
		t.Fatalf("%+v %v", v, err)
	}
}

func TestSessLeadersOfAnotherUnitOfTheGenerationAreNotThisUnits(t *testing.T) {
	r := newSessRig(t)
	l := r.w.leader(500)
	if err := r.led.AddLeader(7, Leader{PID: l.PID, Stamp: l.Stamp, Unit: r.rec.CgroupID + 2}); err != nil {
		t.Fatal(err)
	}
	if p, err := r.c.Prove(r.rec); err != nil || !p.Gone {
		t.Fatalf("a leader recorded for another unit is in this one: %+v %v", p, err)
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
		bad.Unit = r.rec.CgroupID
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
		"ledger unreadable":  func(r *sessRig) { r.led.readErr = errors.New("witness unreadable") },
		"no ledger wired":    func(r *sessRig) { r.c.led = nil },
		"scan fails":         func(r *sessRig) { r.w.errs["Scan"] = errors.New("denied") },
		"boot id unreadable": func(r *sessRig) { r.w.errs["BootID"] = errors.New("no boot id") },
		"self unknown":       func(r *sessRig) { r.w.errs["Self"] = errors.New("no self") },
		"no OS":              func(r *sessRig) { r.c.os = nil },
		"no handle":          func(r *sessRig) { r.rec.Handle = "" },
		"name mismatch":      func(r *sessRig) { r.rec.Generation = 8 },
		"nonce mismatch":     func(r *sessRig) { r.rec.Nonce = strings.Repeat("a", 32) },
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
	if p, err := r.c.Prove(r.rec); err != nil || !p.Gone || !p.BestEffort || !strings.Contains(p.Note, "launchd, open or XPC") {
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
