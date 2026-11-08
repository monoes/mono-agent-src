package missionpursuit

import (
	"testing"

	"github.com/monoes/mono-agent/internal/tasks"
)

func mustStart(t *testing.T, f fixture) Pursuit {
	t.Helper()
	p, err := f.store.Start(bg, "default", f.session, AwayLimits{}, human)
	if err != nil {
		t.Fatalf("start: %v", err)
	}
	return p
}

func goalState(t *testing.T, f fixture) string {
	t.Helper()
	g, err := f.store.Get(bg, "default", f.session)
	if err != nil {
		t.Fatal(err)
	}
	return g.State
}

func TestAgentCannotStartLockPauseOrResume(t *testing.T) {
	for _, by := range []tasks.Actor{lead, agent("x"), {}, {Kind: tasks.Capture, Name: "ext"}} {
		f := confirmedMission(t)
		_, err := f.store.Start(bg, "default", f.session, AwayLimits{}, by)
		wantIs(t, err, tasks.ErrOperatorOnly, "start by "+by.Name)
		if n := count(t, f.db, `SELECT COUNT(*) FROM mission_pursuit`); n != 0 {
			t.Errorf("an agent's start left %d rows", n)
		}
		if s := goalState(t, f); s != StateConfirmed {
			t.Errorf("an agent's start moved the goal to %s", s)
		}
		mustStart(t, f)
		_, err = f.store.Pause(bg, "default", f.session, by)
		wantIs(t, err, tasks.ErrOperatorOnly, "pause by "+by.Name)
		if _, err := f.store.Pause(bg, "default", f.session, human); err != nil {
			t.Fatal(err)
		}
		_, err = f.store.Resume(bg, "default", f.session, by)
		wantIs(t, err, tasks.ErrOperatorOnly, "resume by "+by.Name)
		if p, _ := f.store.GetPursuit(bg, "default", f.session); p.PausedBy == "" {
			t.Errorf("an agent resumed a paused pursuit")
		}
	}
}

func TestStartNeedsAConfirmedGoal(t *testing.T) {
	none := newMission(t)
	proposed := newMission(t)
	mustPropose(t, proposed, goodCriteria(), lead)
	for name, f := range map[string]fixture{"no goal": none, "a proposal": proposed} {
		if _, err := f.store.Start(bg, "default", f.session, AwayLimits{}, human); err == nil {
			t.Errorf("%s: start was accepted", name)
		}
		if n := count(t, f.db, `SELECT COUNT(*) FROM mission_pursuit`); n != 0 {
			t.Errorf("%s: %d rows", name, n)
		}
	}
	if s := goalState(t, proposed); s != StateProposed {
		t.Errorf("a refused start moved the goal to %s", s)
	}
}

func TestStartEnablesAndLocksTogether(t *testing.T) {
	f := confirmedMission(t)
	p := mustStart(t, f)
	if !p.Enabled || p.PausedBy != "" || p.LockedBy == "" || p.ParkedMax != 3 || p.Pct != 20 {
		t.Errorf("pursuit = %+v, want enabled, not paused, locked by the person, defaults 3 and 20", p)
	}
	if s := goalState(t, f); s != StateLocked {
		t.Errorf("goal = %s, want locked", s)
	}
	if _, err := f.store.Start(bg, "default", f.session, AwayLimits{}, human); err == nil {
		t.Errorf("a second start was accepted")
	}
	got, err := f.store.GetPursuit(bg, "default", f.session)
	if err != nil || got != p {
		t.Errorf("get = %+v, %v", got, err)
	}
}

func TestStartRefusesAGoalLockedOutsideIt(t *testing.T) {
	f := confirmedMission(t)
	if _, err := f.store.Lock(bg, "default", f.session, human); err != nil {
		t.Fatal(err)
	}
	if _, err := f.store.Start(bg, "default", f.session, AwayLimits{}, human); err == nil {
		t.Errorf("start on an already locked goal was accepted")
	}
	if n := count(t, f.db, `SELECT COUNT(*) FROM mission_pursuit`); n != 0 {
		t.Errorf("%d rows", n)
	}
}

func TestStartRefusesWhenChatModeIsNotMission(t *testing.T) {
	f := confirmedMission(t)
	f.store = f.store.WithModes(fixedModes("open"))
	if _, err := f.store.Start(bg, "default", f.session, AwayLimits{}, human); err == nil {
		t.Errorf("start in a chat that is not in mission mode was accepted")
	}
	if n := count(t, f.db, `SELECT COUNT(*) FROM mission_pursuit`); n != 0 {
		t.Errorf("%d rows", n)
	}
}

func TestAwayLimitsAreValidated(t *testing.T) {
	for _, c := range []struct {
		in      AwayLimits
		want    AwayLimits
		invalid bool
	}{
		{AwayLimits{}, AwayLimits{ParkedMax: 3, Pct: 20, Wakes: 6}, false},
		{AwayLimits{ParkedMax: 1, Pct: 1}, AwayLimits{ParkedMax: 1, Pct: 1, Wakes: 6}, false},
		{AwayLimits{ParkedMax: 20, Pct: 100}, AwayLimits{ParkedMax: 20, Pct: 100, Wakes: 6}, false},
		{AwayLimits{ParkedMax: 5, Pct: 0}, AwayLimits{ParkedMax: 5, Pct: 20, Wakes: 6}, false},
		{AwayLimits{ParkedMax: 0, Pct: 50}, AwayLimits{ParkedMax: 3, Pct: 50, Wakes: 6}, false},
		{AwayLimits{ParkedMax: -1, Pct: 20}, AwayLimits{}, true},
		{AwayLimits{ParkedMax: 21, Pct: 20}, AwayLimits{}, true},
		{AwayLimits{ParkedMax: 3, Pct: -1}, AwayLimits{}, true},
		{AwayLimits{ParkedMax: 3, Pct: 101}, AwayLimits{}, true},
		{AwayLimits{Wakes: 51}, AwayLimits{}, true},
		{AwayLimits{Wakes: -1}, AwayLimits{}, true},
		{AwayLimits{Wakes: 9}, AwayLimits{ParkedMax: 3, Pct: 20, Wakes: 9}, false},
	} {
		f := confirmedMission(t)
		p, err := f.store.Start(bg, "default", f.session, c.in, human)
		if c.invalid {
			wantIs(t, err, tasks.ErrInvalid, "limits")
			if n := count(t, f.db, `SELECT COUNT(*) FROM mission_pursuit`); n != 0 || goalState(t, f) != StateConfirmed {
				t.Errorf("%+v: a refused start left rows or locked the goal", c.in)
			}
			continue
		}
		if err != nil || (AwayLimits{ParkedMax: p.ParkedMax, Pct: p.Pct, Wakes: p.WakesMax}) != c.want {
			t.Errorf("%+v: got %+v, %v, want %+v", c.in, p, err, c.want)
		}
	}
}

func TestPauseAndResume(t *testing.T) {
	f := confirmedMission(t)
	if _, err := f.store.Pause(bg, "default", f.session, human); err == nil {
		t.Errorf("pause before any start was accepted")
	}
	mustStart(t, f)
	if _, err := f.store.Resume(bg, "default", f.session, human); err == nil {
		t.Errorf("resume of a running pursuit was accepted")
	}
	p, err := f.store.Pause(bg, "default", f.session, human)
	if err != nil || p.PausedBy == "" || !p.Enabled {
		t.Fatalf("pause = %+v, %v", p, err)
	}
	if _, err := f.store.Pause(bg, "default", f.session, human); err == nil {
		t.Errorf("a second pause was accepted")
	}
	p, err = f.store.Resume(bg, "default", f.session, human)
	if err != nil || p.PausedBy != "" || !p.Enabled {
		t.Fatalf("resume = %+v, %v", p, err)
	}
}

func TestGetPursuitOfAMissionWithoutOneIsNotFound(t *testing.T) {
	f := confirmedMission(t)
	_, err := f.store.GetPursuit(bg, "default", f.session)
	wantIs(t, err, tasks.ErrNotFound, "get")
}

// The rules below hold whatever code writes: raw SQL.
func TestPursuitCannotBeEnabledWithoutAConfirmedGoalAtDatabase(t *testing.T) {
	none := newMission(t)
	proposed := newMission(t)
	mustPropose(t, proposed, goodCriteria(), lead)
	for name, f := range map[string]fixture{"no goal": none, "proposal": proposed} {
		mustRefuse(t, f, name+": a row born enabled", `INSERT INTO mission_pursuit (session_id, enabled, locked_by, created_at, updated_at) VALUES (?, 1, 'you', 'x', 'x')`, f.session)
		mustExec(t, f.db, `INSERT INTO mission_pursuit (session_id, enabled, created_at, updated_at) VALUES (?, 0, 'x', 'x')`, f.session)
		mustRefuse(t, f, name+": enabling a row", `UPDATE mission_pursuit SET enabled = 1, locked_by = 'you' WHERE session_id = ?`, f.session)
	}
	// a confirmed goal lets it be enabled, but only with a named locker
	f := confirmedMission(t)
	mustRefuse(t, f, "enabled, nobody named", `INSERT INTO mission_pursuit (session_id, enabled, created_at, updated_at) VALUES (?, 1, 'x', 'x')`, f.session)
	mustExec(t, f.db, `INSERT INTO mission_pursuit (session_id, enabled, locked_by, created_at, updated_at) VALUES (?, 1, 'you', 'x', 'x')`, f.session)
	// enabled never goes back, even while the goal is still confirmed
	mustRefuse(t, f, "disable", `UPDATE mission_pursuit SET enabled = 0 WHERE session_id = ?`, f.session)
}

func TestPursuitRulesAtDatabase(t *testing.T) {
	f := confirmedMission(t)
	mustStart(t, f)
	s := f.session
	mustRefuse(t, f, "disable", `UPDATE mission_pursuit SET enabled = 0 WHERE session_id = ?`, s)
	mustRefuse(t, f, "change who locked", `UPDATE mission_pursuit SET locked_by = 'someone' WHERE session_id = ?`, s)
	mustRefuse(t, f, "clear who locked", `UPDATE mission_pursuit SET locked_by = '' WHERE session_id = ?`, s)
	mustRefuse(t, f, "raise parked max", `UPDATE mission_pursuit SET away_budget_parked_max = 20 WHERE session_id = ?`, s)
	mustRefuse(t, f, "raise pct", `UPDATE mission_pursuit SET away_budget_pct = 100 WHERE session_id = ?`, s)
	mustRefuse(t, f, "move the session", `UPDATE mission_pursuit SET session_id = session_id + 100 WHERE session_id = ?`, s)
	mustRefuse(t, f, "backdate", `UPDATE mission_pursuit SET created_at = 'y' WHERE session_id = ?`, s)
	mustRefuse(t, f, "remove the row", `DELETE FROM mission_pursuit WHERE session_id = ?`, s)
	mustRefuse(t, f, "replace", `INSERT OR REPLACE INTO mission_pursuit (session_id, enabled, locked_by, created_at, updated_at) VALUES (?, 1, 'x', 'x', 'x')`, s)
	for _, bad := range []struct{ col, val string }{
		{"away_budget_parked_max", "0"}, {"away_budget_parked_max", "21"}, {"away_budget_pct", "0"}, {"away_budget_pct", "101"},
	} {
		mustRefuse(t, f, bad.col+"="+bad.val, `INSERT INTO mission_pursuit (session_id, `+bad.col+`, created_at, updated_at) VALUES (?, `+bad.val+`, 'x', 'x')`, addSession(t, f.db, "default", "mission", "chat1", "running"))
	}
	// pausing and resuming are the one thing that moves
	mustExec(t, f.db, `UPDATE mission_pursuit SET paused_by = 'you' WHERE session_id = ?`, s)
	mustExec(t, f.db, `UPDATE mission_pursuit SET paused_by = '' WHERE session_id = ?`, s)
}

func TestSessionDeleteCascadesToThePursuit(t *testing.T) {
	f := confirmedMission(t)
	mustStart(t, f)
	mustExec(t, f.db, `DELETE FROM task_sessions WHERE id = ?`, f.session)
	for _, tb := range []string{"mission_pursuit", "mission_goals", "mission_goal_criteria"} {
		if n := count(t, f.db, `SELECT COUNT(*) FROM `+tb); n != 0 {
			t.Errorf("%s keeps %d rows after the session was deleted", tb, n)
		}
	}
}
