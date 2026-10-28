package coderhost

import (
	"strings"
	"testing"
	"time"
)

// A spawn whose reservation went stale and was taken again by a newer attempt must not touch the newer
// reservation: it links nothing, starts nothing, and releases only its own conversation.
func TestStaleSpawnCannotConsumeANewerReservation(t *testing.T) {
	f := newHostFix(t)
	if err := f.st.SetBudget("p", f.lead.ConversationID, 10); err != nil {
		t.Fatal(err)
	}
	if _, err := f.st.Mark(f.scope(), "p", f.se.ID, f.ids[0], MarkOptions{BudgetUSD: 4}); err != nil {
		t.Fatal(err)
	}
	var newer ChildRow
	f.st.afterChildCreated = func() {
		f.st.afterChildCreated = nil
		future := time.Now().Add(10 * time.Minute)
		f.st.now = func() time.Time { return future }
		if reset, _, err := f.st.ResetStaleSpawning(5 * time.Minute); err != nil || reset != 1 {
			t.Fatalf("reset=%d err=%v", reset, err)
		}
		f.setPolicy(t, OperatorScope{}, PolicyPatch{SharePct: ip(10)})
		if _, err := f.st.Mark(f.scope(), "p", f.se.ID, f.ids[0], MarkOptions{BudgetUSD: 1}); err != nil {
			t.Fatal(err)
		}
		var err error
		if newer, err = f.st.Reserve(f.scope(), "p", f.se.ID, f.ids[0]); err != nil || newer.Attempts != 2 {
			t.Fatalf("newer=%+v err=%v", newer, err)
		}
	}
	out, err := f.st.SpawnForTask(f.scope(), "p", f.se.ID, f.ids[0], "stale original spawn")
	if err == nil {
		t.Fatalf("the stale attempt started child %s with a $%v cap against the newer reservation: %+v", out.ConversationID, out.BudgetUSD, f.row(t, 0))
	}
	r := f.row(t, 0)
	if r.State != ChildSpawning || r.Attempts != 2 || r.ReservedUSD != newer.ReservedUSD || r.ChildConversationID != "" {
		t.Fatalf("the newer reservation was touched: %+v", r)
	}
	var n int
	if e := f.st.db.QueryRow(`SELECT COUNT(*) FROM coder_sessions WHERE parent_conversation_id = ?`, f.lead.ConversationID).Scan(&n); e != nil || n != 0 {
		t.Fatalf("the stale attempt left %d child sessions (%v)", n, e)
	}
}

// A finished task child lets go of its master's spawn slot; an idle child that is not a task's keeps it.
func TestCompletedMissionChildReleasesItsSpawnSlot(t *testing.T) {
	f := newHostFix(t)
	settings, err := f.st.GetSettings("p", f.lead.ConversationID)
	if err != nil {
		t.Fatal(err)
	}
	settings.MaxChildren = 1
	if err = f.st.SaveSettings("p", f.lead.ConversationID, settings); err != nil {
		t.Fatal(err)
	}
	out := f.task(t, 0, 0)
	f.ranTurn(t, out.ConversationID)
	if _, err = f.st.db.Exec(`UPDATE coder_session_commands SET status='done'`); err != nil {
		t.Fatal(err)
	}
	f.setTask(t, 0, "review")
	f.h.missionChildPass()
	if r := f.row(t, 0); r.State != ChildEnded {
		t.Fatalf("the child did not end: %+v", r)
	}
	if _, err = f.st.Mark(f.scope(), "p", f.se.ID, f.ids[1], MarkOptions{}); err != nil {
		t.Fatal(err)
	}
	if _, err = f.st.SpawnForTask(f.scope(), "p", f.se.ID, f.ids[1], "next task"); err != nil && strings.Contains(err.Error(), "child sessions") {
		t.Fatalf("a finished task child still holds the master's only child slot: %v", err)
	} else if err != nil {
		t.Fatal(err)
	}
}
