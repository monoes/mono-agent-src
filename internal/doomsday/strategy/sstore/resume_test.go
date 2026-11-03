package sstore

import (
	"testing"

	"github.com/monoes/mono-agent/internal/doomsday/strategy/types"
)

func TestResumeQueries(t *testing.T) {
	e := newEnv(t)
	if _, ok, err := e.s.LatestEpoch(ctx, e.prof, e.gen); ok || err != nil {
		t.Fatalf("empty: %v %v", ok, err)
	}
	if n, _ := e.s.NextEpochSeq(ctx, e.prof, e.gen); n != 1 {
		t.Fatalf("next %d", n)
	}
	for i, tr := range []types.Trigger{types.TriggerTimer, types.TriggerFinishParked, types.TriggerFinishParked} {
		if _, _, err := e.s.InsertEpoch(ctx, e.epoch(int64(i+1), tr)); err != nil {
			t.Fatal(err)
		}
	}
	l, ok, _ := e.s.LatestEpoch(ctx, e.prof, e.gen)
	if !ok || l.Seq != 3 {
		t.Fatalf("latest %+v", l)
	}
	if n, _ := e.s.NextEpochSeq(ctx, e.prof, e.gen); n != 4 {
		t.Fatalf("next %d", n)
	}
	if n, err := e.s.ParkedFinishRounds(ctx, e.prof, e.gen); err != nil || n != 2 {
		t.Fatalf("parked %d %v", n, err)
	}
	e.accept(t, "d1", "a")
	e.accept(t, "d2", "b")
	e.accept(t, "d3", "c")
	e.s.MoveDirection(ctx, e.prof, e.gen, "d2", types.DirPruned, "")
	e.s.MoveDirection(ctx, e.prof, e.gen, "d3", types.DirActive, "")
	e.s.MoveDirection(ctx, e.prof, e.gen, "d3", types.DirFinished, "")
	open, err := e.s.OpenDirections(ctx, e.prof, e.gen)
	if err != nil || len(open) != 1 || open[0].ID != "d1" {
		t.Fatalf("open %+v %v", open, err)
	}
}

func TestRestartReplayCreatesNoDuplicateEpochOrJudgment(t *testing.T) {
	e := newEnv(t)
	body := []byte(`{"criteria":[],"directions":[],"next_actions":[]}`)
	for i := 0; i < 2; i++ { // the second pass is the restarted engine repeating its steps
		if _, _, err := e.s.InsertEpoch(ctx, e.epoch(1, types.TriggerTimer)); err != nil {
			t.Fatal(err)
		}
		if _, err := e.s.RecordJudgment(ctx, e.prof, e.gen, 1, body); err != nil {
			t.Fatal(err)
		}
	}
	if n := e.count(t, `SELECT COUNT(*) FROM doomsday_strategy_epochs`); n != 1 {
		t.Fatalf("epochs %d", n)
	}
	if n := e.count(t, `SELECT COUNT(*) FROM doomsday_audit WHERE kind LIKE 'strategy.%'`); n != 2 {
		t.Fatalf("audit lines %d", n)
	}
	e.auditOK(t)
}
