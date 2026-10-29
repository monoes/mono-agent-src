package coderhost

import (
	"fmt"
	"strings"
	"testing"
)

func cmdStatus(t *testing.T, st *Store, id int64) (string, string) {
	t.Helper()
	c, ok, err := st.GetCommand(id)
	if err != nil || !ok {
		t.Fatalf("command %d: %v %v", id, ok, err)
	}
	return c.Status, c.Error
}

func pausedChild(t *testing.T, f *hostFix) string {
	t.Helper()
	a := f.task(t, 0, 0)
	if err := f.h.Tick(); err != nil {
		t.Fatal(err)
	}
	if _, err := f.st.Enqueue(Command{ProfileID: "p", ConversationID: a.ConversationID, Kind: KindPause}); err != nil {
		t.Fatal(err)
	}
	if err := f.h.Tick(); err != nil {
		t.Fatal(err)
	}
	if state(t, f.st, a.ConversationID).State != StatePaused {
		t.Fatal("the person's pause did not pause the child")
	}
	return a.ConversationID
}

// The lead (a model) cannot lift a pause a person made; the person can.
func TestAMastersResumeDoesNotLiftAPersonsPause(t *testing.T) {
	f := newHostFix(t)
	conv := pausedChild(t, f)
	cmd, err := f.st.ControlChild(f.lead, conv, KindResume)
	if err != nil {
		t.Fatal(err)
	}
	if err := f.h.Tick(); err != nil {
		t.Fatal(err)
	}
	if state(t, f.st, conv).State != StatePaused {
		t.Fatal("a master's resume lifted a person's pause")
	}
	if status, why := cmdStatus(t, f.st, cmd.ID); status != CmdFailed || !strings.Contains(why, "person") {
		t.Fatalf("the refused resume = %s %q, want failed with the reason", status, why)
	}
	if _, err := f.st.Enqueue(Command{ProfileID: "p", ConversationID: conv, Kind: KindResume}); err != nil {
		t.Fatal(err)
	}
	if err := f.h.Tick(); err != nil {
		t.Fatal(err)
	}
	if state(t, f.st, conv).State == StatePaused {
		t.Fatal("a person's resume did not lift the pause")
	}
	f.finish()
}

// A master's own pause is the master's to lift.
func TestAMastersResumeLiftsAMastersPause(t *testing.T) {
	f := newHostFix(t)
	a := f.task(t, 0, 0)
	if err := f.h.Tick(); err != nil {
		t.Fatal(err)
	}
	if _, err := f.st.ControlChild(f.lead, a.ConversationID, KindPause); err != nil {
		t.Fatal(err)
	}
	if err := f.h.Tick(); err != nil {
		t.Fatal(err)
	}
	if _, err := f.st.ControlChild(f.lead, a.ConversationID, KindResume); err != nil {
		t.Fatal(err)
	}
	if err := f.h.Tick(); err != nil {
		t.Fatal(err)
	}
	if state(t, f.st, a.ConversationID).State == StatePaused {
		t.Fatal("a master's resume did not lift its own pause")
	}
	f.finish()
}

// The cascade pauses a paused mission's child and resumes it when the mission runs again.
func TestCascadePauseThenMissionResumeStillResumes(t *testing.T) {
	f := newHostFix(t)
	a := f.task(t, 0, 0)
	if err := f.h.Tick(); err != nil {
		t.Fatal(err)
	}
	set := func(s string) {
		if _, err := f.st.db.Exec(`UPDATE task_sessions SET state = ? WHERE id = ?`, s, f.se.ID); err != nil {
			t.Fatal(err)
		}
	}
	set("paused")
	f.h.missionChildPass()
	if err := f.h.Tick(); err != nil {
		t.Fatal(err)
	}
	if state(t, f.st, a.ConversationID).State != StatePaused {
		t.Fatal("the cascade did not pause the child")
	}
	set("running")
	f.h.missionChildPass()
	if err := f.h.Tick(); err != nil {
		t.Fatal(err)
	}
	if state(t, f.st, a.ConversationID).State == StatePaused {
		t.Fatal("the cascade did not resume the child it paused")
	}
	f.finish()
}

// A person's pause that lands between the pass's check and its resume is not undone.
func TestPersonsPauseBetweenCascadeCheckAndResumeSurvives(t *testing.T) {
	f := newHostFix(t)
	a := f.task(t, 0, 0)
	if err := f.h.Tick(); err != nil {
		t.Fatal(err)
	}
	set := func(s string) {
		if _, err := f.st.db.Exec(`UPDATE task_sessions SET state = ? WHERE id = ?`, s, f.se.ID); err != nil {
			t.Fatal(err)
		}
	}
	set("paused")
	f.h.missionChildPass()
	if err := f.h.Tick(); err != nil {
		t.Fatal(err)
	}
	set("running")
	f.st.betweenCascadeCheckAndResume = func() {
		if _, err := f.st.Enqueue(Command{ProfileID: "p", ConversationID: a.ConversationID, Kind: KindPause}); err != nil {
			t.Error(err)
		}
	}
	f.h.missionChildPass()
	f.st.betweenCascadeCheckAndResume = nil
	if f.st.hasPendingCascade("p", a.ConversationID, KindResume) {
		t.Fatal("the pass queued its resume behind a person's pause")
	}
	if err := f.h.Tick(); err != nil {
		t.Fatal(err)
	}
	if state(t, f.st, a.ConversationID).State != StatePaused {
		t.Fatalf("the cascade resume undid a person's pause:\n%s", f.commandsFor(t, a.ConversationID))
	}
	f.finish()
}

// A host restarted over the same database, with a model's or the cascade's resume still queued behind a
// person's pause, keeps the pause and ends the resume refused.
func TestRestartWithAQueuedModelResumeKeepsAPersonsPause(t *testing.T) {
	for _, name := range []string{"master", "cascade"} {
		for _, pauseDone := range []bool{true, false} {
			t.Run(fmt.Sprintf("%s/pauseDone=%v", name, pauseDone), func(t *testing.T) {
				f := newHostFix(t)
				a := f.task(t, 0, 0)
				if err := f.h.Tick(); err != nil {
					t.Fatal(err)
				}
				if _, err := f.st.Enqueue(Command{ProfileID: "p", ConversationID: a.ConversationID, Kind: KindPause}); err != nil {
					t.Fatal(err)
				}
				if pauseDone {
					if err := f.h.Tick(); err != nil {
						t.Fatal(err)
					}
				}
				var cmd Command
				var err error
				if name == "master" {
					cmd, err = f.st.ControlChild(f.lead, a.ConversationID, KindResume)
				} else {
					cmd, err = f.st.Enqueue(Command{ProfileID: "p", ConversationID: a.ConversationID, Kind: KindResume, TurnID: cascadeTurn})
				}
				if err != nil {
					t.Fatal(err)
				}
				f.finish()
				// the restart: a fresh store and host over the same database
				l := newFakeLauncher()
				h2 := &Host{Store: NewStore(f.st.db), L: l}
				if err := h2.Recover(); err != nil {
					t.Fatal(err)
				}
				if err := h2.Tick(); err != nil {
					t.Fatal(err)
				}
				if state(t, h2.Store, a.ConversationID).State != StatePaused {
					t.Fatal("the restarted host let a queued model resume lift a person's pause")
				}
				if status, _ := cmdStatus(t, h2.Store, cmd.ID); status != CmdFailed {
					t.Fatalf("the queued resume ended %s, want failed", status)
				}
			})
		}
	}
}
