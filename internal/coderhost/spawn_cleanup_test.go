package coderhost

import (
	"errors"
	"testing"
	"time"

	"github.com/monoes/mono-agent/internal/ai"
)

// A refused carve whose delete fails, recovered by the stale pass, is not an attempt.
func TestStalePassGivesBackTheAttemptOfARefusedCarve(t *testing.T) {
	f := carveRefusedFix(t)
	if _, err := f.st.db.Exec(`CREATE TRIGGER block_conv_removal BEFORE DELETE ON ai_chat_conversations BEGIN SELECT RAISE(ABORT, 'removal blocked'); END`); err != nil {
		t.Fatal(err)
	}
	if _, err := f.st.SpawnForTask(f.scope(), "p", f.se.ID, f.ids[0], "brief"); !errors.Is(err, ErrChildrenFull) {
		t.Fatalf("err = %v, want ErrChildrenFull", err)
	}
	conv := f.row(t, 0).PendingConversationID
	if _, err := f.st.db.Exec(`DROP TRIGGER block_conv_removal`); err != nil {
		t.Fatal(err)
	}
	real := time.Now()
	f.st.now = func() time.Time { return real.Add(30 * time.Minute) }
	f.h.missionChildPass()
	r := f.row(t, 0)
	if r.State == ChildSpawning || r.Attempts != 0 {
		t.Fatalf("row = %+v, want ended by the stale pass with the attempt given back", r)
	}
	if x, ok, _ := f.st.GetSession("p", conv); ok && x.State != StateStopped {
		t.Fatalf("the orphan is %s, want stopped", x.State)
	}
}

// A failure that is not a refused carve is still an attempt when the stale pass ends it.
func TestStalePassStillChargesAnOrdinaryOrphan(t *testing.T) {
	f := carveRefusedFix(t)
	f.st.afterChildCreated = nil
	f.st.SetBudget("p", f.lead.ConversationID, 10)
	row, err := f.st.Reserve(f.scope(), "p", f.se.ID, f.ids[0])
	if err != nil {
		t.Fatal(err)
	}
	out, err := f.st.Spawn(f.lead, SpawnRequest{BudgetUSD: 1})
	if err != nil {
		t.Fatal(err)
	}
	if err := f.st.notePending(f.scope(), "p", f.se.ID, f.ids[0], out.ConversationID, row.Generation); err != nil {
		t.Fatal(err)
	}
	real := time.Now()
	f.st.now = func() time.Time { return real.Add(30 * time.Minute) }
	f.h.missionChildPass()
	if r := f.row(t, 0); r.State != ChildFailed || r.Attempts != 1 {
		t.Fatalf("row = %+v, want failed with its attempt", r)
	}
}

// An older cleanup must not delete a conversation a newer reservation now owns through the store API.
func TestOlderCleanupDoesNotDeleteAConversationAnotherRowOwns(t *testing.T) {
	f := newHostFix(t)
	for i := 0; i < 2; i++ {
		if _, err := f.st.Mark(f.scope(), "p", f.se.ID, f.ids[i], MarkOptions{}); err != nil {
			t.Fatal(err)
		}
	}
	old, err := f.st.Reserve(f.scope(), "p", f.se.ID, f.ids[0])
	if err != nil {
		t.Fatal(err)
	}
	newer, err := f.st.Reserve(f.scope(), "p", f.se.ID, f.ids[1])
	if err != nil {
		t.Fatal(err)
	}
	out, err := f.st.Spawn(f.lead, SpawnRequest{})
	if err != nil {
		t.Fatal(err)
	}
	if err := f.st.NotePending(f.scope(), "p", f.se.ID, f.ids[1], out.ConversationID); err != nil {
		t.Fatal(err)
	}
	_ = newer
	_ = f.st.abandonSpawn(f.lead, "p", f.se.ID, f.ids[0], old.Generation, out.ConversationID, ErrInvalid)
	store, _ := ai.NewAIStore(f.st.db)
	if _, err := store.GetConversation(out.ConversationID, "p"); err != nil {
		t.Fatalf("the older cleanup deleted a conversation another reservation owns: %v", err)
	}
	if r := f.row(t, 1); r.PendingConversationID != out.ConversationID || r.State != ChildSpawning {
		t.Fatalf("the newer row = %+v, want it still holding the conversation", r)
	}
}

// The ownership check and the delete are one transaction: a reservation that takes the conversation after the
// cleanup looked (here: just before its delete) keeps it, and its row stays intact.
func TestCleanupDoesNotDeleteAConversationTakenJustBeforeTheDelete(t *testing.T) {
	f := newHostFix(t)
	for i := 0; i < 2; i++ {
		if _, err := f.st.Mark(f.scope(), "p", f.se.ID, f.ids[i], MarkOptions{}); err != nil {
			t.Fatal(err)
		}
	}
	old, err := f.st.Reserve(f.scope(), "p", f.se.ID, f.ids[0])
	if err != nil {
		t.Fatal(err)
	}
	if _, err := f.st.Reserve(f.scope(), "p", f.se.ID, f.ids[1]); err != nil {
		t.Fatal(err)
	}
	out, err := f.st.Spawn(f.lead, SpawnRequest{})
	if err != nil {
		t.Fatal(err)
	}
	f.st.beforeCleanupDelete = func() {
		if err := f.st.NotePending(f.scope(), "p", f.se.ID, f.ids[1], out.ConversationID); err != nil {
			t.Error(err)
		}
	}
	_ = f.st.abandonSpawn(f.lead, "p", f.se.ID, f.ids[0], old.Generation, out.ConversationID, ErrInvalid)
	store, _ := ai.NewAIStore(f.st.db)
	if _, err := store.GetConversation(out.ConversationID, "p"); err != nil {
		t.Fatalf("the cleanup deleted a conversation another reservation took before its delete: %v", err)
	}
	if r := f.row(t, 1); r.PendingConversationID != out.ConversationID || r.State != ChildSpawning {
		t.Fatalf("the newer row = %+v, want it still holding the conversation", r)
	}
}
