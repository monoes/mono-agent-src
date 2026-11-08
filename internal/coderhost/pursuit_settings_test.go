package coderhost

import (
	"errors"
	"testing"
	"time"
)

// pursueConversation makes conversation conv the chat of a mission whose pursuit is started (enabled), by raw SQL: a
// session, a confirmed goal, and the pursuit row. paused leaves the pursuit paused.
func pursueConversation(t *testing.T, st *Store, conv string, enabled, paused bool) {
	t.Helper()
	for _, q := range []string{
		`INSERT OR IGNORE INTO profiles (id, name) VALUES ('p', 'p')`,
		`INSERT INTO task_sessions (id, profile_id, conversation_id, title, state, kind, created_by, created_at, updated_at) VALUES (77, 'p', '` + conv + `', 't', 'running', 'mission', 'you', 'x', 'x')`,
		`INSERT INTO mission_goals (session_id, state, criteria_hash, proposed_by, created_at, updated_at) VALUES (77, 'proposed', 'h', 'lead', 'x', 'x')`,
		`INSERT INTO mission_goal_criteria (session_id, pos, kind, spec, expected, hash) VALUES (77, 1, 'rubric', 'ok', '', 'h')`,
		`UPDATE mission_goals SET state = 'confirmed', confirmed_by = 'you', confirmed_at = 'x' WHERE session_id = 77`,
	} {
		if _, err := st.db.Exec(q); err != nil {
			t.Fatalf("%s: %v", q, err)
		}
	}
	by, on := "", 0
	if paused {
		by = "you"
	}
	q := `INSERT INTO mission_pursuit (session_id, enabled, paused_by, locked_by, created_at, updated_at) VALUES (77, 0, ?, '', 'x', 'x')`
	if enabled {
		on = 1
		q = `INSERT INTO mission_pursuit (session_id, enabled, locked_by, created_at, updated_at) VALUES (77, 1, 'you', 'x', 'x')`
		if _, err := st.db.Exec(q); err != nil {
			t.Fatal(err)
		}
		if paused {
			if _, err := st.db.Exec(`UPDATE mission_pursuit SET paused_by = 'you' WHERE session_id = 77`); err != nil {
				t.Fatal(err)
			}
		}
		return
	}
	if _, err := st.db.Exec(q, by); err != nil {
		t.Fatal(err)
	}
	_ = on
}

// A started pursuit, running or paused, changes nothing about who answers a gap or what a waiting question does: the
// non-hard classes may take their default after ParkTimeout, the hard ones never do and wait for the person.
func TestAPursuitLeavesTheQuestionPolicyAsItIs(t *testing.T) {
	for _, state := range []string{"none", "started", "paused"} {
		st, clk := parkStore(t)
		addConversation(t, st, "a", "/w/a")
		setSettings(t, st, "a", `{"autonomy_preset": "decide-and-log", "autonomy_classes": {"ambiguity": {"who": "user", "fallback": "take_default"}}}`)
		switch state {
		case "started":
			pursueConversation(t, st, "a", true, false)
		case "paused":
			pursueConversation(t, st, "a", true, true)
		}
		soft, err := st.Park("p", "a", "ambiguity", "which port?", []string{"80", "8080"}, "8080", false)
		if err != nil {
			t.Fatalf("%s: %v", state, err)
		}
		hard, err := st.Park("p", "a", "risky_action", "delete it?", []string{"yes", "no"}, "yes", false)
		if err != nil {
			t.Fatalf("%s: %v", state, err)
		}
		clk.t = clk.t.Add(ParkTimeout + time.Second)
		n, err := st.ApplyDefaults()
		if err != nil {
			t.Fatal(err)
		}
		if got := questionOf(t, st, soft.ID).Status; n != 1 || got == "open" {
			t.Errorf("%s: %d defaults applied, soft question %s: a non-hard class still takes its default", state, n, got)
		}
		if got := questionOf(t, st, hard.ID).Status; got != "open" {
			t.Errorf("%s: the hard question is %s: it waits for the person", state, got)
		}
	}
}

// The own-decision paths are what they are without a pursuit: no master means the model decides a soft gap and records
// it, a hard class is never recorded, and a started pursuit does not change either.
func TestAPursuitLeavesTheOwnDecisionPathsAsTheyAre(t *testing.T) {
	for _, pursue := range []bool{false, true} {
		st, _ := parkStore(t)
		addConversation(t, st, "a", "/w/a")
		if pursue {
			pursueConversation(t, st, "a", true, false)
		}
		c := Caller{ProfileID: "p", ConversationID: "a"}
		if _, err := st.ParkOwn(c, ParkRequest{Class: "ambiguity", Question: "which port?"}); !errors.Is(err, ErrNoMasterDecide) {
			t.Errorf("pursue=%v: ParkOwn of a soft class with no master = %v, want ErrNoMasterDecide", pursue, err)
		}
		if _, err := st.RecordOwn(c, RecordRequest{Class: "ambiguity", Question: "q", Choice: "x"}); err != nil {
			t.Errorf("pursue=%v: RecordOwn of a soft class: %v", pursue, err)
		}
		for _, class := range []string{"risky_action", "missing_credential", "plan_approval"} {
			if _, err := st.RecordOwn(c, RecordRequest{Class: class, Question: "q", Choice: "x"}); !errors.Is(err, ErrPolicyReserves) {
				t.Errorf("pursue=%v: RecordOwn(%s) = %v, want ErrPolicyReserves (the hard-class rule)", pursue, class, err)
			}
		}
	}
}
