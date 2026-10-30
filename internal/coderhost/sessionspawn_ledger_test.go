package coderhost

import (
	"context"
	"fmt"
	"testing"

	"github.com/monoes/mono-agent/internal/tasks"
)

// A spawn that fails (bad access here) writes no acceptance: the acceptance is a master row nothing rotates out, so a
// loop of failures must not push an older decision out of the audit trail.
func TestFailingSpawnsWriteNoAcceptanceAndKeepOlderMasterRows(t *testing.T) {
	f := newMC(t, true)
	old, err := f.st.Record(LedgerEntry{ProfileID: "p", ConversationID: f.lead.ConversationID, Class: "ambiguity", MadeBy: WhoMaster, Question: "q", Choice: "an earlier decision", Reversible: false})
	if err != nil {
		t.Fatal(err)
	}
	for i := 0; i < MaxDecisionsPerChat+5; i++ {
		r := SessionSpawnRequest{Title: fmt.Sprintf("d%d", i), Goal: "g", Kind: tasks.SessionKindDirection, Reason: ReasonParallel, TaskIDs: f.bigScope(), Access: "bogus"}
		if _, err := f.st.SpawnSession(context.Background(), f.scope(), "p", r); err == nil {
			t.Fatal("a spawn with bad access was accepted")
		}
	}
	if _, ok, _ := f.st.GetDecision("p", f.lead.ConversationID, old.ID); !ok {
		t.Fatal("the older irreversible master row was rotated out by failing spawns")
	}
	if n := countRows(t, f.st, "SELECT COUNT(*) FROM coder_session_decisions WHERE conversation_id = ? AND choice = '"+AdviceAccept+"'", f.lead.ConversationID); n != 0 {
		t.Fatalf("%d acceptance rows for spawns that were not made", n)
	}
}

// A spawn that is made writes exactly one acceptance, as the master.
func TestASpawnThatIsMadeWritesOneAcceptance(t *testing.T) {
	f := newMC(t, true)
	r := SessionSpawnRequest{Title: "d", Goal: "g", Kind: tasks.SessionKindDirection, Reason: ReasonParallel, TaskIDs: f.bigScope()}
	if _, err := f.st.SpawnSession(context.Background(), f.scope(), "p", r); err != nil {
		t.Fatal(err)
	}
	if n := countRows(t, f.st, "SELECT COUNT(*) FROM coder_session_decisions WHERE conversation_id = ? AND choice = '"+AdviceAccept+"' AND made_by = 'master'", f.lead.ConversationID); n != 1 {
		t.Fatalf("%d acceptance rows, want 1", n)
	}
}

// An acceptance that cannot be recorded is not made: no child session is left behind.
func TestASpawnWhoseAcceptanceCannotBeRecordedIsNotMade(t *testing.T) {
	f := newMC(t, true)
	if _, err := f.st.db.Exec(`CREATE TRIGGER rv_ledger BEFORE INSERT ON coder_session_decisions BEGIN SELECT RAISE(ABORT, 'ledger down'); END`); err != nil {
		t.Fatal(err)
	}
	r := SessionSpawnRequest{Title: "d", Goal: "g", Kind: tasks.SessionKindDirection, Reason: ReasonParallel, TaskIDs: f.bigScope()}
	if _, err := f.st.SpawnSession(context.Background(), f.scope(), "p", r); err == nil {
		t.Fatal("the spawn was made without its acceptance being recorded")
	}
	if n := countRows(t, f.st, `SELECT COUNT(*) FROM coder_sessions WHERE parent_conversation_id != ''`); n != 0 {
		t.Fatalf("%d child sessions left behind", n)
	}
	if n := countRows(t, f.st, `SELECT COUNT(*) FROM task_sessions WHERE parent_session_id IS NOT NULL AND state NOT IN ('stopped','done','failed')`); n != 0 {
		t.Fatalf("%d open child task sessions left behind", n)
	}
}
