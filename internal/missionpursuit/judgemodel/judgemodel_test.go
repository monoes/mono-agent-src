package judgemodel

import (
	"context"
	"errors"
	"math"
	"strings"
	"testing"

	"github.com/monoes/mono-agent/internal/doomsday/strategy/judge"
	"github.com/monoes/mono-agent/internal/doomsday/strategy/types"
	"github.com/monoes/mono-agent/internal/missionpursuit/judgecost"
	"github.com/monoes/mono-agent/internal/monomind"
)

type report struct {
	n     int
	usd   float64
	known bool
}

func sinkCtx() (context.Context, *report) {
	r := &report{}
	return judgecost.With(context.Background(), func(usd float64, known bool) { r.n++; r.usd, r.known = usd, known }), r
}

func newModel(t *testing.T, exec ExecFunc) judge.Model {
	t.Helper()
	m, err := New(Config{Pin: "claude/haiku@1", Exec: exec, Workspace: func() (string, error) { return t.TempDir(), nil }})
	if err != nil {
		t.Fatal(err)
	}
	return m
}

func done(text string) *monomind.TurnResult {
	return &monomind.TurnResult{ResultText: text, SawDone: true}
}

func TestACompletedCallReturnsTheReplyAndReportsTheCostTheRuntimeMeasured(t *testing.T) {
	var got monomind.ExecOptions
	m := newModel(t, func(_ context.Context, o monomind.ExecOptions, _ func(monomind.Event)) (*monomind.TurnResult, error) {
		got = o
		r := done(`{"x":1}`)
		r.CostUSD, r.HasCostUSD = 0.0123, true
		return r, nil
	})
	ctx, rep := sinkCtx()
	out, err := m.Complete(ctx, judge.Request{System: "s", User: "u"})
	if err != nil || string(out) != `{"x":1}` {
		t.Fatalf("complete: %q %v", out, err)
	}
	if rep.n != 1 || !rep.known || rep.usd != 0.0123 {
		t.Fatalf("the cost report: %+v", *rep)
	}
	if got.Runtime != "claude" || got.Model != "haiku" || got.Prompt != "u" || got.SystemPrompt != "s" || got.MaxTurns != 1 ||
		got.Sandbox != monomind.TurnSandboxMode || got.BudgetUSD != judgecost.PerCallCapUSD || len(got.Tools) != 0 {
		t.Fatalf("the turn is not a one-turn, no-tool, sandboxed, capped turn: %+v", got)
	}
	if m.Pin() != "claude/haiku@1" {
		t.Fatalf("pin %q", m.Pin())
	}
}

func TestACallThatReportsNoCostIsReportedUnknownNotFree(t *testing.T) {
	m := newModel(t, func(context.Context, monomind.ExecOptions, func(monomind.Event)) (*monomind.TurnResult, error) { return done("{}"), nil })
	ctx, rep := sinkCtx()
	if _, err := m.Complete(ctx, judge.Request{}); err != nil {
		t.Fatal(err)
	}
	if rep.n != 1 || rep.known {
		t.Fatalf("a turn with no reported cost must be reported unknown: %+v", *rep)
	}
}

func TestAFailedTurnStillReportsItsCost(t *testing.T) {
	for name, exec := range map[string]ExecFunc{
		"an error with usage": func(context.Context, monomind.ExecOptions, func(monomind.Event)) (*monomind.TurnResult, error) {
			return &monomind.TurnResult{CostUSD: 0.2, HasCostUSD: true}, errors.New("boom")
		},
		"an error with nothing": func(context.Context, monomind.ExecOptions, func(monomind.Event)) (*monomind.TurnResult, error) {
			return nil, errors.New("boom")
		},
		"not done": func(context.Context, monomind.ExecOptions, func(monomind.Event)) (*monomind.TurnResult, error) {
			return &monomind.TurnResult{ResultText: "{}", CostUSD: 0.2, HasCostUSD: true}, nil
		},
		"a non-zero exit": func(context.Context, monomind.ExecOptions, func(monomind.Event)) (*monomind.TurnResult, error) {
			return &monomind.TurnResult{ResultText: "{}", SawDone: true, ExitCode: 2, CostUSD: 0.2, HasCostUSD: true}, nil
		},
	} {
		t.Run(name, func(t *testing.T) {
			ctx, rep := sinkCtx()
			if _, err := newModel(t, exec).Complete(ctx, judge.Request{}); err == nil {
				t.Fatal("a failed turn gave an answer")
			}
			if rep.n != 1 {
				t.Fatalf("%d reports for a failed turn", rep.n)
			}
		})
	}
}

func TestAnOversizedReplyIsCutOffWhileItStreamsAndStillCharged(t *testing.T) {
	var cancelled bool
	m := newModel(t, func(ctx context.Context, _ monomind.ExecOptions, on func(monomind.Event)) (*monomind.TurnResult, error) {
		on(monomind.Event{Text: strings.Repeat("a", types.MaxJudgmentBytes+1)})
		cancelled = ctx.Err() != nil
		return &monomind.TurnResult{CostUSD: 0.4, HasCostUSD: true}, errors.New("cancelled")
	})
	ctx, rep := sinkCtx()
	_, err := m.Complete(ctx, judge.Request{})
	if !errors.Is(err, judge.ErrReplyTooLong) || !errors.Is(err, types.ErrBadJudgment) || !cancelled {
		t.Fatalf("err=%v cancelled=%t", err, cancelled)
	}
	if rep.n != 1 || !rep.known || rep.usd != 0.4 {
		t.Fatalf("the cost of the cut-off turn: %+v", *rep)
	}
	big := newModel(t, func(context.Context, monomind.ExecOptions, func(monomind.Event)) (*monomind.TurnResult, error) {
		return done(strings.Repeat("a", types.MaxJudgmentBytes+1)), nil
	})
	if _, err := big.Complete(context.Background(), judge.Request{}); !errors.Is(err, judge.ErrReplyTooLong) {
		t.Fatalf("a long result text: %v", err)
	}
}

func TestNoTurnMeansNothingSpentAndIsReportedSo(t *testing.T) {
	called := false
	m, err := New(Config{Pin: "claude/haiku", Exec: func(context.Context, monomind.ExecOptions, func(monomind.Event)) (*monomind.TurnResult, error) {
		called = true
		return nil, nil
	}, Workspace: func() (string, error) { return "", errors.New("no workspace") }})
	if err != nil {
		t.Fatal(err)
	}
	ctx, rep := sinkCtx()
	if _, err := m.Complete(ctx, judge.Request{}); err == nil || called {
		t.Fatalf("err=%v called=%t", err, called)
	}
	if rep.n != 1 || !rep.known || rep.usd != 0 {
		t.Fatalf("a call that never started a turn: %+v", *rep)
	}
}

func TestAnUnusableCostFromTheRuntimeIsReportedUnknown(t *testing.T) {
	for _, usd := range []float64{-1, math.Inf(1)} {
		m := newModel(t, func(context.Context, monomind.ExecOptions, func(monomind.Event)) (*monomind.TurnResult, error) {
			r := done("{}")
			r.CostUSD, r.HasCostUSD = usd, true
			return r, nil
		})
		ctx, rep := sinkCtx()
		if _, err := m.Complete(ctx, judge.Request{}); err != nil {
			t.Fatal(err)
		}
		if rep.known {
			t.Errorf("cost %v was taken as known", usd)
		}
	}
}

func TestThePinMustNameARuntimeAndAModel(t *testing.T) {
	for _, bad := range []string{"", "claude", "claude/", "/haiku", "claude/ha iku", "claude/@1"} {
		if _, err := New(Config{Pin: bad}); err == nil {
			t.Errorf("pin %q accepted", bad)
		}
	}
	if _, err := New(Config{Pin: "claude/haiku-4.5@2026"}); err != nil {
		t.Fatal(err)
	}
}

func TestTheIndependenceRuleRefusesTheLeadAndTheVerifierAndNeverRunsVacuous(t *testing.T) {
	if _, err := Refuse(); err == nil {
		t.Fatal("a rule with nothing to refuse was built")
	}
	if _, err := Refuse("", ""); err == nil {
		t.Fatal("a rule with only empty pins was built")
	}
	r, err := Refuse("claude/opus@1", "codex/gpt@2")
	if err != nil {
		t.Fatal(err)
	}
	for pin, want := range map[string]bool{"": true, "claude/opus@1": true, "codex/gpt@2": true, "claude/haiku@1": false} {
		if r(pin) != want {
			t.Errorf("refuse(%q) = %t", pin, !want)
		}
	}
}
