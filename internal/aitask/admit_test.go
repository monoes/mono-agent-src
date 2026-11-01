package aitask

import (
	"context"
	"testing"
)

// A refusal from the Admit hook keeps the task queued and detaches nothing; a
// task it admits runs.
func TestStartIsHeldByTheAdmitHook(t *testing.T) {
	s, l := newService(t)
	refuse := true
	s.Admit = func(context.Context, Task) (float64, error) {
		if refuse {
			return 0, &Error{Code: CodeBudgetExceeded, Message: "over budget"}
		}
		return 1, nil
	}
	task, err := s.Submit(context.Background(), SubmitInput{Kind: "generic", Goal: "g", QueueOnly: true})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.Start(context.Background(), task.ID); err == nil {
		t.Fatal("started over budget")
	}
	if cur, _ := s.Store.Get(task.ID); cur.Status != StatusQueued || len(l.detached) != 0 {
		t.Fatalf("status %s, detached %v: a refused start must leave the task queued", cur.Status, l.detached)
	}
	refuse = false
	if got, err := s.Start(context.Background(), task.ID); err != nil || got.Status != StatusRunning {
		t.Fatalf("admitted start: %v %v", got.Status, err)
	}
}
