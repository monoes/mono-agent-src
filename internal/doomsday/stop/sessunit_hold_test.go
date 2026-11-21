package stop

import (
	"errors"
	"strings"
	"testing"
)

// ----- remedy 2: a session seen gone is retired, and a recycled number is never taken for the unit's -----

// The scenario of the number-reuse hole: the leader and its session are gone and seen so; an unrelated session then takes the
// number, its leader exits and leaves a child that carries the number. That child is not the unit's and is never signalled.
func TestSessRecycledNumberOfARetiredSessionIsNotTheUnits(t *testing.T) {
	r := newSessRig(t)
	l := r.w.leader(500)
	r.record(l)
	delete(r.w.procs, 500)                                 // the leader and its session are gone ...
	if p, err := r.c.Prove(r.rec); err != nil || !p.Gone { // ... and a look at the unit sees it
		t.Fatalf("%+v %v", p, err)
	}
	got, _ := r.led.Leaders(7)
	if len(got) != 1 || !got[0].Empty {
		t.Fatalf("the session seen gone was not recorded as gone: %+v", got)
	}
	// the number is taken by an unrelated session whose leader exits, leaving a child
	stranger := r.w.put(500, 1, 500, 500, "unrelated-leader")
	stranger.Stamp = "1900000000.000001"
	child := r.w.put(501, 500, 501, 500, "unrelated-child")
	child.Stamp = "1900000001.000001"
	delete(r.w.procs, 500)
	child.PPID = 1
	if p, err := r.c.Prove(r.rec); err != nil || !p.Gone {
		t.Fatalf("a recycled number made the unit non-empty: %+v %v", p, err)
	}
	if out := r.end(nil); out.Kind != UnitBestEffort || !r.w.has(501) || len(r.w.log) != 0 {
		t.Fatalf("an unrelated process of a recycled number was signalled: %+v %v", out, r.w.log)
	}
}

func TestSessSessionIsRetiredOnlyWhenItIsGone(t *testing.T) {
	r := newSessRig(t)
	live := r.w.leader(500)
	r.record(live)
	orphaned := r.w.leader(520)
	r.record(orphaned)
	delete(r.w.procs, 520)
	r.w.put(521, 1, 521, 520, "orphan") // the leader exited, a member stays
	gone := r.w.leader(540)
	r.record(gone)
	delete(r.w.procs, 540)
	if _, err := r.c.Prove(r.rec); err != nil {
		t.Fatal(err)
	}
	got, _ := r.led.Leaders(7)
	empty := map[int]bool{}
	for _, l := range got {
		empty[l.PID] = l.Empty
	}
	if empty[500] || empty[520] || !empty[540] {
		t.Fatalf("only the session seen gone is retired: %v", empty)
	}
	if r.led.retires != 1 {
		t.Fatalf("%d lines", r.led.retires)
	}
	// looking again writes nothing new
	if _, err := r.c.Prove(r.rec); err != nil || r.led.retires != 1 {
		t.Fatalf("%v %d", err, r.led.retires)
	}
}

func TestSessRetireFailureDoesNotFailTheProof(t *testing.T) {
	r := newSessRig(t)
	r.record(r.w.leader(500))
	delete(r.w.procs, 500)
	r.led.retErr = errors.New("witness busy")
	if p, err := r.c.Prove(r.rec); err != nil || !p.Gone {
		t.Fatalf("%+v %v", p, err)
	}
}

// ----- remedy 1: a recorded leader stays unreaped, so its number cannot be given to anyone else -----

func (r *sessRig) adopt(p *fakeSessProc) error { return r.placer().Adopt(p.PID, p.Stamp) }

func TestSessAdoptKeepsTheLeadersNumberReserved(t *testing.T) {
	r := newSessRig(t)
	l := r.w.leader(500)
	if err := r.adopt(l); err != nil {
		t.Fatal(err)
	}
	if heldCount(r.rec.CgroupID) != 1 {
		t.Fatal("the adopted leader is not held")
	}
	delete(r.w.procs, 500) // the leader exits: it stays an unreaped zombie, the number stays reserved
	if _, err := r.w.tryPut(500, 1, 500, 500, "stranger"); err == nil {
		t.Fatal("the number of an unreaped leader was handed to another process")
	}
	// the session is seen gone: the hold is let go with the line that retires it
	if _, err := r.c.Prove(r.rec); err != nil {
		t.Fatal(err)
	}
	if heldCount(r.rec.CgroupID) != 0 || r.w.held[500] {
		t.Fatal("the hold of a session seen gone was kept")
	}
	if _, err := r.w.tryPut(500, 1, 500, 500, "stranger"); err != nil {
		t.Fatalf("the number is free again: %v", err)
	}
}

func TestSessAdoptingTheSameLeaderAgainHoldsItOnce(t *testing.T) {
	r := newSessRig(t)
	l := r.w.leader(500)
	for i := 0; i < 3; i++ {
		if err := r.adopt(l); err != nil {
			t.Fatal(err)
		}
	}
	if heldCount(r.rec.CgroupID) != 1 {
		t.Fatalf("%d holds", heldCount(r.rec.CgroupID))
	}
}

func TestSessAdoptRefusesWhatItCannotHold(t *testing.T) {
	r := newSessRig(t)
	l := r.w.leader(500)
	r.w.errs["Hold"] = errors.New("cannot keep it")
	if err := r.adopt(l); !errors.Is(err, ErrNotPlaced) || r.led.adds != 0 {
		t.Fatalf("a leader whose number cannot be kept was recorded: %v (%d lines)", err, r.led.adds)
	}
	delete(r.w.errs, "Hold")
	r.led.addErr = errors.New("witness full")
	if err := r.adopt(l); !errors.Is(err, ErrNotPlaced) || !errors.Is(err, ErrReapOwned) || heldCount(r.rec.CgroupID) != 1 || !r.w.held[500] {
		t.Fatalf("a refused leader must keep its hold (the launcher kills it by number meanwhile): %v", err)
	}
	ReleaseAllUnitHolds()
	if heldCount(r.rec.CgroupID) != 0 || r.w.held[500] {
		t.Fatal("the shutdown release left the hold of an unrecorded leader")
	}
}

// The zombies a unit keeps are bounded: at the cap another turn is refused rather than another leader kept.
func TestSessUnreapedLeadersAreBounded(t *testing.T) {
	old := sessMaxHolds
	sessMaxHolds = 3
	t.Cleanup(func() { sessMaxHolds = old })
	r := newSessRig(t)
	var ls []*fakeSessProc
	for i := 0; i < 4; i++ {
		ls = append(ls, r.w.leader(600+i))
	}
	for i := 0; i < 3; i++ {
		if err := r.adopt(ls[i]); err != nil {
			t.Fatal(err)
		}
	}
	err := r.adopt(ls[3])
	if !errors.Is(err, ErrNotPlaced) || !strings.Contains(err.Error(), "unreaped") || r.led.adds != 3 {
		t.Fatalf("the cap did not refuse the next turn: %v (%d lines)", err, r.led.adds)
	}
	// a session that ended frees its hold at the next look, and the turn can start
	delete(r.w.procs, 600)
	if err := r.adopt(ls[3]); err != nil {
		t.Fatalf("a freed hold did not make room: %v", err)
	}
}

func TestSessReleaseUnitHoldsLetsEveryLeaderGo(t *testing.T) {
	r := newSessRig(t)
	a, b := r.w.leader(500), r.w.leader(520)
	for _, l := range []*fakeSessProc{a, b} {
		if err := r.adopt(l); err != nil {
			t.Fatal(err)
		}
	}
	ReleaseUnitHolds(r.rec.CgroupID)
	if heldCount(r.rec.CgroupID) != 0 || r.w.held[500] || r.w.held[520] {
		t.Fatal("holds were kept after the unit's release")
	}
}

// A retired leader's number is not the unit's: a leader with that number recorded again later is a new entry.
func TestSessRetiredLeaderDoesNotHideALaterLeaderOfTheSameNumber(t *testing.T) {
	r := newSessRig(t)
	r.record(r.w.leader(500))
	delete(r.w.procs, 500)
	if _, err := r.c.Prove(r.rec); err != nil {
		t.Fatal(err)
	}
	second := r.w.leader(500)
	second.Stamp = "1900000000.000007"
	r.record(second)
	r.w.put(501, 500, 501, 500, "git")
	v, err := r.c.view(r.rec)
	if err != nil || len(v.members) != 2 {
		t.Fatalf("%+v %v", v, err)
	}
}
