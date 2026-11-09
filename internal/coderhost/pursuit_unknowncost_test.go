package coderhost

import (
	"context"
	"math"
	"testing"
	"time"

	"github.com/monoes/mono-agent/internal/doomsday/strategy/judge"
	"github.com/monoes/mono-agent/internal/missionpursuit/judgemodel"
	"github.com/monoes/mono-agent/internal/monomind"
)

// What a cost the runtime does not report, or reports as nonsense, is charged. No spend, no network: a fake runner that
// completes its turn is driven through the REAL judge adapter, the real meter and ledger (judge calls), and the real turn
// journal reader (wake turns). The outcomes below are the contract; a change to one must be decided in review.

// fakeRunner is a monomind.Exec that completes a judging turn with a valid reply and reports the given cost.
func fakeRunner(t *testing.T, has bool, cost float64) judgemodel.ExecFunc {
	return func(ctx context.Context, opts monomind.ExecOptions, _ func(monomind.Event)) (*monomind.TurnResult, error) {
		body, err := (&fakeJudge{noCost: true}).Complete(ctx, judge.Request{System: opts.SystemPrompt, User: opts.Prompt})
		if err != nil {
			t.Fatal(err)
		}
		return &monomind.TurnResult{ResultText: string(body), SawDone: true, HasCostUSD: has, CostUSD: cost}, nil
	}
}

// A judge call is reserved at the per-call cap ($0.50) before it is made and settled once afterwards:
//   - the runtime reports nothing, a NaN, an infinity or a negative amount: the call stays at the whole cap, known = 0;
//   - it reports a plain amount: that amount, known = 1;
//   - it reports an absurd finite amount: the amount clamped to $1000, known = 1 (the ledger's sum cannot overflow, and the
//     20 percent rule stops the pursuit at once).
func TestAJudgeCallThatReportsNoUsableCostIsChargedTheWholePerCallCap(t *testing.T) {
	const capMicro = 500_000
	for name, c := range map[string]struct {
		has       bool
		cost      float64
		wantMicro int64
		wantKnown int
	}{
		"no cost reported":             {false, 0, capMicro, 0},
		"a NaN cost":                   {true, math.NaN(), capMicro, 0},
		"an infinite cost":             {true, math.Inf(1), capMicro, 0},
		"a negative cost":              {true, -3, capMicro, 0},
		"a plain cost":                 {true, 0.0123, 12_300, 1},
		"a reported zero is trusted":   {true, 0, 0, 1},
		"an absurd cost is clamped":    {true, 1e30, maxJudgeCallMicro, 1},
		"a cost above the cap is kept": {true, 0.9, 900_000, 1},
	} {
		t.Run(name, func(t *testing.T) {
			w := newPursuitWorld(t)
			model, err := judgemodel.New(judgemodel.Config{Pin: "fake/judge", Exec: fakeRunner(t, c.has, c.cost), Workspace: func() (string, error) { return t.TempDir(), nil }})
			if err != nil {
				t.Fatal(err)
			}
			w.h.Pursuit.Model = model
			w.firstEpoch()
			rows := w.ledger()
			if len(rows) != 1 {
				t.Fatalf("ledger rows: %+v", rows)
			}
			if r := rows[0]; r.Settled != 1 || r.Micro != c.wantMicro || r.Known != c.wantKnown {
				t.Fatalf("charged %+v, want micro %d known %d settled 1", r, c.wantMicro, c.wantKnown)
			}
		})
	}
}

// A wake turn runs under a recorded cap. What it cost is read from the turn journal (budget.go):
//   - it journaled no usage event: the WHOLE CAP is charged (unknown is never free);
//   - its events carry a negative or non-numeric cost: those are not costs, so again the whole cap;
//   - it reported 0: trusted at 0 (a runtime that always reports 0 is believed; kilo, which cannot be capped, is refused);
//   - it reported an absurd finite amount: that amount (the lead's remaining budget goes negative, so nothing more runs).
func TestAWakeTurnThatReportsNoUsableCostIsChargedItsWholeCap(t *testing.T) {
	const turnCap = 2.0
	for name, c := range map[string]struct {
		payloads []string
		want     float64
	}{
		"no usage event":              {nil, turnCap},
		"a negative cost":             {[]string{`{"agentId":"","costUsd":-5}`}, turnCap},
		"a cost that is text":         {[]string{`{"agentId":"","costUsd":"NaN"}`}, turnCap},
		"no cost field":               {[]string{`{"agentId":""}`}, turnCap},
		"a reported zero":             {[]string{`{"agentId":"","costUsd":0}`}, 0},
		"a plain cost":                {[]string{`{"agentId":"","costUsd":0.4}`}, 0.4},
		"the largest snapshot wins":   {[]string{`{"agentId":"","costUsd":0.9}`, `{"agentId":"","costUsd":0.3}`}, 0.9},
		"an absurd finite cost":       {[]string{`{"agentId":"","costUsd":1e30}`}, 1e30},
		"a negative after a real one": {[]string{`{"agentId":"","costUsd":0.7}`, `{"agentId":"","costUsd":-50}`}, 0.7},
	} {
		t.Run(name, func(t *testing.T) {
			_, _, st := newHost(t)
			if _, err := st.db.Exec(`INSERT OR IGNORE INTO profiles (id, name) VALUES ('p', 'p')`); err != nil {
				t.Fatal(err)
			}
			addConversation(t, st, "lead", "/w")
			if _, err := st.EnsureSession("p", "lead"); err != nil {
				t.Fatal(err)
			}
			turn := "pursuit-1-aa"
			if err := st.RecordTurnCap("p", "lead", turn, turnCap); err != nil {
				t.Fatal(err)
			}
			st.MarkTurnCapStarted("p", "lead", turn)
			addTurn(t, st, "lead", turn, "completed", "x", time.Now().Add(-time.Hour))
			for i, p := range c.payloads {
				event(t, st, "lead", turn, i+1, "usage.updated", p)
			}
			got, err := st.Spent("p", "lead")
			if err != nil || math.Abs(got-c.want) > 1e-9*math.Max(1, c.want) {
				t.Fatalf("charged %v (%v), want %v", got, err, c.want)
			}
		})
	}
}
