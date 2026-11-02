package task

import (
	"encoding/json"
	"testing"
)

func sessionRev(t *testing.T, db string, session int64) int64 {
	t.Helper()
	var d map[string]json.RawMessage
	mustTaskJSON(t, db, "default", &d, "", "session", "show", id(session))
	var se struct {
		Rev int64 `json:"rev"`
	}
	if err := json.Unmarshal(d["session"], &se); err != nil || se.Rev == 0 {
		t.Fatalf("no rev in the session JSON: %v %s", err, d["session"])
	}
	return se.Rev
}

// Review 2, F4: the board approves what it shows. `session approve --rev N` is refused with
// plan_changed when a lead changed the draft after the revision N was read.
func TestSessionApproveWithARevRefusesAPlanThatMoved(t *testing.T) {
	t.Parallel()
	db := newTaskTestDB(t)
	s := sessionNew(t, db, "draft")
	sessionAdd(t, db, s, "a")
	rev := sessionRev(t, db, s)
	sessionAdd(t, db, s, "swapped in")
	doc := failedTaskJSON(t, db, "default", 3, "session", "approve", id(s), "--rev", id(rev))
	if doc["code"] != "plan_changed" {
		t.Fatalf("want plan_changed: %v", doc)
	}
	if st := sessionState(t, db, s); st.Session.State != "draft" {
		t.Fatalf("approved a plan that moved: %+v", st.Session)
	}
}

func TestSessionApproveWithTheCurrentRevWorksAndWithoutOneStillWorks(t *testing.T) {
	t.Parallel()
	db := newTaskTestDB(t)
	s := sessionNew(t, db, "draft")
	sessionAdd(t, db, s, "a")
	var out map[string]map[string]any
	mustTaskJSON(t, db, "default", &out, "", "session", "approve", id(s), "--rev", id(sessionRev(t, db, s)))
	if out["session"]["state"] != "running" {
		t.Fatalf("session = %+v", out["session"])
	}
	s2 := sessionNew(t, db, "script")
	sessionAdd(t, db, s2, "a")
	mustTaskJSON(t, db, "default", &out, "", "session", "approve", id(s2))
	if out["session"]["state"] != "running" {
		t.Fatalf("session = %+v", out["session"])
	}
}
