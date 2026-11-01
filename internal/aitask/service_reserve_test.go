package aitask

import (
	"context"
	"errors"
	"testing"
)

// Two submits that name one id at the same moment: the second arrives while the first is between its id check and
// its insert. Only the first may make a conversation or a context folder; the second is refused at the id.
func TestSubmitsNamingOneIdAtOnceHaveOneWinner(t *testing.T) {
	s, l := newService(t)
	const id = "at-00000000000000c3"
	var innerErr error
	nested := false
	l.onCreate = func() {
		if nested {
			return
		}
		nested = true
		_, innerErr = s.Submit(context.Background(), SubmitInput{ID: id, Kind: "generic", Goal: "second", QueueOnly: true})
	}
	first, err := s.Submit(context.Background(), SubmitInput{ID: id, Kind: "generic", Goal: "first", QueueOnly: true})
	if err != nil || first.Goal != "first" || first.Status != StatusQueued {
		t.Fatalf("the first submit: %+v %v", first, err)
	}
	if code(innerErr) != CodeInvalidInput {
		t.Fatalf("the second submit = %v, want it refused at the id", innerErr)
	}
	if l.attempts != 1 {
		t.Fatalf("%d conversations were made for one id", l.attempts)
	}
}

// A named id whose submit stops after it was reserved leaves a failed row with the reason, never a queued one.
func TestAReservedIdThatNeverBecameATaskIsFailedWithTheReason(t *testing.T) {
	s, l := newService(t)
	l.refuse = errors.New("no conversation")
	const id = "at-00000000000000d4"
	if _, err := s.Submit(context.Background(), SubmitInput{ID: id, Kind: "generic", Goal: "g"}); err == nil {
		t.Fatal("submit succeeded")
	}
	got, err := s.Store.Get(id)
	if err != nil || got.Status != StatusFailed || got.Error != "no conversation" {
		t.Fatalf("reserved row = %+v %v", got, err)
	}
}

// A submit that made its task and then could not start it does not leave it queued; a budget hold does (it is
// started later).
func TestSubmitThatCannotStartItsTaskDoesNotLeaveItQueued(t *testing.T) {
	s, l := newService(t)
	l.onCreate = func() { l.readyErr = errf(CodeCoderDisabled, "coder mode is off") }
	if _, err := s.Submit(context.Background(), SubmitInput{Kind: "generic", Goal: "g"}); code(err) != CodeCoderDisabled {
		t.Fatalf("submit = %v", err)
	}
	tasks, err := s.Store.List("", "")
	if err != nil || len(tasks) != 1 || tasks[0].Status != StatusFailed || tasks[0].Error == "" {
		t.Fatalf("tasks = %+v %v", tasks, err)
	}

	s, _ = newService(t)
	s.Admit = func(context.Context, Task) (float64, error) { return 0, errors.New("over budget") }
	if _, err := s.Submit(context.Background(), SubmitInput{Kind: "generic", Goal: "g"}); err == nil {
		t.Fatal("started over budget")
	}
	tasks, _ = s.Store.List("", "")
	if len(tasks) != 1 || tasks[0].Status != StatusQueued {
		t.Fatalf("a budget hold must keep the task queued: %+v", tasks)
	}
}
