package doomsday

import (
	"context"
	"errors"
	"testing"

	"github.com/monoes/mono-agent/internal/doomsday/stop"
)

// ----- the wiring hands the session unit its leader ledger (the witness) through the Kernel -----

func TestWithLedgerGivesTheKernelTheWitnessAsItsLeaderLedger(t *testing.T) {
	r, _ := newWire(t, func() error { return nil }, fakeKernel{}, nil)
	r.start(t)
	w := r.w.WithLedger()
	led, ok := w.Kernel.(stop.LeaderLedger)
	if !ok {
		t.Fatal("the wired kernel carries no leader ledger, so a session unit could not be proven, placed into or adopted into")
	}
	if _, err := w.Kernel.BootID(); err != nil { // everything else is the platform kernel's
		t.Fatalf("the platform kernel is no longer reachable: %v", err)
	}
	l := stop.Leader{PID: 500, Stamp: "1.000001", Unit: 42}
	if err := led.AddLeader(3, l); err != nil {
		t.Fatal(err)
	}
	got, err := led.Leaders(3)
	if err != nil || len(got) != 1 || got[0].PID != 500 || got[0].Unit != 42 {
		t.Fatalf("the ledger did not go through the witness: %+v %v", got, err)
	}
	// it is the same witness the end proof reads
	seq, err := r.w.Sequencer(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if direct, err := seq.Leaders(3); err != nil || len(direct) != 1 {
		t.Fatalf("the witness does not hold the leader: %+v %v", direct, err)
	}
}

func TestWithLedgerLeavesNoKernelAloneAndBuildsNothingUntilUsed(t *testing.T) {
	if k := (Wiring{}).WithLedger().Kernel; k != nil {
		t.Fatalf("a wiring with no kernel got one: %T", k)
	}
	real := PinFunc
	pins := 0
	PinFunc = func(context.Context, stop.PinOptions) (*stop.Pinned, error) {
		pins++
		return nil, errors.New("counted")
	}
	defer func() { PinFunc = real }()
	r, _ := newWire(t, func() error { return nil }, fakeKernel{}, nil)
	w := r.w.WithLedger()
	if pins != 0 {
		t.Fatalf("building the ledger pinned the database %d times", pins)
	}
	if _, err := w.Kernel.(stop.LeaderLedger).Leaders(3); err == nil {
		t.Fatal("a ledger whose witness cannot be built answered with a list")
	}
	if pins == 0 {
		t.Fatal("the ledger never asked for the witness")
	}
}

// A wiring with no witness has a ledger that refuses, never one that reads as empty.
func TestWithLedgerWithoutAWitnessRefuses(t *testing.T) {
	r, _ := newWire(t, func() error { return nil }, fakeKernel{}, nil)
	w := r.w
	w.Witness = nil
	led := w.WithLedger().Kernel.(stop.LeaderLedger)
	if got, err := led.Leaders(3); err == nil {
		t.Fatalf("no witness, yet a list: %+v", got)
	}
	if err := led.AddLeader(3, stop.Leader{PID: 5, Stamp: "1.000001", Unit: 1}); err == nil {
		t.Fatal("a leader was recorded with no witness")
	}
	if err := led.RetireLeader(3, stop.Leader{PID: 5, Stamp: "1.000001", Unit: 1}); err == nil {
		t.Fatal("a retire line was written with no witness")
	}
}

// UnitEnded asks the witness whether the unit's end line is written.
func TestWiringUnitEndedReadsTheWitness(t *testing.T) {
	r, _ := newWire(t, func() error { return nil }, fakeKernel{}, nil)
	r.start(t)
	seq, err := r.w.Sequencer(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	rec := stop.UnitRecord{Kind: stop.KindSession, Generation: 3, CgroupID: 42, Name: "n"}
	if err := seq.Bound(rec); err != nil {
		t.Fatal(err)
	}
	if ended, err := r.w.UnitEnded(3, 42); err != nil || ended {
		t.Fatalf("before: %v %v", ended, err)
	}
	if err := seq.UnitEnded(rec, "best-effort"); err != nil {
		t.Fatal(err)
	}
	if ended, err := r.w.UnitEnded(3, 42); err != nil || !ended {
		t.Fatalf("after: %v %v", ended, err)
	}
	w := r.w
	w.Witness = nil
	if ended, err := w.UnitEnded(3, 42); err == nil || ended {
		t.Fatalf("no witness: %v %v", ended, err)
	}
}
