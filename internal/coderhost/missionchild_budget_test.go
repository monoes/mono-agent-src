package coderhost

import (
	"errors"
	"strings"
	"testing"
	"unicode/utf8"
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

// A carve the lead's budget no longer covers (it was lowered after the reservation read it) is a refusal, not a
// failed attempt: the task goes back to marked with all its attempts, and no conversation is left.
func TestACarveRefusedAfterTheConversationExistsUsesNoAttempt(t *testing.T) {
	f := newMC(t, true)
	if err := f.st.SetBudget("p", f.lead.ConversationID, 10); err != nil {
		t.Fatal(err)
	}
	if _, err := f.st.Mark(f.scope(), "p", f.se.ID, f.ids[0], MarkOptions{BudgetUSD: 3}); err != nil {
		t.Fatal(err)
	}
	f.st.afterChildCreated = func() {
		if _, err := f.st.db.Exec(`UPDATE coder_sessions SET budget_usd = 0.5 WHERE profile_id = 'p' AND conversation_id = ?`, f.lead.ConversationID); err != nil {
			t.Error(err)
		}
	}
	_, err := f.st.SpawnForTask(f.scope(), "p", f.se.ID, f.ids[0], "brief")
	if !errors.Is(err, ErrLimit) {
		t.Fatalf("spawn = %v, want a limit refusal", err)
	}
	r, _, cerr := f.st.Child("p", f.se.ID, f.ids[0])
	if cerr != nil || r.State != ChildMarked || r.Attempts != 0 || r.EndReason != "" {
		t.Fatalf("a carve refusal used an attempt: %+v %v", r, cerr)
	}
	var n int
	if f.st.db.QueryRow(`SELECT COUNT(*) FROM coder_sessions WHERE role = 'child'`).Scan(&n) != nil || n != 0 {
		t.Fatalf("%d child sessions left", n)
	}
}

// An audit note cut to its limit stays valid UTF-8.
func TestMissionEventNoteIsCutOnACharacterBoundary(t *testing.T) {
	f := newMC(t, true)
	note := "a" + strings.Repeat("é", 200) // byte 300 is the second half of a character
	if err := addMissionEvent(f.st.db, "p", f.se.ID, f.ids[0], "spawn", "host", note, f.st.stamp()); err != nil {
		t.Fatal(err)
	}
	var got string
	if err := f.st.db.QueryRow(`SELECT note FROM coder_mission_events WHERE kind = 'spawn' ORDER BY id DESC LIMIT 1`).Scan(&got); err != nil {
		t.Fatal(err)
	}
	if !utf8.ValidString(got) || len(got) > 300 || len(got) < 298 {
		t.Fatalf("note = %d bytes, valid=%v", len(got), utf8.ValidString(got))
	}
}
