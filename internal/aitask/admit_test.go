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

// A second Start that lands while the first is between its checks and its
// move must not replace the winner's token (R09-04).
func TestConcurrentStartKeepsTheWinnersToken(t *testing.T) {
	s, l := newService(t)
	task, err := s.Submit(context.Background(), SubmitInput{Kind: "generic", Goal: "g", QueueOnly: true})
	if err != nil {
		t.Fatal(err)
	}
	nested := false
	s.Admit = func(ctx context.Context, tk Task) (float64, error) {
		if !nested {
			nested = true
			if _, err := s.Start(ctx, tk.ID); err != nil {
				t.Fatal(err)
			}
		}
		return 1, nil
	}
	if _, err := s.Start(context.Background(), task.ID); err != nil {
		t.Fatal(err)
	}
	if len(l.tokens) != 1 {
		t.Fatalf("detached %d times", len(l.tokens))
	}
	cur, _ := s.Store.Get(task.ID)
	var hash string
	if err := s.Store.DB.QueryRow(`SELECT token_hash FROM ai_tasks WHERE id = ?`, task.ID).Scan(&hash); err != nil {
		t.Fatal(err)
	}
	if hash != HashToken(cur.Profile, cur.ID, cur.ConversationID, l.tokens[0]) {
		t.Fatal("the detached session holds a token that is no longer the task's")
	}
}
