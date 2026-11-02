package coderhost

import (
	"errors"
	"fmt"
	"testing"
)

// Tests for the review of #1121: a child grant obeys the share ceiling and the reservations like a carve does,
// a stale request stays closed, stale rows hide nothing, the row is pinned, and money is compared exactly.

// A child's raise stays inside the share of the lead's remaining budget that the mission's policy allows,
// exactly as a carve does (roomIn): $10 lead, 50% share, a $4 child: $5 may be carved in all.
func TestBudgetGrantRespectsTheShareCeiling(t *testing.T) {
	f, child := budgetFix(t)
	b := f.ask(t, TargetChild, 3) // $4 + $3 > 50% of $10
	if _, err := f.st.GrantBudget(OperatorScope{}, "p", b.ID, 0); !errors.Is(err, ErrLimit) {
		t.Fatalf("a grant past the share ceiling: %v", err)
	}
	if f.capOf(t, child) != 4 {
		t.Fatalf("cap = %v", f.capOf(t, child))
	}
	if _, err := f.st.GrantBudget(OperatorScope{}, "p", b.ID, 1); err != nil { // $5 is the share
		t.Fatalf("a grant that fits the share: %v", err)
	}
}

// What the lead has already reserved for children that are still starting counts against the same ceiling.
func TestBudgetGrantCountsOutstandingReservations(t *testing.T) {
	f, child := budgetFix(t)
	if err := f.st.SetBudget("p", f.lead.ConversationID, 20); err != nil {
		t.Fatal(err)
	}
	f.addTasks(t, 1)
	f.setPolicy(t, OperatorScope{}, PolicyPatch{SharePct: ip(80)}) // $16 of $20
	if _, err := f.st.Mark(f.scope(), "p", f.se.ID, f.ids[1], MarkOptions{BudgetUSD: 10}); err != nil {
		t.Fatal(err)
	}
	if _, err := f.st.Reserve(f.scope(), "p", f.se.ID, f.ids[1]); err != nil { // $4 held + $10 reserved
		t.Fatal(err)
	}
	b := f.ask(t, TargetChild, 3) // $4 + $10 + $3 > $16
	if _, err := f.st.GrantBudget(OperatorScope{}, "p", b.ID, 0); !errors.Is(err, ErrLimit) {
		t.Fatalf("a grant past the reservations: %v", err)
	}
	if _, err := f.st.GrantBudget(OperatorScope{}, "p", b.ID, 2); err != nil { // exactly $16
		t.Fatalf("a grant that fits: %v", err)
	}
	if f.capOf(t, child) != 6 {
		t.Fatalf("cap = %v", f.capOf(t, child))
	}
}

// Once stale, always closed: a budget that moves and comes back does not make the request grantable again, and a
// stale request cannot be declined into a fresh answer either.
func TestStaleBudgetRequestStaysClosedAfterTheNumbersComeBack(t *testing.T) {
	f, child := budgetFix(t)
	b := f.ask(t, TargetChild, 1)
	if err := f.st.SetBudget("p", child, 4.5); err != nil {
		t.Fatal(err)
	}
	if err := f.st.SetBudget("p", child, 4); err != nil {
		t.Fatal(err)
	}
	if got, _ := f.st.GetBudgetRequest("p", b.ID); got.Open || got.Status == BudgetRequestOpen {
		t.Fatalf("a request that went stale is %+v", got)
	}
	if _, err := f.st.GrantBudget(OperatorScope{}, "p", b.ID, 0); !errors.Is(err, ErrBudgetRequestClosed) {
		t.Fatalf("grant after ABA: %v", err)
	}
	if _, err := f.st.DeclineBudget(OperatorScope{}, "p", b.ID); !errors.Is(err, ErrBudgetRequestClosed) {
		t.Fatalf("decline of a stale request: %v", err)
	}
	// the same for a mission that ended and came back
	g := f.ask(t, TargetChild, 1)
	f.st.db.Exec(`UPDATE task_sessions SET state = 'done' WHERE id = ?`, f.se.ID)
	f.st.db.Exec(`UPDATE task_sessions SET state = 'running' WHERE id = ?`, f.se.ID)
	if _, err := f.st.GrantBudget(OperatorScope{}, "p", g.ID, 0); !errors.Is(err, ErrBudgetRequestClosed) {
		t.Fatalf("grant after the mission ended and came back: %v", err)
	}
}

// Old stale rows do not hide a valid request: the open rows are filtered, then limited.
func TestStaleRowsDoNotHideAValidBudgetRequest(t *testing.T) {
	f, _ := budgetFix(t)
	tx, err := f.st.db.Begin()
	if err != nil {
		t.Fatal(err)
	}
	for i := 0; i < listLimitMax+100; i++ { // rows of long-gone missions that were never voided
		res, err := tx.Exec(`INSERT INTO coder_budget_requests (profile_id, session_id, lead_conversation_id, target, target_conversation_id, amount_usd, budget_usd, spent_usd, status, created_at)
			VALUES ('p', 99999, 'gone', 'lead', ?, 1, 1, 0, 'open', '2020-01-01T00:00:00Z')`, fmt.Sprintf("gone-%d", i))
		if err != nil {
			t.Fatal(err)
		}
		id, _ := res.LastInsertId()
		if _, err := tx.Exec(`INSERT INTO coder_session_person_items (profile_id, conversation_id, kind, ref_id, created_at) VALUES ('p', 'gone', 'budget_request', ?, '2020-01-01T00:00:00Z')`, id); err != nil {
			t.Fatal(err)
		}
	}
	if err := tx.Commit(); err != nil {
		t.Fatal(err)
	}
	b := f.ask(t, TargetLead, 1)
	items, err := f.st.OpenPersonItems("p")
	if err != nil || len(items) != 1 || items[0].RefID != b.ID {
		t.Fatalf("items = %d (%+v), %v: the valid request is hidden", len(items), items, err)
	}
	if open := f.requests(t, true); len(open) != 1 || open[0].ID != b.ID {
		t.Fatalf("open list = %d", len(open))
	}
}

// What the person saw is what a grant raises: the request's identity cannot be edited, a closed request cannot be
// reopened, and a lead-target row names the lead itself.
func TestBudgetRequestRowIsPinned(t *testing.T) {
	f, child := budgetFix(t)
	b := f.ask(t, TargetChild, 1)
	for _, set := range []string{"target_conversation_id = 'other'", "session_id = 1", "amount_usd = 50", "target = 'lead'", "lead_conversation_id = 'x'", "budget_usd = 9"} {
		if _, err := f.st.db.Exec(`UPDATE coder_budget_requests SET `+set+` WHERE id = ?`, b.ID); err == nil {
			t.Errorf("%s was accepted", set)
		}
	}
	if _, err := f.st.DeclineBudget(OperatorScope{}, "p", b.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := f.st.db.Exec(`UPDATE coder_budget_requests SET status = 'open' WHERE id = ?`, b.ID); err == nil {
		t.Error("a declined request was reopened")
	}
	if _, err := f.st.db.Exec(`INSERT INTO coder_budget_requests (profile_id, session_id, lead_conversation_id, target, target_conversation_id, amount_usd, budget_usd, spent_usd, status, created_at)
		VALUES ('p', ?, ?, 'lead', ?, 1, 4, 0, 'open', '2026-01-01T00:00:00Z')`, f.se.ID, f.lead.ConversationID, child); err != nil {
		t.Fatal(err)
	}
	var id int64
	f.st.db.QueryRow(`SELECT MAX(id) FROM coder_budget_requests`).Scan(&id)
	if got, _ := f.st.GetBudgetRequest("p", id); got.Open {
		t.Fatalf("a lead-target row aimed at a child is open: %+v", got)
	}
	if _, err := f.st.GrantBudget(OperatorScope{}, "p", id, 0); err == nil {
		t.Fatal("a mis-aimed lead row was granted")
	}
}

// Money is compared in whole micro-dollars: no tolerance lets a grant exceed what was asked.
func TestBudgetGrantAmountIsExactAndAtLeastACent(t *testing.T) {
	f, _ := budgetFix(t)
	b := f.ask(t, TargetChild, 1)
	if _, err := f.st.GrantBudget(OperatorScope{}, "p", b.ID, 1+1e-5); !errors.Is(err, ErrInvalid) {
		t.Fatalf("a grant above the ask by a hair: %v", err)
	}
	if _, err := f.st.RequestBudget(f.scope(), "p", f.se.ID, TargetLead, 0, 0.001, "x"); !errors.Is(err, ErrInvalid) {
		t.Fatalf("a request below a cent: %v", err)
	}
	if _, err := f.st.GrantBudget(OperatorScope{}, "p", b.ID, 0.001); !errors.Is(err, ErrInvalid) {
		t.Fatalf("a grant below a cent: %v", err)
	}
}
