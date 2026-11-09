package verifier

import (
	"context"
	"testing"
)

func TestTheFakeAnswersItsScriptAndRecordsWhatItWasAsked(t *testing.T) {
	f := NewFake(checkerPin, FakeStep{Result: Result{Outcome: OutcomeFailed, Reason: ReasonCriteriaFailed}}, FakeStep{Result: Result{Outcome: OutcomePassedMachine}})
	q := Request{Root: "/x", GoalHash: h64("goal"), Criteria: []Criterion{fileExists("c1")}, JudgePin: judgePin, LeadPin: leadPin, NoLeadReread: true}
	a, err := f.Run(context.Background(), q)
	b, _ := f.Run(context.Background(), q)
	if err != nil || a.Outcome != OutcomeFailed || b.Outcome != OutcomePassedMachine || len(f.Calls()) != 2 || f.Pin() != checkerPin {
		t.Fatalf("%v %v", a, b)
	}
	if _, err := f.Run(context.Background(), q); err == nil {
		t.Fatal("an exhausted script must be an error, not a pass")
	}
}

func TestTheFakeRefusesWhatTheRealOneRefuses(t *testing.T) {
	f := NewFake(checkerPin, FakeStep{Result: Result{Outcome: OutcomePassedMachine}})
	q := Request{Root: "/x", GoalHash: h64("goal"), Criteria: []Criterion{fileExists("c1")}, JudgePin: checkerPin, LeadPin: leadPin, NoLeadReread: true}
	if got, _ := f.Run(context.Background(), q); got.Outcome != OutcomeCannotCheck || got.Reason != ReasonPinNotIndependent {
		t.Fatalf("%v", got)
	}
	q.JudgePin = judgePin
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if got, _ := f.Run(ctx, q); got.Outcome != OutcomeAbandoned {
		t.Fatalf("%v", got)
	}
}

func TestTheFakeCanBeCancelledMidCheck(t *testing.T) {
	f := NewFake(checkerPin, FakeStep{UntilCancelled: true})
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan Result)
	go func() {
		got, _ := f.Run(ctx, Request{Root: "/x", GoalHash: h64("goal"), Criteria: []Criterion{fileExists("c1")}, JudgePin: judgePin, LeadPin: leadPin, NoLeadReread: true})
		done <- got
	}()
	cancel()
	if got := <-done; got.Outcome != OutcomeAbandoned {
		t.Fatalf("%v", got)
	}
}

func TestMessage(t *testing.T) {
	if Message(OutcomeCannotCheck, ReasonNoNetworkBlock) != "cannot-check: no network block" || Message(OutcomeFailed, ReasonCriteriaFailed) != "failed: criteria-failed" ||
		Message(OutcomePassedMachine, ReasonNone) != "passed-machine" {
		t.Fatal("messages")
	}
}

func TestTheFakeValidatesLikeTheRealOneAndCopiesTheCriteria(t *testing.T) {
	f := NewFake(checkerPin, FakeStep{Result: Result{Outcome: OutcomePassedMachine}})
	cs := []Criterion{fileExists("c1")}
	q := Request{Root: "/x", GoalHash: h64("goal"), Criteria: cs, JudgePin: judgePin, LeadPin: leadPin, NoLeadReread: true}
	bad := q
	bad.GoalHash = ""
	if got, _ := f.Run(context.Background(), bad); got.Reason != ReasonBadRequest {
		t.Fatalf("%v", got)
	}
	got, _ := f.Run(context.Background(), q)
	cs[0].ID = "mutated"
	if got.Outcome != OutcomePassedMachine || f.Calls()[1].Criteria[0].ID != "c1" || got.GoalHash != q.GoalHash {
		t.Fatalf("%v %v", got, f.Calls())
	}
}
