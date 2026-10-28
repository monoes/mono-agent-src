package coderhost

import "testing"

// A person's pause still waiting when the pass pauses a paused mission's child must survive the mission running.
func TestAPersonsPendingPauseSurvivesCascadePauseAndResume(t *testing.T) {
	f := newHostFix(t)
	a := f.task(t, 0, 0)
	if err := f.h.Tick(); err != nil {
		t.Fatal(err)
	}
	if _, err := f.st.Enqueue(Command{ProfileID: "p", ConversationID: a.ConversationID, Kind: KindPause}); err != nil {
		t.Fatal(err)
	}
	if _, err := f.st.db.Exec(`UPDATE task_sessions SET state = 'paused' WHERE id = ?`, f.se.ID); err != nil {
		t.Fatal(err)
	}
	f.h.missionChildPass() // the person's pause has not run yet: the pass queues its own
	if err := f.h.Tick(); err != nil {
		t.Fatal(err)
	}
	if state(t, f.st, a.ConversationID).State != StatePaused {
		t.Fatal("the child was not paused")
	}
	if _, err := f.st.db.Exec(`UPDATE task_sessions SET state = 'running' WHERE id = ?`, f.se.ID); err != nil {
		t.Fatal(err)
	}
	f.h.missionChildPass()
	if err := f.h.Tick(); err != nil {
		t.Fatal(err)
	}
	if state(t, f.st, a.ConversationID).State != StatePaused {
		t.Fatal("the mission running lifted a pause a person made")
	}
	f.finish()
}
