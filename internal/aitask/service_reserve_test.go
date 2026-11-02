package aitask

import (
	"context"
	"errors"
	"testing"
	"time"
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

}

// Only a refusal for want of budget keeps the task queued; every other admission error is a failed start.
func TestSubmitKeepsOnlyABudgetHoldQueued(t *testing.T) {
	cases := []struct {
		name   string
		err    error
		queued bool
	}{
		{"no budget", &Error{Code: CodeNoBudget, Message: "no cap"}, true},
		{"budget exceeded", &Error{Code: CodeBudgetExceeded, Message: "over"}, true},
		{"unenforceable runtime", &Error{Code: "budget_unenforceable", Message: "cannot hold a cap"}, false},
		{"database", errors.New("database is locked"), false},
		{"context", context.Canceled, false},
	}
	for _, c := range cases {
		s, _ := newService(t)
		s.Admit = func(context.Context, Task) (float64, error) { return 0, c.err }
		if _, err := s.Submit(context.Background(), SubmitInput{Kind: "generic", Goal: "g"}); err == nil {
			t.Fatalf("%s: started", c.name)
		}
		tasks, _ := s.Store.List("", "")
		want := StatusFailed
		if c.queued {
			want = StatusQueued
		}
		if len(tasks) != 1 || tasks[0].Status != want {
			t.Errorf("%s: %+v, want one %s task", c.name, tasks, want)
		}
	}
}

// A reservation is not a finished task, not listed, and a stale one (its submit died) is failed with a reason.
func TestAReservationIsNotListedAndAStaleOneIsReaped(t *testing.T) {
	s, _ := newService(t)
	const id = "at-00000000000000e5"
	if err := s.Store.Reserve(Task{ID: id, Kind: "generic", Goal: "g", Owner: "o"}); err != nil {
		t.Fatal(err)
	}
	if got, _ := s.Store.Get(id); got.Status != StatusReserved || Terminal(got.Status) {
		t.Fatalf("reservation = %+v", got)
	}
	if ts, _ := s.Store.List("", ""); len(ts) != 0 {
		t.Fatalf("a reservation is listed as a task: %+v", ts)
	}
	if err := s.Store.ReapReservations(); err != nil {
		t.Fatal(err)
	}
	if got, _ := s.Store.Get(id); got.Status != StatusReserved {
		t.Fatalf("a live reservation was failed at once: %+v", got)
	}
	if _, err := s.Store.DB.Exec(`UPDATE ai_tasks SET updated_at = ? WHERE id = ?`,
		time.Now().Add(-2*ReservationTTL).UTC().Format(time.RFC3339Nano), id); err != nil {
		t.Fatal(err)
	}
	if err := s.Store.ReapReservations(); err != nil {
		t.Fatal(err)
	}
	if got, _ := s.Store.Get(id); got.Status != StatusFailed || got.Error == "" {
		t.Fatalf("a stale reservation = %+v", got)
	}
}

// The recovery cancel holds only for a queued task of the named owner and application.
func TestCancelQueuedOwnedTouchesOnlyAQueuedTaskOfItsOwner(t *testing.T) {
	s, _ := newService(t)
	mk := func(id, status, owner string) {
		if _, err := s.Store.Insert(Task{ID: id, Kind: "generic", ConversationID: "c", Status: status, Goal: "g",
			ApplicationID: "app", Owner: owner, Access: AccessCoding, BudgetUSD: 1}); err != nil {
			t.Fatal(err)
		}
	}
	mk("at-00000000000000a1", StatusQueued, "me")
	mk("at-00000000000000a2", StatusRunning, "me")
	mk("at-00000000000000a3", StatusQueued, "other")
	for id, want := range map[string]bool{"at-00000000000000a1": true, "at-00000000000000a2": false, "at-00000000000000a3": false} {
		got, err := s.Store.CancelQueuedOwned(id, "me", "app")
		if err != nil || got != want {
			t.Errorf("%s: cancelled %v %v, want %v", id, got, err, want)
		}
	}
	if got, _ := s.Store.CancelQueuedOwned("at-00000000000000a3", "me", "elsewhere"); got {
		t.Error("cancelled a task of another application")
	}
	if g, _ := s.Store.Get("at-00000000000000a2"); g.Status != StatusRunning {
		t.Errorf("a running task was cancelled: %+v", g)
	}
}
