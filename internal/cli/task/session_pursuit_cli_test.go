package task

import (
	"strings"
	"testing"

	"github.com/monoes/mono-agent/internal/missionpursuit"
)

type pursuitDoc struct {
	Pursuit missionpursuit.Pursuit `json:"pursuit"`
}

// confirmedGoalMission is a mission whose goal the person set (confirmed), and the lead's session token.
func confirmedGoalMission(t *testing.T, db string) (int64, string) {
	t.Helper()
	s, tok := goalMission(t, db, "mission")
	var g goalDoc
	mustTaskJSON(t, db, "default", &g, goalJSON, "session", "goal", "set", id(s), "--criteria", "-")
	return s, tok
}

func pursuitExists(t *testing.T, db string, s int64) bool {
	t.Helper()
	_, _, err := runTask(t, db, "default", true, "", "session", "pursuit", "status", id(s))
	return err == nil
}

func TestSessionPursuitStartPauseResumeStatus(t *testing.T) {
	t.Parallel()
	db := newTaskTestDB(t)
	s, _ := confirmedGoalMission(t, db)
	if out, _, err := runTask(t, db, "default", true, "", "session", "pursuit", "status", id(s)); err == nil {
		t.Errorf("status of a mission with no pursuit: %s", out)
	}
	var st pursuitDoc
	mustTaskJSON(t, db, "default", &st, "", "session", "pursuit", "start", id(s), "--parked-max", "5", "--pct", "30")
	p := st.Pursuit
	if !p.Enabled || p.ParkedMax != 5 || p.Pct != 30 || p.LockedBy != "you" || p.Paused() {
		t.Fatalf("start: %+v", p)
	}
	if g, _ := goalState(t, db, s); g.State != "locked" {
		t.Errorf("the goal after start: %s", g.State)
	}
	if out, _, err := runTask(t, db, "default", true, "", "session", "pursuit", "start", id(s)); err == nil {
		t.Errorf("a second start: %s", out)
	}
	st = pursuitDoc{}
	mustTaskJSON(t, db, "default", &st, "", "session", "pursuit", "pause", id(s))
	if !st.Pursuit.Paused() {
		t.Errorf("pause: %+v", st.Pursuit)
	}
	st = pursuitDoc{}
	mustTaskJSON(t, db, "default", &st, "", "session", "pursuit", "status", id(s))
	if !st.Pursuit.Paused() {
		t.Errorf("status after pause: %+v", st.Pursuit)
	}
	st = pursuitDoc{}
	mustTaskJSON(t, db, "default", &st, "", "session", "pursuit", "resume", id(s))
	if st.Pursuit.Paused() || !st.Pursuit.Enabled {
		t.Errorf("resume: %+v", st.Pursuit)
	}
	text, _, err := runTask(t, db, "default", false, "", "session", "pursuit", "status", id(s))
	if err != nil || !strings.Contains(text, "running") || !strings.Contains(text, "5") {
		t.Errorf("text status: %v %q", err, text)
	}
}

func TestSessionPursuitVerbsRefuseAnAgent(t *testing.T) {
	t.Parallel()
	db := newTaskTestDB(t)
	s, tok := confirmedGoalMission(t, db)
	asLead(t, tok)
	for _, verb := range []string{"start", "pause", "resume"} {
		out, _, err := runTask(t, db, "default", true, "", "session", "pursuit", verb, id(s), "--as", "lead")
		if exitCode(err) != 3 || !strings.Contains(out, `"operator_only"`) {
			t.Errorf("%s by the lead: exit %d %s", verb, exitCode(err), out)
		}
	}
	asOperator(t)
	if pursuitExists(t, db, s) {
		t.Errorf("an agent's start left a pursuit")
	}
	if g, _ := goalState(t, db, s); g.State != "confirmed" {
		t.Errorf("the goal after the agent's attempts: %s", g.State)
	}
}

func TestSessionPursuitStartRefusesAnUnconfirmedGoal(t *testing.T) {
	t.Parallel()
	db := newTaskTestDB(t)
	s, tok := goalMission(t, db, "mission")
	asLead(t, tok)
	mustTaskJSON(t, db, "default", &goalDoc{}, goalJSON, "session", "goal", "propose", id(s), "--criteria", "-", "--as", "lead")
	asOperator(t)
	if out, _, err := runTask(t, db, "default", true, "", "session", "pursuit", "start", id(s)); err == nil {
		t.Errorf("start on a proposal: %s", out)
	}
	if pursuitExists(t, db, s) {
		t.Errorf("a refused start left a pursuit")
	}
}
