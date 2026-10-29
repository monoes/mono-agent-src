package coderhost

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/monoes/mono-agent/internal/tasks"
)

// shadow links a plain board draft (kind board, made by something other than the mission planner) to the chat.
func (f *mcFix) shadow(t *testing.T) tasks.Session {
	t.Helper()
	se, err := f.ts.CreateSession(context.Background(), "p", tasks.SessionInput{Title: "mine", Goal: "g", ConversationID: f.lead.ConversationID}, tasks.Actor{Kind: tasks.Agent, Name: "agent-x", Conversation: f.lead.ConversationID})
	if err != nil {
		t.Fatal(err)
	}
	return se
}

// A newer plain board draft an agent linked to its own chat does not take the place of the chat's mission, for any of
// the selectors that read it.
func TestANewerPlainBoardDraftDoesNotShadowTheMission(t *testing.T) {
	f := newMC(t, true)
	sh := f.shadow(t)
	if sh.ID <= f.se.ID || sh.Kind == tasks.SessionKindMission {
		t.Fatalf("fixture: shadow %+v", sh)
	}
	if id, err := f.st.LeadMission(f.scope(), "p"); err != nil || id != f.se.ID {
		t.Fatalf("LeadMission = %d, %v; want the mission %d", id, err, f.se.ID)
	}
	if id, err := f.st.OperatorMissionOf("p", f.lead.ConversationID); err != nil || id != f.se.ID {
		t.Fatalf("OperatorMissionOf = %d, %v; want %d", id, err, f.se.ID)
	}
	if id, err := f.st.openMissionSession("p", f.lead.ConversationID); err != nil || id != f.se.ID {
		t.Fatalf("openMissionSession = %d, %v; want %d", id, err, f.se.ID)
	}
	h := &Host{Store: f.st, L: newFakeLauncher()}
	if m, ok := h.missionOf("p", f.lead.ConversationID); !ok || m == nil || m.Total != 2 {
		t.Fatalf("missionOf = %+v, %v; want the mission's two tasks", m, ok)
	}
}

// A plain board draft alone is no mission: the lead has none, and the chat is not held by it.
func TestAPlainBoardDraftAloneIsNoMission(t *testing.T) {
	st := newStoreT(t)
	lead, _ := master(t, st, "workspace")
	if _, err := st.db.Exec(`INSERT OR IGNORE INTO profiles (id, name) VALUES ('p', 'P')`); err != nil {
		t.Fatal(err)
	}
	ts := tasks.NewStore(st.db)
	if _, err := ts.CreateSession(context.Background(), "p", tasks.SessionInput{Title: "mine", Goal: "g", ConversationID: lead.ConversationID}, tasks.Actor{Kind: tasks.Agent, Name: "agent-x", Conversation: lead.ConversationID}); err != nil {
		t.Fatal(err)
	}
	if _, err := st.LeadMission(NewLeadScope(lead, BindingNone), "p"); !errors.Is(err, errMissionRefused) {
		t.Fatalf("LeadMission on a plain board draft: %v", err)
	}
	if id, _ := st.openMissionSession("p", lead.ConversationID); id != 0 {
		t.Fatalf("openMissionSession = %d for a plain board draft", id)
	}
}

// LeadMission and openMissionSession name the same session: when the newest open one is paused, the lead has none to
// write to (it does not fall back to an older running one).
func TestLeadMissionAndOpenMissionSessionAgreeOnAPausedNewest(t *testing.T) {
	f := newMC(t, true)
	if _, err := f.st.db.Exec(`UPDATE task_sessions SET state = 'paused' WHERE id = ?`, f.se.ID); err != nil {
		t.Fatal(err)
	}
	second, err := f.ts.CreateSession(context.Background(), "p", tasks.SessionInput{Title: "second", Goal: "g", ConversationID: f.lead.ConversationID, Kind: tasks.SessionKindMission}, opActor)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := f.st.db.Exec(`UPDATE task_sessions SET state = 'running' WHERE id = ?`, f.se.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := f.st.db.Exec(`UPDATE task_sessions SET state = 'paused' WHERE id = ?`, second.ID); err != nil {
		t.Fatal(err)
	}
	open, _ := f.st.openMissionSession("p", f.lead.ConversationID)
	id, err := f.st.LeadMission(f.scope(), "p")
	if open != second.ID {
		t.Fatalf("openMissionSession = %d, want the newest open %d", open, second.ID)
	}
	if err == nil {
		t.Fatalf("LeadMission = %d: it fell back to an older session than the newest open one (%d)", id, open)
	}
}

// A damaged parent link that loops back must not keep the check-in's tree read running for ever.
func TestDirectionFactsEndOnACyclicSessionTree(t *testing.T) {
	f := newMC(t, true)
	if _, err := f.st.db.Exec(`UPDATE task_sessions SET budget_usd = 10, spent_usd = 2 WHERE id = ?`, f.se.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := f.st.db.Exec(`UPDATE task_sessions SET parent_session_id = id WHERE id = ?`, f.se.ID); err != nil {
		t.Fatal(err)
	}
	h := &Host{Store: f.st, L: newFakeLauncher()}
	done := make(chan error, 1)
	go func() { _, _, _, err := h.directionFacts(f.se.ID); done <- err }()
	select {
	case err := <-done:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("directionFacts is still running on a cyclic session tree")
	}
}
