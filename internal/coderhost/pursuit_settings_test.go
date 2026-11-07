package coderhost

import (
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

func TestSettingsCarryThePursuitFlagFromTheStore(t *testing.T) {
	for _, c := range []struct {
		name            string
		row             bool
		enabled, paused bool
		want            bool
	}{
		{"no pursuit row", false, false, false, false},
		{"a row not enabled", true, false, false, false},
		{"started", true, true, false, true},
		{"started and paused", true, true, true, true},
	} {
		st, _ := parkStore(t)
		addConversation(t, st, "a", "/w/a")
		if c.row {
			pursueConversation(t, st, "a", c.enabled, c.paused)
		}
		s, err := st.GetSettings("p", "a")
		if err != nil || s.pursuing != c.want {
			t.Errorf("%s: pursuing = %v, %v, want %v", c.name, s.pursuing, err, c.want)
		}
	}
}

// The flag is never stored in the settings document, and a document cannot set it.
func TestThePursuitFlagIsNotPartOfTheStoredSettings(t *testing.T) {
	st, _ := parkStore(t)
	addConversation(t, st, "a", "/w/a")
	pursueConversation(t, st, "a", true, false)
	s, _ := st.GetSettings("p", "a")
	if err := st.SaveSettings("p", "a", s); err != nil {
		t.Fatal(err)
	}
	var raw string
	if err := st.db.QueryRow(`SELECT settings FROM coder_sessions WHERE conversation_id = 'a'`).Scan(&raw); err != nil {
		t.Fatal(err)
	}
	if containsFold(raw, "pursu") {
		t.Errorf("the stored settings mention the pursuit: %s", raw)
	}
	cur := DefaultSettings()
	next, _, err := cur.Apply([]byte(`{"pursuing": true, "Pursuing": true}`))
	if err == nil && next.pursuing {
		t.Errorf("a settings document set the pursuit flag")
	}
}

func containsFold(s, sub string) bool {
	for i := 0; i+len(sub) <= len(s); i++ {
		if equalFold(s[i:i+len(sub)], sub) {
			return true
		}
	}
	return false
}

func equalFold(a, b string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		x, y := a[i], b[i]
		if 'A' <= x && x <= 'Z' {
			x += 'a' - 'A'
		}
		if 'A' <= y && y <= 'Z' {
			y += 'a' - 'A'
		}
		if x != y {
			return false
		}
	}
	return true
}

// In a pursuit the person is the only answerer: a question the policy would let the model settle is parked for them, and
// no default is taken when nobody answers within ParkTimeout. The same question in a mission with no pursuit takes it.
func TestAPursuitParksEveryQuestionForThePersonAndTakesNoDefault(t *testing.T) {
	for _, pursue := range []bool{false, true} {
		st, clk := parkStore(t)
		addConversation(t, st, "a", "/w/a")
		setSettings(t, st, "a", `{"autonomy_preset": "decide-and-log", "autonomy_classes": {"ambiguity": {"who": "user", "fallback": "take_default"}}}`)
		if pursue {
			pursueConversation(t, st, "a", true, false)
		}
		q, err := st.Park("p", "a", "ambiguity", "which port?", []string{"80", "8080"}, "8080", false)
		if err != nil {
			t.Fatalf("pursue=%v: %v", pursue, err)
		}
		clk.t = clk.t.Add(ParkTimeout + time.Second)
		n, err := st.ApplyDefaults()
		if err != nil {
			t.Fatal(err)
		}
		status := questionOf(t, st, q.ID).Status
		if pursue && (n != 0 || status != "open") {
			t.Errorf("in a pursuit: %d defaults applied, status %s: the question must wait for the person", n, status)
		}
		if !pursue && (n != 1 || status == "open") {
			t.Errorf("without a pursuit: %d defaults applied, status %s: the control lost its default", n, status)
		}
	}
}

// A class the preset would give to the model is parked, not decided, in a pursuit (Park refuses it only when the model decides).
func TestAPursuitParkRefusesNothingTheModelWouldHaveDecided(t *testing.T) {
	st, _ := parkStore(t)
	addConversation(t, st, "a", "/w/a")
	setSettings(t, st, "a", `{"autonomy_preset": "decide-and-log"}`)
	if _, err := st.Park("p", "a", "ambiguity", "which?", nil, "", true); err == nil {
		t.Fatal("control: decide-and-log should let the model decide an ambiguity (ErrModelDecides)")
	}
	pursueConversation(t, st, "a", true, false)
	if _, err := st.Park("p", "a", "ambiguity", "which?", nil, "", true); err != nil {
		t.Errorf("in a pursuit the question is parked for the person, got %v", err)
	}
}

// A pursuit that cannot be read counts as started (the safe side), and the settings are still returned.
func TestAnUnreadablePursuitCountsAsStartedAndKeepsTheSettings(t *testing.T) {
	st, _ := parkStore(t)
	addConversation(t, st, "a", "/w/a")
	if _, err := st.db.Exec(`ALTER TABLE mission_pursuit RENAME TO mission_pursuit_away`); err != nil {
		t.Fatal(err)
	}
	s, err := st.GetSettings("p", "a")
	if err != nil || !s.pursuing {
		t.Errorf("pursuing = %v, err = %v, want started and no error", s.pursuing, err)
	}
}
