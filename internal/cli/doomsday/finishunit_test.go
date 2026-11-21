package doomsday

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/monoes/mono-agent/internal/doomsday/finish"
	"github.com/monoes/mono-agent/internal/doomsday/ops"
	"github.com/monoes/mono-agent/internal/doomsday/stop"
	"github.com/monoes/mono-agent/internal/doomsday/store"
)

// The limits and the network requirement reach the sandbox of the round (the real construction), and the verb's flag sets
// the requirement.
func TestTheSandboxCarriesTheLimitsAndTheNetworkRequirement(t *testing.T) {
	_, e := finishedBegun(t)
	e.Limits = finish.Limits{CPUSeconds: 9, ScratchBytes: 1 << 20}
	e.RequireNetDeny = true
	sb, end, err := e.sandbox(1)(context.Background(), 1)
	if err != nil {
		t.Fatal(err)
	}
	defer end()
	if sb.Limits.CPUSeconds != 9 || sb.Limits.ScratchBytes != 1<<20 || !sb.RequireNetDeny {
		t.Fatalf("%+v", sb)
	}
}

func TestTheFinishVerbsFlagRequiresTheNetworkDeny(t *testing.T) {
	r := newFinRig(t, finishCharterJSON)
	r.start(t)
	r.mustDo(t, "finish", "--begin")
	r.mustDo(t, "finish", "--subject", r.subject)
	if r.fe.RequireNetDeny {
		t.Fatal("the requirement is on without the flag")
	}
	r.mustDo(t, "finish", "--require-net-deny")
	if !r.fe.RequireNetDeny {
		t.Fatal("--require-net-deny did not reach the engine")
	}
}

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

// The window between the unit's creation and its member row (a host that dies there): the unit is witnessed as bound and
// has no end line, so it is detectable by the witness alone (BoundUnits), and the finish's stop path ends it by its
// record and writes the end line, whether or not a member row ever existed. The member row is not what makes it
// findable; it is what blocks a new run while the unit lives, and that gap is closed by the witness, not the registry.
func TestAVerificationUnitWithoutItsMemberRowIsFoundAndEndedByItsRecord(t *testing.T) {
	r, e := finishedBegun(t)
	ctx := context.Background()
	w := e.W
	seq, err := w.Sequencer(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if n := len(seq.BoundUnits(1)); n != 0 { // the run's own unit already ended
		t.Fatalf("%d units are bound before the round", n)
	}
	dir := e.unitDir(1, 1)
	// the sandbox's first half, then "the host died": no RegisterVerify
	if _, err := (stop.UnitCreator{K: w.Kernel, Dir: dir, Host: w.Host, Journal: seq}).Create(ctx, 1); err != nil {
		t.Fatal(err)
	}
	if ms, _ := r.w.Store.LiveMembers(ctx, "p1", 1); len(ms) != 0 {
		t.Fatalf("a member exists: %+v", ms)
	}
	if n := len(seq.BoundUnits(1)); n != 1 {
		t.Fatalf("the unit with no member row is not detectable through the witness: %d", n)
	}
	if left := e.endAllUnits(1); len(left) != 0 {
		t.Fatalf("the unit was not ended: %v", left)
	}
	if n := len(seq.BoundUnits(1)); n != 0 || unitLines(t, r.wireRig, stop.RecUnitEnded) != 2 {
		t.Fatalf("bound=%d, lines=%v", n, recTypes(t, r.wireRig))
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

// finish.begin as the boss's typed operation, against the real store and witness: with no running run it is refused before
// anything is written; with one it begins the finish (the Stop path with cause=finish), and a charter that cannot finish
// is refused by the finish's own preconditions with a denied-attempt row.
func TestBossFinishBeginBeginsTheFinishOnlyOnARunningRun(t *testing.T) {
	r := newFinRig(t, finishCharterJSON)
	ctx := context.Background()
	if _, err := r.w.BossFinishBegin(ctx, 5); err == nil || !errors.Is(err, ops.ErrNoCharter) {
		t.Fatalf("no run: %v", err)
	}
	top := r.topLead(t, "conv-boss")
	r.start(t)
	if err := r.boss(t, top); err != nil {
		t.Fatal(err)
	}
	if _, err := r.w.BossFinishBegin(ctx, 0); !errors.Is(err, ops.ErrCallerMismatch) {
		t.Fatalf("no caller: %v", err)
	}
	if runs, _ := r.w.Store.OpenRuns(ctx, "p1"); len(runs) != 1 || runs[0].State != store.StateRunning {
		t.Fatalf("a refused call changed the run: %+v", runs)
	}
	res, err := r.w.BossFinishBegin(ctx, top)
	if err != nil || !res.Accepted || res.Generation != 1 {
		t.Fatalf("%+v %v", res, err)
	}
	if run, _ := r.w.Store.GetRun(ctx, "p1", 1); run.State != store.StateQuiescent || run.Cause != store.FinishCause {
		t.Fatalf("%+v", run)
	}
	if _, err := r.w.BossFinishBegin(ctx, top); !errors.Is(err, ops.ErrNoCharter) {
		t.Fatalf("a second begin on a run that is no longer running: %v", err)
	}
}

func TestBossFinishBeginOnACharterThatCannotFinishIsDeniedAndRecorded(t *testing.T) {
	r := newFinRig(t, strings.Replace(finishCharterJSON, `"criteria":[{"kind":"command","command":"echo hello","expected":"hello"},{"kind":"rubric","text":"the docs read well"}],`, "", 1))
	top := r.topLead(t, "conv-boss")
	r.start(t)
	if err := r.boss(t, top); err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	res, err := r.w.BossFinishBegin(ctx, top)
	if err != nil || res.Accepted || res.Refused == "" {
		t.Fatalf("%+v %v", res, err)
	}
	if runs, _ := r.w.Store.OpenRuns(ctx, "p1"); len(runs) != 1 || runs[0].State != store.StateRunning {
		t.Fatalf("the run changed: %+v", runs)
	}
	if n := count2(t, r, `SELECT COUNT(*) FROM doomsday_audit WHERE kind = 'denied-attempt'`); n != 1 {
		t.Fatalf("denied-attempt rows = %d", n)
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

// A kernel with no service manager is refused where the unit kind needs one (every kind but the macOS ones): the refusal comes from
// the unit's own creation, so a platform whose units need none (macOS, Windows) is no longer turned away by the sandbox first.
func TestNoVerificationUnitWhereTheKindNeedsAServiceManagerAndNoneIsWired(t *testing.T) {
	r, e := finishedBegun(t)
	e.W.Host = nil
	_, _, err := e.sandbox(1)(context.Background(), 1)
	if err == nil || !strings.Contains(err.Error(), "service manager") {
		t.Fatalf("a cgroup verification unit was made (or refused for another reason) with no service manager: %v", err)
	}
	if len(r.world.started) != 1 {
		t.Fatalf("a service was started for it: %v", r.world.started)
	}
}
