package doomsday

import (
	"context"
	"strings"
	"testing"

	"github.com/monoes/mono-agent/internal/doomsday/stop"
	"github.com/monoes/mono-agent/internal/doomsday/store"
)

// The verification unit of a finish round is witnessed like the run's own unit and is a registry member of kind `verify`
// (note 13.6 G3), against the real store and the real witness; the kernel and the service manager are the fake world of
// wire_test.go. Placement into the ticket cgroup needs a real cgroup and is tested by the real-cgroup tests of the stop and
// finish packages.

func unitLines(t *testing.T, w *wireRig, kind string) int {
	t.Helper()
	recs, err := witnessOf(w).Records()
	if err != nil {
		t.Fatal(err)
	}
	n := 0
	for _, r := range recs {
		if r.T == kind {
			n++
		}
	}
	return n
}

func finishedBegun(t *testing.T) (*finRig, *FinishEngine) {
	t.Helper()
	r := newFinRig(t, finishCharterJSON)
	r.start(t)
	r.mustDo(t, "finish", "--begin")
	e := r.w.FinishEngine() // no SandboxFor: the real construction of the verification unit
	return r, e
}

func TestTheVerificationUnitIsWitnessedAndARegisteredMember(t *testing.T) {
	r, e := finishedBegun(t)
	ctx := context.Background()
	intent0, bound0, ended0 := unitLines(t, r.wireRig, stop.RecUnitIntent), unitLines(t, r.wireRig, stop.RecUnitBound), unitLines(t, r.wireRig, stop.RecUnitEnded)
	if intent0 != 1 || bound0 != 1 || ended0 != 1 {
		t.Fatalf("the run's own unit: intent=%d bound=%d ended=%d", intent0, bound0, ended0)
	}
	sb, end, err := e.sandbox(1)(ctx, 1)
	if err != nil {
		t.Fatal(err)
	}
	if sb.Place == nil || sb.Scratch == "" {
		t.Fatalf("no placer or scratch: %+v", sb)
	}
	if unitLines(t, r.wireRig, stop.RecUnitIntent) != 2 || unitLines(t, r.wireRig, stop.RecUnitBound) != 2 { // the run's and this one's
		t.Fatalf("the verification unit's intent and binding are not in the witness: %v", recTypes(t, r.wireRig))
	}
	ms, err := r.w.Store.LiveMembers(ctx, "p1", 1)
	if err != nil || len(ms) != 1 || ms[0].Kind != store.KindVerify || ms[0].Pid == 0 || !strings.HasPrefix(ms[0].ID, "monoagent-run-1-") {
		t.Fatalf("members = %+v %v", ms, err)
	}
	// a live verification unit blocks a new run of the profile (13.3 P1), whatever else is true
	if _, err := run(t, r.deps, "", "start", "--charter", r.charter, "--yes-i-lift", "--hash", fullHash(t, r.rig, r.charter)); err == nil {
		t.Fatal("a run started while a verification unit is a live member")
	}
	end()
	if unitLines(t, r.wireRig, stop.RecUnitEnded) != 2 {
		t.Fatalf("the unit's end is not in the witness: %v", recTypes(t, r.wireRig))
	}
	if ms, _ := r.w.Store.LiveMembers(ctx, "p1", 1); len(ms) != 0 {
		t.Fatalf("the member was not released on the proof of the unit's end: %+v", ms)
	}
	if len(e.notes) != 0 {
		t.Fatalf("notes: %v", e.notes)
	}
}

// A unit that is not proven gone stays a live member and writes no end line: nothing claims what was not proven.
// Mutant tried: release the member whatever the outcome: this test is red.
func TestAVerificationUnitThatIsNotProvenGoneStaysLiveAndUnended(t *testing.T) {
	r, e := finishedBegun(t)
	ctx := context.Background()
	_, end, err := e.sandbox(1)(ctx, 1)
	if err != nil {
		t.Fatal(err)
	}
	for k := range r.world.ids { // the cgroup at the recorded path is not the one recorded: the identity proof refuses it
		r.world.ids[k] += 100
	}
	end()
	if len(e.notes) == 0 {
		t.Fatal("an unproven end was not reported")
	}
	if unitLines(t, r.wireRig, stop.RecUnitEnded) != 1 { // the run's own only
		t.Fatalf("an end line was written for a unit that is not proven gone: %v", recTypes(t, r.wireRig))
	}
	if ms, _ := r.w.Store.LiveMembers(ctx, "p1", 1); len(ms) != 1 || ms[0].Kind != store.KindVerify {
		t.Fatalf("the member of an unproven unit was released: %+v", ms)
	}
}

// With no witness no unit is made: the unit must be recorded, so nothing starts without one.
func TestNoVerificationUnitWithoutAWitness(t *testing.T) {
	r, e := finishedBegun(t)
	e.W.Witness = nil
	if _, _, err := e.sandbox(1)(context.Background(), 1); err == nil {
		t.Fatal("a verification unit was made with no witness")
	}
	if len(r.world.started) != 1 {
		t.Fatalf("a service was started for it: %v", r.world.started)
	}
}

// The registration is the store's own conditional insert: only a finish at quiescent, only kind verify.
func TestRegisterVerifyOnlyForAFinishAtQuiescent(t *testing.T) {
	w, _ := newWire(t, func() error { return nil }, fakeKernel{}, nil)
	w.start(t) // running, not a finish
	err := w.w.Store.RegisterVerify(context.Background(), store.Member{Profile: "p1", Generation: 1, ID: "u", Kind: "session", Pid: 1})
	if err == nil {
		t.Fatal("a verification member was registered for a running run")
	}
	if ms, _ := w.w.Store.LiveMembers(context.Background(), "p1", 1); len(ms) != 0 {
		t.Fatalf("%+v", ms)
	}
}
