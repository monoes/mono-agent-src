package missionpursuit

import (
	"strings"
	"testing"
)

// The guards are in the database, so that no code path of any process can get around them: these tests use raw SQL.
func confirmedMission(t *testing.T) fixture {
	t.Helper()
	f := newMission(t)
	g := mustPropose(t, f, goodCriteria(), lead)
	if _, err := f.store.Confirm(bg, "default", f.session, g.Hash, human); err != nil {
		t.Fatal(err)
	}
	return f
}

func mustRefuse(t *testing.T, f fixture, why, q string, args ...any) {
	t.Helper()
	if _, err := f.db.Exec(q, args...); err == nil {
		t.Errorf("%s: the database accepted %s", why, q)
	}
}

func TestConfirmedCriteriaImmutableAtDatabase(t *testing.T) {
	for _, lock := range []bool{false, true} {
		f := confirmedMission(t)
		if lock {
			if _, err := f.store.Lock(bg, "default", f.session, human); err != nil {
				t.Fatal(err)
			}
		}
		s := f.session
		before := count(t, f.db, `SELECT COUNT(*) FROM mission_goal_criteria`)
		mustRefuse(t, f, "edit a criterion", `UPDATE mission_goal_criteria SET spec = 'true' WHERE session_id = ?`, s)
		mustRefuse(t, f, "edit a hash", `UPDATE mission_goal_criteria SET hash = 'x' WHERE session_id = ?`, s)
		mustRefuse(t, f, "delete a criterion", `DELETE FROM mission_goal_criteria WHERE session_id = ? AND pos = 1`, s)
		mustRefuse(t, f, "add a criterion", `INSERT INTO mission_goal_criteria (session_id, pos, kind, spec, expected, hash) VALUES (?, 3, 'rubric', 'extra', '', 'h')`, s)
		mustRefuse(t, f, "replace a criterion", `INSERT OR REPLACE INTO mission_goal_criteria (session_id, pos, kind, spec, expected, hash) VALUES (?, 1, 'rubric', 'other', '', 'h')`, s)
		mustRefuse(t, f, "change the goal hash", `UPDATE mission_goals SET criteria_hash = 'x' WHERE session_id = ?`, s)
		mustRefuse(t, f, "change who proposed", `UPDATE mission_goals SET proposed_by = 'someone' WHERE session_id = ?`, s)
		mustRefuse(t, f, "change who confirmed", `UPDATE mission_goals SET confirmed_by = 'someone' WHERE session_id = ?`, s)
		mustRefuse(t, f, "delete the goal", `DELETE FROM mission_goals WHERE session_id = ?`, s)
		mustRefuse(t, f, "replace the goal", `INSERT OR REPLACE INTO mission_goals (session_id, state, criteria_hash, proposed_by, created_at, updated_at) VALUES (?, 'proposed', 'x', 'a', 'x', 'x')`, s)
		if after := count(t, f.db, `SELECT COUNT(*) FROM mission_goal_criteria`); after != before {
			t.Errorf("lock=%t: criteria rows %d -> %d", lock, before, after)
		}
	}
}

func TestStateMovesForwardOnly(t *testing.T) {
	f := confirmedMission(t)
	s := f.session
	mustRefuse(t, f, "confirmed back to proposed", `UPDATE mission_goals SET state = 'proposed' WHERE session_id = ?`, s)
	if _, err := f.store.Lock(bg, "default", s, human); err != nil {
		t.Fatal(err)
	}
	mustRefuse(t, f, "locked back to confirmed", `UPDATE mission_goals SET state = 'confirmed' WHERE session_id = ?`, s)
	mustRefuse(t, f, "locked back to proposed", `UPDATE mission_goals SET state = 'proposed' WHERE session_id = ?`, s)
	mustRefuse(t, f, "locked_at moved", `UPDATE mission_goals SET locked_at = 'later' WHERE session_id = ?`, s)

	g := newMission(t)
	mustPropose(t, g, goodCriteria(), lead)
	mustRefuse(t, g, "proposed straight to locked", `UPDATE mission_goals SET state = 'locked', locked_at = 'x' WHERE session_id = ?`, g.session)
	mustRefuse(t, g, "a row born confirmed", `INSERT INTO mission_goals (session_id, state, criteria_hash, proposed_by, confirmed_by, confirmed_at, created_at, updated_at) VALUES (?, 'confirmed', 'x', 'a', 'you', 'x', 'x', 'x')`, addSession(t, g.db, "default", "mission", "chat1", "running"))
	mustRefuse(t, g, "a row born locked", `INSERT INTO mission_goals (session_id, state, criteria_hash, proposed_by, created_at, updated_at) VALUES (?, 'locked', 'x', 'a', 'x', 'x')`, addSession(t, g.db, "default", "mission", "chat1", "running"))
	mustRefuse(t, g, "an unknown state", `UPDATE mission_goals SET state = 'draft' WHERE session_id = ?`, g.session)
}

func TestConfirmNeedsAPersonAndARubricAtDatabase(t *testing.T) {
	f := newMission(t)
	mustPropose(t, f, goodCriteria(), lead)
	s := f.session
	mustRefuse(t, f, "confirm with nobody named", `UPDATE mission_goals SET state = 'confirmed' WHERE session_id = ?`, s)
	mustRefuse(t, f, "confirm with a blank name", `UPDATE mission_goals SET state = 'confirmed', confirmed_by = '', confirmed_at = 'x' WHERE session_id = ?`, s)
	mustRefuse(t, f, "confirm without a time", `UPDATE mission_goals SET state = 'confirmed', confirmed_by = 'you' WHERE session_id = ?`, s)
	// a goal whose criteria hold no rubric cannot be confirmed by any writer
	mustExec(t, f.db, `DELETE FROM mission_goal_criteria WHERE session_id = ? AND kind = 'rubric'`, s)
	mustRefuse(t, f, "confirm with no rubric", `UPDATE mission_goals SET state = 'confirmed', confirmed_by = 'you', confirmed_at = 'x' WHERE session_id = ?`, s)
	mustRefuse(t, f, "a criterion of an unknown kind", `INSERT INTO mission_goal_criteria (session_id, pos, kind, spec, expected, hash) VALUES (?, 5, 'test', 'TestX', '', 'h')`, s)
}

func TestCriteriaNeedTheirGoal(t *testing.T) {
	f := newMission(t)
	_, err := f.db.Exec(`INSERT INTO mission_goal_criteria (session_id, pos, kind, spec, expected, hash) VALUES (?, 1, 'rubric', 'x', '', 'h')`, f.session)
	if err == nil || !strings.Contains(strings.ToLower(err.Error()), "foreign key") {
		t.Errorf("a criterion with no goal: %v", err)
	}
}
