package ops

import (
	"context"
	"errors"
	"testing"
)

// fakeBosses records one boss session for every run.
type fakeBosses struct {
	id    int64
	roots map[int64]int64 // session -> top of its tree; absent: itself
}

func (f fakeBosses) BossOf(context.Context, Active) (int64, bool, error) { return f.id, f.id != 0, nil }

func (f fakeBosses) RootOf(_ context.Context, _ string, s int64) (int64, error) {
	if r, ok := f.roots[s]; ok {
		return r, nil
	}
	return s, nil
}

// A request names its own caller and addressee, so "caller equals addressed" is always true in production. What binds a
// call to the run is the boss recorded for the charter. Mutant tried: dropping requireBoss from Decide and BeginFinish
// makes the "another lead" rows pass through (red).
func TestOnlyTheRunsBossMayDecideOrBeginAFinish(t *testing.T) {
	for _, c := range []struct {
		name   string
		bosses Bosses
	}{
		{"another lead of the profile", fakeBosses{id: 9}},
		{"no boss recorded", fakeBosses{}},
		{"no boss source", nil},
	} {
		r := newRig(t)
		r.e.Bosses = c.bosses
		for _, op := range []string{OpPlanApprove, OpAskAnswer, OpSpendGrant} {
			q := base(op) // caller == addressed == 4, as the production route builds it
			q.Plan, q.Answer, q.Grant = &PlanArgs{Session: 9, Rev: 3, Digest: "d3"}, &AnswerArgs{AskID: 1, Class: "context"}, &GrantArgs{RequestID: 1, Target: "lead", AmountMicro: 1_000_000}
			if _, err := r.e.Decide(bg, q); !errors.Is(err, ErrCallerMismatch) {
				t.Errorf("%s: %s: %v", c.name, op, err)
			}
		}
		f := &fakeFinisher{res: FinishBeginResult{Accepted: true}}
		if _, err := r.e.BeginFinish(bg, finishReq(), f); !errors.Is(err, ErrCallerMismatch) || f.calls != 0 {
			t.Errorf("%s: finish.begin: %v calls=%d", c.name, err, f.calls)
		}
		if len(r.l.consumed) != 0 || len(r.a.rows) != 0 || len(r.pk.parks) != 0 {
			t.Errorf("%s: a refused caller wrote something", c.name)
		}
	}
}

// A lead inside the boss's tree is the boss's hand for plans, asks and grants addressed to it, but never for ending the run.
func TestALeadBelowTheBossAnswersButCannotBeginTheFinish(t *testing.T) {
	r := newRig(t)
	r.e.Bosses = fakeBosses{id: 9, roots: map[int64]int64{4: 9}}
	if _, err := r.e.Decide(bg, base(OpPlanApprove)); errors.Is(err, ErrCallerMismatch) {
		t.Fatalf("a lead below the boss was refused: %v", err)
	}
	f := &fakeFinisher{res: FinishBeginResult{Accepted: true}}
	if _, err := r.e.BeginFinish(bg, finishReq(), f); !errors.Is(err, ErrCallerMismatch) || f.calls != 0 {
		t.Fatalf("a lead below the boss began the finish: %v", err)
	}
}
