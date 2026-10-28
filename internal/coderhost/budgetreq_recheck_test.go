package coderhost

import (
	"errors"
	"math"
	"testing"
)

// Tests for the second review of #1121.

// A grant is a checked amount: nothing outside the range of what can be asked is converted, rounded or stored.
func TestBudgetGrantRefusesOutOfRangeAmounts(t *testing.T) {
	f, child := budgetFix(t)
	b := f.ask(t, TargetChild, 1)
	for _, usd := range []float64{1e100, 1e308, 51, math.MaxFloat64, math.Inf(1), math.NaN(), -1} {
		if _, err := f.st.GrantBudget(OperatorScope{}, "p", b.ID, usd); !errors.Is(err, ErrInvalid) {
			t.Fatalf("grant of %v: %v", usd, err)
		}
	}
	if f.capOf(t, child) != 4 {
		t.Fatalf("cap = %v after refused grants", f.capOf(t, child))
	}
	if got, _ := f.st.GetBudgetRequest("p", b.ID); got.Status != BudgetRequestOpen {
		t.Fatalf("a refused grant closed the request: %+v", got)
	}
	for _, usd := range []float64{1e100, math.Inf(1), math.NaN(), 51} {
		if _, err := f.st.RequestBudget(f.scope(), "p", f.se.ID, TargetLead, 0, usd, "x"); !errors.Is(err, ErrInvalid) {
			t.Fatalf("request of %v: %v", usd, err)
		}
	}
}

// The amount stored and the amount compared are the same whole-micro-dollar number: a request for $1.0000004
// is stored as $1, and a grant of $1.0000004 adds exactly $1.
func TestBudgetAmountsAreStoredAsWholeMicroDollars(t *testing.T) {
	f, child := budgetFix(t)
	b := f.ask(t, TargetChild, 1.0000004)
	if b.AmountUSD != 1 {
		t.Fatalf("stored amount = %v, want 1", b.AmountUSD)
	}
	got, err := f.st.GrantBudget(OperatorScope{}, "p", b.ID, 1.0000004)
	if err != nil || got.GrantedUSD != 1 {
		t.Fatalf("grant = %+v, %v", got, err)
	}
	if f.capOf(t, child) != 5 {
		t.Fatalf("cap = %v, want exactly 5", f.capOf(t, child))
	}
}

// A child row that is removed and put back does not bring a stale request back to life.
func TestRemovedAndRestoredChildRowKeepsTheRequestVoid(t *testing.T) {
	f, child := budgetFix(t)
	b := f.ask(t, TargetChild, 1)
	row, found, err := f.st.Child("p", f.se.ID, f.ids[0])
	if err != nil || !found {
		t.Fatal(found, err)
	}
	if _, err := f.st.db.Exec(`DELETE FROM coder_mission_children WHERE id = ?`, row.ID); err != nil {
		t.Fatal(err)
	}
	if got, _ := f.st.GetBudgetRequest("p", b.ID); got.Status != BudgetRequestVoid {
		t.Fatalf("after the row was removed: %+v", got)
	}
	if _, err := f.st.db.Exec(`INSERT INTO coder_mission_children (id, profile_id, session_id, task_id, lead_conversation_id, child_conversation_id, origin, state, created_at, updated_at, spawned_at)
		VALUES (?, 'p', ?, ?, ?, ?, ?, 'spawned', ?, ?, ?)`, row.ID, f.se.ID, f.ids[0], row.LeadConversationID, child, row.Origin, row.CreatedAt, row.UpdatedAt, row.SpawnedAt); err != nil {
		t.Fatalf("cannot restore the row: %v", err)
	}
	if _, err := f.st.GrantBudget(OperatorScope{}, "p", b.ID, 0); !errors.Is(err, ErrBudgetRequestClosed) {
		t.Fatalf("grant after the row came back: %v", err)
	}
}

// Any change of the target's budget voids the request, however small, even when it is put back.
func TestATinyBudgetChangeVoidsTheRequest(t *testing.T) {
	f, child := budgetFix(t)
	b := f.ask(t, TargetChild, 1)
	if _, err := f.st.db.Exec(`UPDATE coder_sessions SET budget_usd = ? WHERE conversation_id = ?`, 4+1e-12, child); err != nil {
		t.Fatal(err)
	}
	if _, err := f.st.db.Exec(`UPDATE coder_sessions SET budget_usd = 4 WHERE conversation_id = ?`, child); err != nil {
		t.Fatal(err)
	}
	if got, _ := f.st.GetBudgetRequest("p", b.ID); got.Status != BudgetRequestVoid {
		t.Fatalf("after a 1e-12 change and back: %+v", got)
	}
}
