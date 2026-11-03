package doomsday

import (
	"context"
	"os/exec"
	"testing"
)

// A merge under an open run needs its git commands placed in the run's unit: with no containment kernel the
// connection is refused, never handed out with a placer that would run git outside the unit.
func TestMergeForRefusesWhenGitCannotBePlacedInTheUnit(t *testing.T) {
	w, db := newWire(t, func() error { return nil }, fakeKernel{}, nil)
	w.start(t)
	g := &HostGate{Store: w.w.Chained(), Fresh: w.w.Fresh(), Profile: "p1", RunDir: w.w.RunDir, Journal: MergeJournal(db.DB, w.w.Store)}
	if dd, err := g.MergeFor(context.Background()); dd != nil || err == nil {
		t.Fatalf("no kernel: %v, %v", dd, err)
	}
}

// Recovery's placement follows the open run: none while no run is open, the run's unit while one is, a refusal
// when the run's unit cannot be placed in.
func TestRecoveryPlacementFollowsTheOpenRun(t *testing.T) {
	w, db := newWire(t, func() error { return nil }, fakeKernel{}, nil)
	withDB(w, db)
	ctx := context.Background()
	if p, err := w.w.placementOfOpenRun(ctx); p != nil || err != nil {
		t.Fatalf("no run open: %v, %v", p != nil, err)
	}
	w.start(t)
	if p, err := w.w.placementOfOpenRun(ctx); p == nil || err != nil {
		t.Fatalf("run open: %v, %v", p != nil, err)
	}
	w.w.Kernel = nil
	if p, err := w.w.placementOfOpenRun(ctx); p != nil || err == nil {
		t.Fatalf("run open and no kernel: %v, %v", p != nil, err)
	}
}

// The launcher placement hook: with no open run the command is untouched; with a run open and a unit that cannot
// be placed in, it is refused (the launcher must not start it); a foreign profile is refused.
func TestPlaceCmdFollowsTheOpenRunAndFailsClosed(t *testing.T) {
	w, _ := newWire(t, func() error { return nil }, fakeKernel{}, nil)
	g := &HostGate{Store: w.w.Chained(), Fresh: w.w.Fresh(), Profile: "p1", RunDir: w.w.RunDir, Kernel: w.w.Kernel}
	cmd := exec.Command("true")
	if err := g.PlaceCmd(cmd, "p1", "c1"); err != nil || cmd.SysProcAttr != nil {
		t.Fatalf("no run open: %v, %+v", err, cmd.SysProcAttr)
	}
	w.start(t)
	cmd = exec.Command("true")
	if err := g.PlaceCmd(cmd, "p1", "c1"); err == nil || cmd.SysProcAttr != nil {
		t.Fatalf("run open, fake unit: %v, %+v", err, cmd.SysProcAttr)
	}
	if err := g.PlaceCmd(exec.Command("true"), "other", "c1"); err == nil {
		t.Fatal("a foreign profile was placed")
	}
	g.Kernel = nil
	if err := g.PlaceCmd(exec.Command("true"), "p1", "c1"); err == nil {
		t.Fatal("placed with no kernel")
	}
}
