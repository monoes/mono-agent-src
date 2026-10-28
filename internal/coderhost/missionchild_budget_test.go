package coderhost

import (
	"errors"
	"testing"
)

// A mission with a budget binds its children: the lead conversation may have no cap of its own, and a marked task
// still cannot start without a positive carve that fits what the mission has left.
func TestMissionBudgetBindsChildrenWhoseLeadHasNoCap(t *testing.T) {
	f := newMC(t, true)
	if _, err := f.st.db.Exec(`UPDATE task_sessions SET budget_usd = 10, spent_usd = 2 WHERE id = ?`, f.se.ID); err != nil {
		t.Fatal(err)
	}
	f.addTasks(t, 1)
	mark := func(i int, usd float64) {
		t.Helper()
		if _, err := f.st.Mark(f.scope(), "p", f.se.ID, f.ids[i], MarkOptions{BudgetUSD: usd}); err != nil {
			t.Fatal(err)
		}
	}
	mark(0, 0) // no wish, no default, lead uncapped: carve 0
	if _, err := f.st.Reserve(f.scope(), "p", f.se.ID, f.ids[0]); !errors.Is(err, ErrChildrenFull) {
		t.Fatalf("an uncapped child under a mission budget was reserved: %v", err)
	}
	if r, _, err := f.st.Child("p", f.se.ID, f.ids[0]); err != nil || r.State != ChildMarked || r.Attempts != 0 {
		t.Fatalf("a refusal used an attempt or moved the row: %+v %v", r, err)
	}
	mark(1, 9) // $2 of $10 is spent: $8 left
	if _, err := f.st.Reserve(f.scope(), "p", f.se.ID, f.ids[1]); !errors.Is(err, ErrChildrenFull) {
		t.Fatalf("a carve past the mission's remaining budget was reserved: %v", err)
	}
	mark(1, 5)
	if _, err := f.st.Reserve(f.scope(), "p", f.se.ID, f.ids[1]); err != nil {
		t.Fatalf("a carve that fits was refused: %v", err)
	}
	mark(2, 4) // $5 held + $4 > $8
	if _, err := f.st.Reserve(f.scope(), "p", f.se.ID, f.ids[2]); !errors.Is(err, ErrChildrenFull) {
		t.Fatalf("two carves passed the mission's remaining budget: %v", err)
	}
}
