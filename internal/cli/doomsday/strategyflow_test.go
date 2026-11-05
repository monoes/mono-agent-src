package doomsday

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/monoes/mono-agent/internal/doomsday/ops"
	"github.com/monoes/mono-agent/internal/doomsday/store"
	"github.com/monoes/mono-agent/internal/doomsday/strategy/epoch"
	"github.com/monoes/mono-agent/internal/doomsday/strategy/sstore"
	"github.com/monoes/mono-agent/internal/doomsday/strategy/types"
)

func (e *fenv) propose(t *testing.T, title string) ops.ProposeOutcome {
	t.Helper()
	out, err := e.host.Sink().Propose(bg, ops.StrategyProposal{Profile: e.prof, Generation: e.gen,
		Proposal: types.Proposal{Title: title, Rationale: "because", ProposerSession: "11"}})
	if err != nil {
		t.Fatalf("propose %q: %v", title, err)
	}
	return out
}

func (e *fenv) markers(t *testing.T) int {
	return e.count(t, `SELECT count(*) FROM doomsday_audit WHERE profile=? AND generation=? AND kind=?`, e.prof, e.gen, types.KindFinishReady)
}

// A4/A5 at the host: whatever the judge says, the ready marker is written only when every condition of the policy holds,
// and never by any of the others. The positive control proves the table can reach a marker at all. Nothing here can begin
// a finish: the host has no finish engine to call.
func TestHostMarksReadyOnlyWhenEveryConditionHolds(t *testing.T) {
	cases := []struct {
		name string
		deps func(*StrategyHostDeps)
		pre  func(*fenv)
		want int
	}{
		{"all conditions hold", nil, nil, 1},
		{"no boss recorded does not matter: the boss is not who is asked", nil, func(e *fenv) { e.runs.set(func(r *StrategyRun) { r.HasBoss = false }) }, 1},
		{"criterion only progressing", nil, func(e *fenv) { e.model.set(types.CritProgressing) }, 0},
		{"criterion regressed", nil, func(e *fenv) { e.model.set(types.CritRegressed) }, 0},
		{"subject time unknown", func(d *StrategyHostDeps) { d.SubjectChangedAt = nil }, nil, 0},
		{"subject changed after the judgment", nil, func(e *fenv) { e.subj = e.clock.Now().Add(40 * time.Minute) }, 0},
		{"charter without criteria", nil, func(e *fenv) { e.runs.set(func(r *StrategyRun) { r.Criteria = nil }) }, 0},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			e := newFenv(t, c.deps)
			e.model.set(types.CritPlausiblyMet)
			if c.pre != nil {
				c.pre(e)
			}
			_, _ = e.host.Tick(bg) // errors are allowed here (an input with no criteria); only the markers matter
			e.clock.Advance(31 * time.Minute)
			_, _ = e.host.Tick(bg)
			if got := e.markers(t); got != c.want {
				t.Fatalf("%d ready markers, want %d", got, c.want)
			}
			if e.count(t, `SELECT count(*) FROM doomsday_audit WHERE kind LIKE 'finish.%' OR kind = 'denied-attempt'`) != 0 {
				t.Fatal("a finish row exists: the host asked the finish engine for something")
			}
		})
	}
}

// A finish that is already under way (the finish engine froze the subject) is never marked ready again.
func TestHostWritesNoMarkerOnceAFinishIsUnderWay(t *testing.T) {
	e := newFenv(t, nil)
	e.model.set(types.CritPlausiblyMet)
	// the chain is not verified here, only counted: a freeze row is what the finish engine writes when it has begun
	if _, err := e.db.Exec(`INSERT INTO doomsday_audit(profile, generation, seq, kind, payload, prev, mac) VALUES (?, ?, 9999, 'finish.freeze', '{}', x'', x'')`, e.prof, e.gen); err != nil {
		t.Fatal(err)
	}
	e.tickAfter(t, 31*time.Minute)
	if e.markers(t) != 0 {
		t.Fatal("a marker was written for a run whose finish is under way")
	}
}

// A refused begin of the boss is a parked round in the finish engine's own record: the next epoch is opened for it, that
// epoch's judgment is not newer than the park, so no marker is written for it; a later judgment is marked again.
func TestHostParkedFinishWaitsForANewJudgment(t *testing.T) {
	e := newFenv(t, nil)
	e.model.set(types.CritPlausiblyMet)
	e.tickAfter(t, 31*time.Minute)
	if e.markers(t) != 1 {
		t.Fatalf("markers %d", e.markers(t))
	}
	if _, err := e.core.AppendAudit(bg, e.prof, e.gen, KindFinishDenied, "a person item is open"); err != nil {
		t.Fatal(err)
	}
	tk := e.tickAfter(t, 6*time.Minute)
	if tk.Epoch == nil || tk.Epoch.Trigger != types.TriggerFinishParked {
		t.Fatalf("the park did not open an epoch: %+v", tk.Epoch)
	}
	if tk.Decision == nil || tk.Decision.Action != "park" || e.markers(t) != 1 {
		t.Fatalf("decision %+v markers %d", tk.Decision, e.markers(t))
	}
	e.tickAfter(t, 31*time.Minute)
	if e.markers(t) != 2 {
		t.Fatalf("a newer judgment should be marked again, markers %d", e.markers(t))
	}
	if err := e.core.VerifyAudit(bg, e.prof, e.gen); err != nil {
		t.Fatal(err)
	}
}

// A6: after any Stop the host starts no epoch, calls no judge and writes no marker, whatever the clock says.
func TestHostIsInertAfterStop(t *testing.T) {
	e := newFenv(t, nil)
	e.model.set(types.CritPlausiblyMet)
	e.stopRun(t)
	epochs := e.count(t, `SELECT count(*) FROM doomsday_strategy_epochs`)
	tk := e.tickAfter(t, 3*time.Hour)
	if tk.Epoch == nil || tk.Epoch.Outcome != epoch.OutcomeInert {
		t.Fatalf("%+v", tk.Epoch)
	}
	if e.model.calls != 0 || e.markers(t) != 0 || e.count(t, `SELECT count(*) FROM doomsday_strategy_epochs`) != epochs {
		t.Fatal("the host acted after Stop")
	}
	if _, err := e.host.Sink().Propose(bg, ops.StrategyProposal{Profile: e.prof, Generation: e.gen,
		Proposal: types.Proposal{Title: "late idea", ProposerSession: "11"}}); err == nil {
		t.Fatal("a proposal was accepted after Stop")
	}
}

func TestSinkProposeStoresRefusesDuplicatesAndChains(t *testing.T) {
	e := newFenv(t, nil)
	first := e.propose(t, "Build the importer")
	if !first.Accepted || first.DirectionID == "" || first.Outcome != types.NoveltyAccepted {
		t.Fatalf("%+v", first)
	}
	dup := e.propose(t, "build the  importer")
	if dup.Accepted || dup.MatchedID == "" {
		t.Fatalf("a duplicate was accepted or did not cite a row: %+v", dup)
	}
	if err := e.core.VerifyAudit(bg, e.prof, e.gen); err != nil {
		t.Fatal(err)
	}
	if e.count(t, `SELECT count(*) FROM doomsday_strategy_directions`) != 1 {
		t.Fatal("the duplicate created a direction")
	}
}

// Q4: with no similarity source at most one proposal per epoch is accepted unchecked.
func TestSinkUncheckedAllowanceIsOnePerEpoch(t *testing.T) {
	e := newFenv(t, func(d *StrategyHostDeps) { d.Similar = nil })
	a := e.propose(t, "first idea")
	b := e.propose(t, "second idea")
	if a.Outcome != types.NoveltyAcceptedUnchecked || !a.Accepted {
		t.Fatalf("%+v", a)
	}
	if b.Accepted || b.Outcome != types.NoveltyRefusedCorpus {
		t.Fatalf("%+v", b)
	}
}

func TestSinkRefusesAnotherProfileAndExpectsTheCharterIDs(t *testing.T) {
	e := newFenv(t, nil)
	if _, err := e.host.Sink().Propose(bg, ops.StrategyProposal{Profile: "other", Generation: e.gen,
		Proposal: types.Proposal{Title: "x", ProposerSession: "1"}}); !errors.Is(err, sstore.ErrRunNotRunning) {
		t.Fatalf("%v", err)
	}
	d := e.propose(t, "an idea")
	exp, err := e.host.Sink().EpochExpect(bg, e.prof, e.gen, 1)
	if err != nil || len(exp.CriterionIDs) != 1 || exp.CriterionIDs[0] != store.CriterionID(0) || len(exp.DirectionIDs) != 1 || exp.DirectionIDs[0] != d.DirectionID {
		t.Fatalf("%+v %v", exp, err)
	}
	if err := e.host.Sink().MoveDirection(bg, e.prof, e.gen, d.DirectionID, types.DirFinished); !errors.Is(err, sstore.ErrBackwardMove) {
		t.Fatalf("a proposed direction cannot finish: %v", err)
	}
}

// A8: a stored row that fails its MAC parks the strategy for the generation; the run itself stays running.
func TestHostTamperedRowParksTheStrategyNotTheRun(t *testing.T) {
	e := newFenv(t, nil)
	e.tickAfter(t, 31*time.Minute)
	// An attacker editing the file removes the trigger first; the MAC is what still catches the edit.
	for _, q := range []string{`DROP TRIGGER doomsday_strategy_epochs_forward_only`, `UPDATE doomsday_strategy_epochs SET model_pin = 'evil'`} {
		if _, err := e.db.Exec(q); err != nil {
			t.Fatal(err)
		}
	}
	e.clock.Advance(31 * time.Minute)
	if _, err := e.host.Tick(bg); !errors.Is(err, sstore.ErrTampered) {
		t.Fatalf("err %v", err)
	}
	calls := e.model.calls
	tk, err := e.host.Tick(bg)
	if err != nil || tk.Idle != "parked" || e.model.calls != calls {
		t.Fatalf("%+v %v", tk, err)
	}
	run, err := e.core.GetRun(bg, e.prof, e.gen)
	if err != nil || run.State != store.StateRunning {
		t.Fatalf("the run was touched: %+v %v", run, err)
	}
	if !strings.Contains(strings.Join(e.auditKinds(t), ","), types.KindParked) {
		t.Fatal("the park was not recorded")
	}
}

func TestStoreRunSourceReadsTheOpenRunAndItsCriteria(t *testing.T) {
	e := newFenv(t, nil)
	src := StoreRunSource{Profile: e.prof, Runs: e.core, Bosses: bossStub{id: 9}}
	run, ok, err := src.Current(bg)
	if err != nil || !ok || run.Generation != e.gen || !run.Running || !run.HasBoss || run.Boss != 9 {
		t.Fatalf("%+v %v %v", run, ok, err)
	}
	if got := run.CriterionIDs(); len(got) != 1 || got[0] != store.CriterionID(0) || len(run.Hash) != 64 {
		t.Fatalf("%+v", run)
	}
	e.stopRun(t)
	run, ok, err = src.Current(bg)
	if err != nil || !ok || run.Running || run.HasBoss {
		t.Fatalf("a run on its way out must not be running or have a boss: %+v", run)
	}
}

type bossStub struct{ id int64 }

func (b bossStub) BossOf(_ context.Context, _ ops.Active) (int64, bool, error) {
	return b.id, true, nil
}

func (b bossStub) RootOf(context.Context, string, int64) (int64, error) { return b.id, nil }
