package tasks

import (
	"errors"
	"testing"
)

// Approving a draft re-reads the ceiling of its whole ancestor chain and holds the draft's STORED limits to
// it (ApproveSession, withinCeiling). Through the store a draft is always clamped, so no store-built tree
// reaches that check: these tests edit a draft's row by hand (data from before the rule, or an edited
// database), give it a task so that approval is otherwise possible, and expect the approval to be refused
// with the limit error and the draft left a draft. OUT OF SCOPE: how the row came to be over its
// ancestors; concurrent writers.

// approvalChain makes root -> middle -> draft, the operator creating each. The root holds the ceiling
// (coding, 5 tasks, depth 3, $10); the middle sits at the same limits, so the minimum over the chain is
// the root's. The draft is created at those limits too and holds one task.
func approvalChain(t *testing.T, s *Store, budget float64) (root, mid, draft Session) {
	t.Helper()
	limits := func(parent int64) SessionInput {
		return SessionInput{Title: "s", ParentSessionID: parent, AccessLevel: "coding", MaxTasks: 5, MaxDepth: 3, BudgetUSD: budget}
	}
	var err error
	if root, err = s.CreateSession(bg, "default", limits(0), human); err != nil {
		t.Fatal(err)
	}
	if mid, err = s.CreateSession(bg, "default", limits(root.ID), human); err != nil {
		t.Fatal(err)
	}
	if draft, err = s.CreateSession(bg, "default", limits(mid.ID), human); err != nil {
		t.Fatal(err)
	}
	if _, err = s.AddSessionTask(bg, "default", draft.ID, SessionTaskInput{Title: "t", Acceptance: "done when t"}, human); err != nil {
		t.Fatal(err)
	}
	return root, mid, draft
}

func TestApprovalRefusesADraftWhoseStoredLimitsAreAboveItsAncestors(t *testing.T) {
	for _, tc := range []struct {
		name   string
		budget float64 // the budget of every session in the chain
		edit   string  // run against the draft's row
		over   bool
	}{
		{"access", 10, `UPDATE task_sessions SET access_level = 'workspace' WHERE id = ?`, true},
		{"max tasks", 10, `UPDATE task_sessions SET max_tasks = 50 WHERE id = ?`, true},
		{"max depth", 10, `UPDATE task_sessions SET max_depth = 4 WHERE id = ?`, true},
		{"budget", 10, `UPDATE task_sessions SET budget_usd = 25 WHERE id = ?`, true},
		{"no budget under a budgeted chain", 10, `UPDATE task_sessions SET budget_usd = 0 WHERE id = ?`, true},
		// the control: the same edits, but to exactly the ceiling, are approved
		{"access at the ceiling", 10, `UPDATE task_sessions SET access_level = 'coding' WHERE id = ?`, false},
		{"max tasks at the ceiling", 10, `UPDATE task_sessions SET max_tasks = 5 WHERE id = ?`, false},
		{"max depth at the ceiling", 10, `UPDATE task_sessions SET max_depth = 3 WHERE id = ?`, false},
		{"budget at the ceiling", 10, `UPDATE task_sessions SET budget_usd = 10 WHERE id = ?`, false},
		{"no budget anywhere", 0, `UPDATE task_sessions SET budget_usd = 0 WHERE id = ?`, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s, db, _ := newTestStore(t)
			_, _, draft := approvalChain(t, s, tc.budget)
			if _, err := db.Exec(tc.edit, draft.ID); err != nil {
				t.Fatal(err)
			}
			got, err := s.ApproveSession(bg, "default", draft.ID, human)
			if tc.over {
				if !errors.Is(err, ErrLimit) {
					t.Fatalf("approving a draft above its ancestors: err = %v, want a limit", err)
				}
				after, gerr := s.GetSession(bg, "default", draft.ID, human)
				if gerr != nil {
					t.Fatal(gerr)
				}
				if after.State != SessionDraft {
					t.Fatalf("the refused draft is %s, want draft", after.State)
				}
				return
			}
			if err != nil {
				t.Fatalf("approving a draft within its ancestors: %v", err)
			}
			if got.State != SessionRunning {
				t.Fatalf("the approved session is %s, want running", got.State)
			}
		})
	}
}

// The ceiling is the minimum over the whole chain: a draft within its parent's limits but above the
// ROOT's (the middle edited by hand too) is refused.
func TestApprovalHoldsADraftToTheWholeChainNotItsParent(t *testing.T) {
	s, db, _ := newTestStore(t)
	_, mid, draft := approvalChain(t, s, 10)
	if _, err := db.Exec(`UPDATE task_sessions SET max_tasks = 50 WHERE id IN (?, ?)`, mid.ID, draft.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := s.ApproveSession(bg, "default", draft.ID, human); !errors.Is(err, ErrLimit) {
		t.Fatalf("err = %v, want a limit: the draft equals its parent's 50 but the root allows 5", err)
	}
}
