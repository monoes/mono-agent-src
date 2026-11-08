package task

import (
	"strings"
	"testing"

	"github.com/monoes/mono-agent/internal/missionpursuit"
	"github.com/monoes/mono-agent/internal/storage"
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

func TestSessionPursuitStartRefusesAnExplicitZeroLimit(t *testing.T) {
	t.Parallel()
	db := newTaskTestDB(t)
	s, _ := confirmedGoalMission(t, db)
	for _, flag := range []string{"--parked-max", "--pct"} {
		if out, _, err := runTask(t, db, "default", true, "", "session", "pursuit", "start", id(s), flag, "0"); err == nil {
			t.Errorf("%s 0 was accepted: %s", flag, out)
		}
	}
	if pursuitExists(t, db, s) {
		t.Errorf("a refused start left a pursuit")
	}
}

func TestSessionPursuitStatusShowsHowFarEveryLimitHasComeAndWhyItWaits(t *testing.T) {
	t.Parallel()
	db := newTaskTestDB(t)
	s, _ := confirmedGoalMission(t, db)
	var st struct {
		Pursuit  missionpursuit.Pursuit  `json:"pursuit"`
		Progress missionpursuit.Progress `json:"progress"`
	}
	mustTaskJSON(t, db, "default", &st, "", "session", "pursuit", "start", id(s), "--wakes-max", "4")
	if st.Pursuit.WakesMax != 4 {
		t.Fatalf("start: %+v", st.Pursuit)
	}
	st.Progress = missionpursuit.Progress{}
	mustTaskJSON(t, db, "default", &st, "", "session", "pursuit", "status", id(s))
	if st.Progress.Seen || st.Progress.WakesMax != 4 || st.Progress.HostWakesMax != 12 || st.Progress.ParkedMax != 3 {
		t.Fatalf("progress before the loop looked: %+v", st.Progress)
	}
	text, _, err := runTask(t, db, "default", false, "", "session", "pursuit", "status", id(s))
	if err != nil || !strings.Contains(text, "has not looked at it yet") || !strings.Contains(text, "4 wake-up(s)") {
		t.Fatalf("text status: %v %q", err, text)
	}
	d, err := storage.NewDatabase(db)
	if err != nil {
		t.Fatal(err)
	}
	defer d.Close()
	if _, err := d.DB.Exec(`INSERT INTO mission_pursuit_state (session_id, waiting, wakes_since, parked, spent_away_micro, budget_micro, hour_wakes, hour_frees_at, updated_at, judge_micro, lead_micro, judge_calls, bad_rounds, hour_judges, last_round)
		VALUES (?, 'wakes', 4, 1, 2500000, 10000000, 3, '2026-11-08T13:00:00Z', '2026-11-08T12:00:00Z', 1500000, 700000, 3, 2, 5, 'invalid')`, s); err != nil {
		t.Fatal(err)
	}
	st.Progress = missionpursuit.Progress{}
	mustTaskJSON(t, db, "default", &st, "", "session", "pursuit", "status", id(s))
	if !st.Progress.Seen || st.Progress.WakesUsed != 4 || st.Progress.Waiting != "wakes" || st.Progress.SpentAwayUSD != 2.5 || st.Progress.HostWakes != 3 ||
		st.Progress.JudgeSpendUSD != 1.5 || st.Progress.LeadSpendUSD != 0.7 || st.Progress.JudgeCalls != 3 || st.Progress.BadRounds != 2 || st.Progress.BadRoundsMax != 3 || st.Progress.HostJudges != 5 || st.Progress.HostJudgesMax != 12 || st.Progress.LastRound != "invalid" {
		t.Fatalf("progress = %+v", st.Progress)
	}
	text, _, err = runTask(t, db, "default", false, "", "session", "pursuit", "status", id(s))
	for _, want := range []string{"4 of 4 wake-up(s)", "1 of 3 parked", "$2.50 metered spend", "$10.00 budget", "3 of 12 pursuit wake-up(s)", "2026-11-08T13:00:00Z", "waiting for you: the pursuit woke the lead", "judge cost $1.50 in 3 call(s)", "turns $0.70", "5 of 12 judge call(s)", "failed in a row: 2 of 3", "the last round ended: invalid", "closing the mission stays yours"} {
		if err != nil || !strings.Contains(text, want) {
			t.Errorf("text status lacks %q: %v\n%s", want, err, text)
		}
	}
}
