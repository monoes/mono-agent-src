package coderhost

import "testing"

// A mission whose budget is committed past (its own spend plus its children's budgets exceed it) cannot run any worker, so a
// wake-up or a judge call would be paid for nothing: the pursuit waits ("over-committed") and spends neither.
func TestAnOverCommittedMissionWaitsAndSpendsNothing(t *testing.T) {
	w := newPursuitWorld(t)
	w.exec(`INSERT INTO task_sessions (id, profile_id, title, state, kind, budget_usd, parent_session_id, created_by, created_at, updated_at) VALUES (90, 'p', 'kid', 'running', 'task', 10, 77, 'you', 'x', 'x')`)
	w.setSpent(2) // 2 spent + 10 held by the child of 10
	w.rounds(3)
	if w.model.count() != 0 || len(w.wakes()) != 0 || w.waiting() != "over-committed" {
		t.Fatalf("judge calls %d, wake-ups %d, waiting %q", w.model.count(), len(w.wakes()), w.waiting())
	}
	w.exec(`UPDATE task_sessions SET budget_usd = 12 WHERE id = 77`) // raised: room again
	w.rounds(1)
	if w.waiting() != "" {
		t.Fatalf("after the budget was raised: waiting %q", w.waiting())
	}
	if w.model.count() == 0 {
		t.Fatalf("the judge is called again once there is room")
	}
}

// Room under the budget is no reason to wait.
func TestAMissionWithRoomIsNotOverCommitted(t *testing.T) {
	w := newPursuitWorld(t)
	w.exec(`INSERT INTO task_sessions (id, profile_id, title, state, kind, budget_usd, parent_session_id, created_by, created_at, updated_at) VALUES (90, 'p', 'kid', 'running', 'task', 4, 77, 'you', 'x', 'x')`)
	w.rounds(1)
	if w.waiting() == "over-committed" || w.model.count() == 0 {
		t.Fatalf("judge calls %d, waiting %q", w.model.count(), w.waiting())
	}
}
