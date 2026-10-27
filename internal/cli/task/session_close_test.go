package task

import (
	"strings"
	"testing"
)

type sessionEventsDoc struct {
	Events []struct {
		Kind  string `json:"kind"`
		Actor string `json:"actor"`
		Note  string `json:"note"`
	} `json:"events"`
}

func runningSession(t *testing.T, db string) int64 {
	t.Helper()
	s := sessionNew(t, db)
	sessionAdd(t, db, s, "t")
	var d sessionDoc
	mustTaskJSON(t, db, "default", &d, "", "session", "approve", id(s))
	return s
}

// close takes a running or paused session to done, as the operator, with the note in its history.
func TestSessionCloseTakesRunningAndPausedToDone(t *testing.T) {
	t.Parallel()
	db := newTaskTestDB(t)
	run, paused := runningSession(t, db), runningSession(t, db)
	var d sessionDoc
	mustTaskJSON(t, db, "default", &d, "", "session", "pause", id(paused))
	for _, s := range []int64{run, paused} {
		var got sessionDoc
		mustTaskJSON(t, db, "default", &got, "", "session", "close", id(s), "--note", "looks good")
		if got.Session.State != "done" {
			t.Fatalf("close #%d: %+v", s, got.Session)
		}
		if st := sessionState(t, db, s); st.Session.State != "done" {
			t.Fatalf("stored state %s", st.Session.State)
		}
		var ev sessionEventsDoc
		mustTaskJSON(t, db, "default", &ev, "", "session", "show", id(s))
		found := false
		for _, e := range ev.Events {
			found = found || e.Kind == "close" && strings.Contains(e.Note, "looks good")
		}
		if !found {
			t.Fatalf("no close event with the note: %+v", ev.Events)
		}
	}
}

// close refuses a session in any other state, and leaves it as it was.
func TestSessionCloseRefusesOtherStates(t *testing.T) {
	t.Parallel()
	db := newTaskTestDB(t)
	draft := sessionNew(t, db)
	stopped, done := runningSession(t, db), runningSession(t, db)
	var d sessionDoc
	mustTaskJSON(t, db, "default", &d, "", "session", "stop", id(stopped))
	mustTaskJSON(t, db, "default", &d, "", "session", "close", id(done))
	for name, s := range map[string]int64{"draft": draft, "stopped": stopped, "done": done} {
		want := sessionState(t, db, s).Session.State
		doc := failedTaskJSON(t, db, "default", 3, "session", "close", id(s))
		if doc["error"] == nil || doc["code"] == nil || doc["code"] == "operator_only" {
			t.Errorf("%s: %v", name, doc)
		}
		if got := sessionState(t, db, s).Session.State; got != want {
			t.Errorf("%s: state %s after a refused close, was %s", name, got, want)
		}
	}
	if doc := failedTaskJSON(t, db, "default", 2, "session", "close", "999"); doc["code"] != "not_found" {
		t.Errorf("unknown session: %v", doc)
	}
}

// An agent, in any context, cannot close: the session stays running.
func TestSessionCloseRefusesEveryAgentContext(t *testing.T) {
	t.Parallel()
	for name, setup := range opsAgentContexts {
		t.Run(name, func(t *testing.T) {
			db := newTaskTestDB(t)
			s := runningSession(t, db)
			extra := setup(t)
			doc := failedTaskJSON(t, db, "default", 3, append([]string{"session", "close", id(s)}, extra...)...)
			if doc["code"] != "operator_only" {
				t.Errorf("%v", doc)
			}
			if st := sessionState(t, db, s); st.Session.State != "running" {
				t.Errorf("an agent closed the session: %+v", st.Session)
			}
		})
	}
}
