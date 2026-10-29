package coderhost

import (
	"errors"
	"testing"
	"time"
)

// carveRefusedFix is a lead with a budget that drops once the task's child exists, so the carve is refused.
func carveRefusedFix(t *testing.T) *hostFix {
	t.Helper()
	f := newHostFix(t)
	if err := f.st.SetBudget("p", f.lead.ConversationID, 10); err != nil {
		t.Fatal(err)
	}
	if _, err := f.st.Mark(f.scope(), "p", f.se.ID, f.ids[0], MarkOptions{BudgetUSD: 3}); err != nil {
		t.Fatal(err)
	}
	f.st.afterChildCreated = func() {
		if err := f.st.SetBudget("p", f.lead.ConversationID, 0.5); err != nil {
			t.Error(err)
		}
	}
	return f
}

// A refused carve reads as full (the task waits), and the task goes back to marked without an attempt.
func TestRefusedCarveReadsAsChildrenFull(t *testing.T) {
	f := carveRefusedFix(t)
	_, err := f.st.SpawnForTask(f.scope(), "p", f.se.ID, f.ids[0], "brief")
	if !errors.Is(err, ErrChildrenFull) || !errors.Is(err, ErrLimit) {
		t.Fatalf("a refused carve = %v, want ErrChildrenFull (wrapping ErrLimit)", err)
	}
	if r := f.row(t, 0); r.State != ChildMarked || r.Attempts != 0 {
		t.Fatalf("row after a refused carve = %+v, want marked with no attempt", r)
	}
}

// If deleting the conversation fails after a refused carve, the row keeps naming it, so the host's stale pass can
// end and stop it; it must not be left as a child-role session in no row.
func TestRefusedCarveWithFailedDeleteKeepsTheConversationOnTheRow(t *testing.T) {
	f := carveRefusedFix(t)
	if _, err := f.st.db.Exec(`CREATE TRIGGER block_conv_removal BEFORE DELETE ON ai_chat_conversations BEGIN SELECT RAISE(ABORT, 'removal blocked'); END`); err != nil {
		t.Fatal(err)
	}
	_, err := f.st.SpawnForTask(f.scope(), "p", f.se.ID, f.ids[0], "brief")
	r := f.row(t, 0)
	if r.State != ChildSpawning || r.PendingConversationID == "" {
		t.Fatalf("row = %+v, want spawning and still naming the conversation", r)
	}
	if !errors.Is(err, ErrChildrenFull) {
		t.Fatalf("err = %v, want ErrChildrenFull", err)
	}
	conv := r.PendingConversationID
	if _, err := f.st.db.Exec(`DROP TRIGGER block_conv_removal`); err != nil {
		t.Fatal(err)
	}
	real := time.Now()
	f.st.now = func() time.Time { return real.Add(30 * time.Minute) }
	f.h.missionChildPass()
	if r := f.row(t, 0); r.State == ChildSpawning {
		t.Fatalf("the stale pass left the row spawning: %+v", r)
	}
	if x, ok, _ := f.st.GetSession("p", conv); ok && x.State != StateStopped {
		t.Fatalf("the conversation the failed delete left is %s, want stopped", x.State)
	}
}
