package tasks

import (
	"errors"
	"testing"
)

func budgetOf(t *testing.T, s *Store, id int64) (float64, float64) {
	t.Helper()
	se, err := s.GetSession(bg, "default", id, human)
	if err != nil {
		t.Fatal(err)
	}
	return se.BudgetUSD, se.SpentUSD
}

// Finding 7: the limits and the spend are one write; a refused spend leaves the limits alone.
func TestChangeSessionIsAtomic(t *testing.T) {
	s, db, _ := newTestStore(t)
	se := mustSession(t, s, "default", "budget")
	five, three := 5.0, 3.0
	if _, err := s.ChangeSession(bg, "default", se.ID, SessionUpdate{BudgetUSD: &five}, -1, "", human); !errors.Is(err, ErrInvalid) {
		t.Fatalf("a spend below 0: %v", err)
	}
	if b, sp := budgetOf(t, s, se.ID); b != 0 || sp != 0 {
		t.Fatalf("applied a part: budget %v spent %v", b, sp)
	}
	// the spend's own write fails after the limits were written: nothing stays
	if _, err := db.Exec(`CREATE TRIGGER no_spend BEFORE INSERT ON task_session_events WHEN NEW.kind = 'spend' BEGIN SELECT RAISE(ABORT, 'boom'); END`); err != nil {
		t.Fatal(err)
	}
	if _, err := s.ChangeSession(bg, "default", se.ID, SessionUpdate{BudgetUSD: &five}, 1, "x", human); err == nil {
		t.Fatal("the injected failure was not seen")
	}
	if b, sp := budgetOf(t, s, se.ID); b != 0 || sp != 0 {
		t.Fatalf("applied a part after a failure: budget %v spent %v", b, sp)
	}
	if _, err := db.Exec(`DROP TRIGGER no_spend`); err != nil {
		t.Fatal(err)
	}
	if _, err := s.ChangeSession(bg, "default", se.ID, SessionUpdate{BudgetUSD: &three}, 1.5, "x", human); err != nil {
		t.Fatal(err)
	}
	if b, sp := budgetOf(t, s, se.ID); b != 3 || sp != 1.5 {
		t.Fatalf("budget %v spent %v", b, sp)
	}
	if _, err := s.ChangeSession(bg, "default", se.ID, SessionUpdate{}, 0, "", human); !errors.Is(err, ErrInvalid) {
		t.Fatalf("nothing: %v", err)
	}
	if _, err := s.ChangeSession(bg, "default", se.ID, SessionUpdate{BudgetUSD: &three}, 0, "", bot("a")); !errors.Is(err, ErrOperatorOnly) {
		t.Fatalf("agent: %v", err)
	}
}
