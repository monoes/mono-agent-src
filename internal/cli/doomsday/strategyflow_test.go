//go:build doomsday

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

// A4/A5 at the host: whatever the judge says, a finish is requested only when every condition of the policy holds, and
// never by any of the others. The positive control proves the table can reach a request at all.
func TestHostRequestsAFinishOnlyWhenEveryConditionHolds(t *testing.T) {
	cases := []struct {
		name string
		deps func(*StrategyHostDeps)
		pre  func(*fenv)
		want int
	}{
		{"all conditions hold", nil, nil, 1},
		{"criterion only progressing", nil, func(e *fenv) { e.model.set(types.CritProgressing) }, 0},
		{"criterion regressed", nil, func(e *fenv) { e.model.set(types.CritRegressed) }, 0},
		{"no boss recorded", nil, func(e *fenv) { e.runs.set(func(r *StrategyRun) { r.HasBoss = false }) }, 0},
		{"no finish engine", func(d *StrategyHostDeps) { d.Finish = nil }, nil, 0},
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
			_, _ = e.host.Tick(bg) // errors are allowed here (an input with no criteria); only the call count matters
			e.clock.Advance(31 * time.Minute)
			_, _ = e.host.Tick(bg)
			if got := e.finish.n(); got != c.want {
				t.Fatalf("finish begin called %d times, want %d", got, c.want)
			}
		})
	}
}

// A refused finish is parked: no immediate retry on the same judgment, a new epoch brings a new request, and the engine's
// answer is what was recorded (the audit has the request BEFORE the park).
func TestHostParkedFinishWaitsForANewJudgment(t *testing.T) {
	e := newFenv(t, nil)
	e.model.set(types.CritPlausiblyMet)
	e.tickAfter(t, 31*time.Minute)
	if e.finish.n() != 1 {
		t.Fatalf("calls %d", e.finish.n())
	}
	tk := e.tickAfter(t, time.Minute)
	if e.finish.n() != 1 || tk.Decision == nil || tk.Decision.Action != "park" {
		t.Fatalf("calls %d decision %+v", e.finish.n(), tk.Decision)
	}
	e.tickAfter(t, 6*time.Minute)
	if e.finish.n() != 2 {
		t.Fatalf("a new judgment should bring a new request, calls %d", e.finish.n())
	}
	kinds := strings.Join(e.auditKinds(t), ",")
	if !strings.Contains(kinds, "strategy.finish.requested,strategy.finish.parked") {
		t.Fatalf("audit %s", kinds)
	}
	if err := e.core.VerifyAudit(bg, e.prof, e.gen); err != nil {
		t.Fatal(err)
	}
}

func TestHostAnAcceptedFinishIsNotRequestedAgain(t *testing.T) {
	e := newFenv(t, nil)
	e.finish.rep = BeginReport{Accepted: true}
	e.model.set(types.CritPlausiblyMet)
	e.tickAfter(t, 31*time.Minute)
	e.tickAfter(t, 31*time.Minute)
	if e.finish.n() != 1 {
		t.Fatalf("calls %d", e.finish.n())
	}
}

// A6: after any Stop the host starts no epoch, calls no judge and asks for no finish, whatever the clock says.
func TestHostIsInertAfterStop(t *testing.T) {
	e := newFenv(t, nil)
	e.model.set(types.CritPlausiblyMet)
	e.stopRun(t)
	epochs := e.count(t, `SELECT count(*) FROM doomsday_strategy_epochs`)
	tk := e.tickAfter(t, 3*time.Hour)
	if tk.Epoch == nil || tk.Epoch.Outcome != epoch.OutcomeInert {
		t.Fatalf("%+v", tk.Epoch)
	}
	if e.model.calls != 0 || e.finish.n() != 0 || e.count(t, `SELECT count(*) FROM doomsday_strategy_epochs`) != epochs {
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
