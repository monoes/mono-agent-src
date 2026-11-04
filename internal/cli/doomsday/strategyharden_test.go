//go:build doomsday

package doomsday

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/monoes/mono-agent/internal/doomsday/strategy/judge"
	"github.com/monoes/mono-agent/internal/doomsday/strategy/sstore"
	"github.com/monoes/mono-agent/internal/doomsday/strategy/types"
	"github.com/monoes/mono-agent/internal/monomind"
)

// A tampered direction table is not "corpus unavailable": the proposal is refused with the tamper error, no unchecked
// acceptance and no new direction are written, and the park is recorded.
func TestATamperedDirectionTableRefusesTheProposalInsteadOfAcceptingItUnchecked(t *testing.T) {
	r := newStratRig(t, finishCharterJSON, nil)
	if res, err := r.propose(t, "first direction"); err != nil || !res.Accepted {
		t.Fatalf("%+v %v", res, err)
	}
	for _, q := range []string{`DROP TRIGGER doomsday_strategy_directions_forward_only`, `UPDATE doomsday_strategy_directions SET title = 'evil'`} {
		if _, err := r.w.DB.Exec(q); err != nil {
			t.Fatal(err)
		}
	}
	dirs, novelty := r.count(t, `SELECT COUNT(*) FROM doomsday_strategy_directions`), r.count(t, `SELECT COUNT(*) FROM doomsday_strategy_novelty`)
	res, err := r.propose(t, "a second, unrelated direction")
	if err == nil || !errors.Is(err, sstore.ErrTampered) || res.Accepted {
		t.Fatalf("the proposal over a tampered table: %+v %v", res, err)
	}
	if r.count(t, `SELECT COUNT(*) FROM doomsday_strategy_directions`) != dirs || r.count(t, `SELECT COUNT(*) FROM doomsday_strategy_novelty WHERE outcome = 'accepted-unchecked'`) != 0 || r.count(t, `SELECT COUNT(*) FROM doomsday_strategy_novelty`) != novelty {
		t.Fatal("a tampered table still took a proposal in")
	}
	if r.count(t, `SELECT COUNT(*) FROM doomsday_audit WHERE kind = ?`, types.KindParked) == 0 {
		t.Fatal("the park was not recorded")
	}
}

// The model adapter reads the runner's reply under the judgment cap: a reply that streams past it is cancelled while it
// streams and refused, not held whole; one exactly at the cap goes through.
func TestTheRunnerJudgeModelBoundsTheReplyWhileItStreams(t *testing.T) {
	var sawCancel bool
	m := runnerJudgeModel{pin: StrategyPin{"p", "m", "1"}, timeout: 1, budgetUSD: 0.1, workspace: func() (string, error) { return t.TempDir(), nil },
		exec: func(ctx context.Context, _ monomind.ExecOptions, on func(monomind.Event)) (*monomind.TurnResult, error) {
			chunk := strings.Repeat("x", 64*1024)
			for i := 0; i < 64; i++ { // 4 MiB offered
				on(monomind.Event{Type: "assistant", Text: chunk})
				if ctx.Err() != nil {
					sawCancel = true
					return nil, ctx.Err()
				}
			}
			return &monomind.TurnResult{SawDone: true, ResultText: strings.Repeat("x", 4<<20)}, nil
		}}
	_, err := m.Complete(context.Background(), judge.Request{System: "s", User: "u", MaxBytes: types.MaxJudgmentBytes})
	if !errors.Is(err, judge.ErrReplyTooLong) || !sawCancel {
		t.Fatalf("err %v, cancelled while streaming: %v", err, sawCancel)
	}
	// a final text over the cap with no stream events is refused before it is copied
	m.exec = func(context.Context, monomind.ExecOptions, func(monomind.Event)) (*monomind.TurnResult, error) {
		return &monomind.TurnResult{SawDone: true, ResultText: strings.Repeat("x", types.MaxJudgmentBytes+1)}, nil
	}
	if _, err := m.Complete(context.Background(), judge.Request{}); !errors.Is(err, judge.ErrReplyTooLong) {
		t.Fatalf("over the cap: %v", err)
	}
	m.exec = func(context.Context, monomind.ExecOptions, func(monomind.Event)) (*monomind.TurnResult, error) {
		return &monomind.TurnResult{SawDone: true, ResultText: strings.Repeat("x", types.MaxJudgmentBytes)}, nil
	}
	if b, err := m.Complete(context.Background(), judge.Request{}); err != nil || len(b) != types.MaxJudgmentBytes {
		t.Fatalf("at the cap: %d %v", len(b), err)
	}
}

// The judge is the charter's boss pin run through the product's runner with one turn and no tools; it can never be the
// finish verifier's pin (the independence rule), and a run with no pin has no judge.
func TestStrategyJudgeModelIsTheBossPinAndNeverTheVerifier(t *testing.T) {
	var got monomind.ExecOptions
	x := StrategyExtras{JudgeExec: func(_ context.Context, o monomind.ExecOptions, _ func(monomind.Event)) (*monomind.TurnResult, error) {
		got = o
		return &monomind.TurnResult{SawDone: true, ResultText: "{}"}, nil
	}}
	r := newStratRig(t, finishCharterJSON, nil)
	d, err := r.w.StrategyHostDeps(bg, x)
	if err != nil {
		t.Fatal(err)
	}
	run, ok, err := d.Runs.Current(bg)
	if err != nil || !ok {
		t.Fatal(ok, err)
	}
	m := d.ModelFor(run)
	if m == nil || m.Pin() != "p/m@1" || run.Verifier.String() != "p/v@2" {
		t.Fatalf("pin %v, verifier %v", m, run.Verifier)
	}
	if _, err := m.Complete(bg, judgeRequest("sys", "user")); err != nil {
		t.Fatal(err)
	}
	if got.Runtime != "p" || got.Model != "m" || got.MaxTurns != 1 || got.Sandbox != monomind.TurnSandboxMode || got.Prompt != "user" || got.SystemPrompt != "sys" {
		t.Fatalf("%+v", got)
	}
	refuse := d.RefuseFor(run)
	if refuse(run.Judge.String()) || !refuse(run.Verifier.String()) {
		t.Fatal("the independence rule does not separate the judge from the verifier")
	}
	if d.ModelFor(StrategyRun{}) != nil {
		t.Fatal("a run with no pin got a judge")
	}
	if !d.RefuseFor(StrategyRun{})("anything") {
		t.Fatal("a run with no verifier pin must refuse every judge")
	}
}

func judgeRequest(system, user string) judge.Request {
	return judge.Request{System: system, User: user, MaxBytes: 1024}
}
