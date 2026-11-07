package strategy

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/monoes/mono-agent/internal/doomsday/strategy/epoch"
	"github.com/monoes/mono-agent/internal/doomsday/strategy/judge"
	"github.com/monoes/mono-agent/internal/doomsday/strategy/types"
)

type fakeJudge struct{ calls int }

func (j *fakeJudge) CheckPin() (string, error) { return "fake/judge/1", nil }

func (j *fakeJudge) Run(_ context.Context, in judge.Input) (judge.Result, error) {
	j.calls++
	var jd types.Judgment
	for _, c := range in.Criteria {
		jd.Criteria = append(jd.Criteria, types.CriterionProgress{ID: c.ID, Status: types.CritProgressing})
	}
	for _, d := range in.Directions {
		jd.Directions = append(jd.Directions, types.DirectionAssessment{ID: d.ID, Verdict: types.VerdictKeep})
	}
	body, _ := json.Marshal(jd)
	return judge.Result{Judgment: jd, Body: body, BodyHash: types.BodyHash(body), ModelPin: "fake/judge/1"}, nil
}

type fakeInputs struct{}

func (fakeInputs) Snapshot(context.Context) (epoch.Snapshot, error) {
	return epoch.Snapshot{Criteria: []judge.CriterionInput{{ID: "c1", Text: "one"}}}, nil
}

// The real epoch engine over the real tables: a timer epoch is opened, judged, applied and stored; a pause stops it
// writing; a stop closes an unfinished epoch as abandoned; a restart (a new engine, a new store) resumes it.
func TestTheRealEpochEngineRunsOverTheStore(t *testing.T) {
	f := newFixture(t)
	clk := epoch.NewFakeClock(time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC))
	jd := &fakeJudge{}
	newEngine := func() *epoch.Engine {
		e, err := epoch.New(epoch.Config{Profile: f.profile, Generation: 1}, epoch.Deps{
			Store: NewStore(f.db), Judge: jd, Inputs: fakeInputs{}, Run: f.store.RunOf(f.session), Clock: clk})
		if err != nil {
			t.Fatal(err)
		}
		return e
	}
	eng := newEngine()
	if r, err := eng.Tick(bg); err != nil || r.Outcome != epoch.OutcomeIdle {
		t.Fatalf("at start: %+v %v", r, err)
	}
	clk.Advance(30 * time.Minute)
	r, err := eng.Tick(bg)
	if err != nil || r.Outcome != epoch.OutcomeApplied || r.EpochSeq != 1 || jd.calls != 1 {
		t.Fatalf("first epoch: %+v %v calls=%d", r, err, jd.calls)
	}
	got, err := f.store.GetEpoch(bg, f.profile, 1, 1)
	if err != nil || got.State != types.EpochApplied || !got.HasJudgment || got.ModelPin != "fake/judge/1" {
		t.Fatalf("stored epoch: %+v %v", got, err)
	}
	if rec, err := f.store.GetJudgment(bg, f.profile, 1, 1); err != nil || rec.BodyHash != got.JudgmentHash {
		t.Fatalf("stored judgment: %v", err)
	}

	// a pause: the engine does nothing and writes nothing
	f.setState(t, "paused")
	clk.Advance(time.Hour)
	if r, err := eng.Tick(bg); err != nil || r.Outcome != epoch.OutcomeInert || r.Held != epoch.HeldNotRunning {
		t.Fatalf("while paused: %+v %v", r, err)
	}
	if n := count(t, f.db, `SELECT COUNT(*) FROM mission_strategy_epochs`); n != 1 || jd.calls != 1 {
		t.Fatalf("a paused session got %d epochs / %d judge calls", n, jd.calls)
	}

	// running again: an epoch is opened, the host dies before it is judged (an open row), the restart resumes the same one
	f.setState(t, "running")
	f.openEpoch(t, 2)
	clk.Advance(time.Minute)
	r, err = newEngine().Tick(bg)
	if err != nil || r.Outcome != epoch.OutcomeApplied || r.EpochSeq != 2 || !r.Resumed {
		t.Fatalf("restart: %+v %v", r, err)
	}
	if n := count(t, f.db, `SELECT COUNT(*) FROM mission_strategy_epochs`); n != 2 {
		t.Fatalf("%d epochs after the resume, want 2", n)
	}

	// a stop with an unfinished epoch: the engine closes it as abandoned
	clk.Advance(time.Hour)
	f.openEpoch(t, 3)
	f.setState(t, "stopped")
	r, err = eng.Tick(bg)
	if err != nil || !r.Abandoned || r.EpochSeq != 3 {
		t.Fatalf("after the stop: %+v %v", r, err)
	}
	if e, _ := f.store.GetEpoch(bg, f.profile, 1, 3); e.State != types.EpochAbandoned {
		t.Fatalf("epoch 3 = %+v", e)
	}
}
