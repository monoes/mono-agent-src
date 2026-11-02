package coderhost

import (
	"errors"
	"testing"
	"time"
)

// --- GROUP 2 ---------------------------------------------------------------------------------------------------

func TestTimestampSiteRecordOwnHourlyRate(t *testing.T) {
	runTS(t, siteRecordRate)
}

func siteRecordRate(t *testing.T, cut time.Time, stamp any) bool {
	t.Helper()
	st, clk := soloStore(t)
	clk.t = cut.Add(recordWindow)
	fill := cut.Add(recordWindow - time.Minute).Format(tsFixedLayout)
	ins := func(at any) {
		if _, err := st.db.Exec(`INSERT INTO coder_session_decisions (profile_id, conversation_id, class, made_by, created_at) VALUES ('p', 'solo', 'ambiguity', 'model', ?)`, at); err != nil {
			t.Fatal(err)
		}
	}
	for i := 0; i < MaxRecordsPerHour-1; i++ {
		ins(fill)
	}
	ins(stamp)
	_, err := st.RecordOwn(caller("solo"), RecordRequest{Class: "ambiguity", Question: "q", Choice: "a", Reversible: true})
	if err != nil && !errors.Is(err, ErrRecordRate) {
		t.Fatal(err)
	}
	return errors.Is(err, ErrRecordRate)
}

func TestTimestampSitePersonItemHourlyRate(t *testing.T) {
	runTS(t, sitePersonRate)
}

func sitePersonRate(t *testing.T, cut time.Time, stamp any) bool {
	t.Helper()
	st, clk := soloStore(t)
	clk.t = cut.Add(recordWindow)
	fill := cut.Add(recordWindow - time.Minute).Format(tsFixedLayout)
	ins := func(ref int, at any) {
		if _, err := st.db.Exec(`INSERT INTO coder_session_person_items (profile_id, conversation_id, kind, ref_id, created_at) VALUES ('p', 'solo', ?, ?, ?)`, ItemParkedQuestion, ref, at); err != nil {
			t.Fatal(err)
		}
	}
	for i := 0; i < MaxPersonItemsPerHour-1; i++ {
		ins(1000+i, fill)
	}
	ins(2000, stamp)
	_, err := st.ParkOwn(caller("solo"), ParkRequest{Class: "risky_action", Question: "q"})
	if err != nil && !errors.Is(err, ErrPersonItemRate) {
		t.Fatal(err)
	}
	return errors.Is(err, ErrPersonItemRate)
}

func TestTimestampSitePruneClosedQuestions(t *testing.T) {
	runTS(t, sitePrune)
}

func sitePrune(t *testing.T, cut time.Time, stamp any) bool {
	t.Helper()
	st, clk := soloStore(t)
	clk.t = cut.Add(MaxSendAge)
	if _, err := st.db.Exec(`INSERT INTO coder_session_questions (profile_id, conversation_id, class, status, command_id, created_at, answered_at) VALUES ('p', 'solo', 'ambiguity', 'answered', 0, ?, ?)`, stamp, stamp); err != nil {
		t.Fatal(err)
	}
	// MaxClosedQuestions newer closed rows that are already queued: the planted row is the one beyond the keep limit.
	if _, err := st.db.Exec(`WITH RECURSIVE n(i) AS (SELECT 1 UNION ALL SELECT i + 1 FROM n WHERE i < ?)
		INSERT INTO coder_session_questions (profile_id, conversation_id, class, status, command_id, created_at, answered_at) SELECT 'p', 'solo', 'ambiguity', 'answered', 1, ?, ? FROM n`,
		MaxClosedQuestions, stamp, stamp); err != nil {
		t.Fatal(err)
	}
	st.pruneClosedQuestions("p", "solo")
	var n int
	_ = st.db.QueryRow(`SELECT COUNT(*) FROM coder_session_questions WHERE command_id = 0`).Scan(&n)
	return n == 1
}

func TestTimestampSiteRedeliverLostNotes(t *testing.T) {
	runTS(t, siteRedeliver)
}

func siteRedeliver(t *testing.T, cut time.Time, stamp any) bool {
	t.Helper()
	st, clk := soloStore(t)
	clk.t = cut.Add(redeliverAfter)
	if _, err := st.db.Exec(`INSERT INTO coder_session_questions (profile_id, conversation_id, class, status, answer, command_id, created_at, answered_at) VALUES ('p', 'solo', 'ambiguity', 'answered', 'a', 0, ?, ?)`, stamp, stamp); err != nil {
		t.Fatal(err)
	}
	_ = st.redeliver()
	var at string
	var cmd int
	_ = st.db.QueryRow(`SELECT answered_at, command_id FROM coder_session_questions`).Scan(&at, &cmd)
	return at == stamp && cmd == 0 // untouched: not yet due
}

func TestTimestampSiteApplyDefaultsParkTimeout(t *testing.T) {
	runTS(t, siteDefaults)
}

func siteDefaults(t *testing.T, cut time.Time, stamp any) bool {
	t.Helper()
	st, clk := parkStore(t)
	childOf(t, st)
	setSettings(t, st, "k1", `{"autonomy_classes": {"ambiguity": {"who": "master", "fallback": "take_default"}}}`)
	clk.t = cut.Add(ParkTimeout)
	if _, err := st.db.Exec(`INSERT INTO coder_session_questions (profile_id, conversation_id, class, default_answer, status, created_at) VALUES ('p', 'k1', 'ambiguity', 'a', 'open', ?)`, stamp); err != nil {
		t.Fatal(err)
	}
	n, _ := st.ApplyDefaults()
	return n == 0 // not yet timed out
}

func expireSendsCase(t *testing.T, now time.Time, turn, issuer, stamp any) bool {
	t.Helper()
	st := newStoreT(t)
	pinClock(st, now)
	if _, err := st.db.Exec(`INSERT INTO coder_session_commands (profile_id, conversation_id, kind, turn_id, status, issuer, created_at, updated_at) VALUES ('p', 'c', 'send', ?, 'pending', ?, ?, ?)`, turn, issuer, stamp, stamp); err != nil {
		t.Fatal(err)
	}
	if err := st.ExpireSends(); err != nil {
		t.Fatal(err)
	}
	var status string
	_ = st.db.QueryRow(`SELECT status FROM coder_session_commands`).Scan(&status)
	return status == "pending" // still waiting
}

func TestTimestampSiteExpireSendsMaxSendAge(t *testing.T) {
	runTS(t, func(t *testing.T, cut time.Time, stamp any) bool {
		return expireSendsCase(t, cut.Add(MaxSendAge), "person-1", "", stamp)
	})
}

func TestTimestampSiteExpireSendsCheckinAge(t *testing.T) {
	runTS(t, func(t *testing.T, cut time.Time, stamp any) bool {
		return expireSendsCase(t, cut.Add(MaxCheckinAge), "checkin-1", "", stamp)
	})
}

func TestTimestampSiteExpireSendsIssuedAge(t *testing.T) {
	runTS(t, func(t *testing.T, cut time.Time, stamp any) bool {
		return expireSendsCase(t, cut.Add(MaxCheckinAge), "person-1", "master", stamp)
	})
}

func plantSpawning(t *testing.T, st *Store, pending string, stamp any) {
	t.Helper()
	if _, err := st.db.Exec(`INSERT INTO coder_mission_children (profile_id, session_id, task_id, lead_conversation_id, pending_conversation_id, origin, state, created_at, updated_at) VALUES ('p', 1, 1, 'lead', ?, 'lead', 'spawning', ?, ?)`, pending, stamp, stamp); err != nil {
		t.Fatal(err)
	}
}

func TestTimestampSiteResetStaleSpawningNeverLinked(t *testing.T) {
	runTS(t, siteResetNever)
}

func siteResetNever(t *testing.T, cut time.Time, stamp any) bool {
	t.Helper()
	st := newStoreT(t)
	pinClock(st, cut.Add(time.Hour))
	plantSpawning(t, st, "", stamp)
	reset, _, err := st.ResetStaleSpawning(time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	return reset == 0
}

func TestTimestampSiteResetStaleSpawningCreatedNotLinked(t *testing.T) {
	runTS(t, siteResetOrphan)
}

func siteResetOrphan(t *testing.T, cut time.Time, stamp any) bool {
	t.Helper()
	st := newStoreT(t)
	pinClock(st, cut.Add(time.Hour))
	plantSpawning(t, st, "pc", stamp)
	_, ended, err := st.ResetStaleSpawning(time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	return len(ended) == 0
}

func TestTimestampSiteFindOrphanCarves(t *testing.T) {
	runTS(t, siteOrphanCarve)
}

func siteOrphanCarve(t *testing.T, cut time.Time, stamp any) bool {
	t.Helper()
	st := newStoreT(t)
	pinClock(st, cut.Add(time.Hour))
	if _, err := st.db.Exec(`INSERT INTO coder_sessions (profile_id, conversation_id, role, parent_conversation_id, budget_usd, created_at, updated_at) VALUES ('p', 'kid', 'child', 'lead', 1, ?, ?)`, stamp, stamp); err != nil {
		t.Fatal(err)
	}
	if _, err := st.db.Exec(`INSERT INTO coder_mission_policy (profile_id, session_id, lead_conversation_id, updated_at) VALUES ('p', 1, 'lead', ?)`, stamp); err != nil {
		t.Fatal(err)
	}
	got, err := st.FindOrphanCarves(time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	return len(got) == 0 // too young to be an orphan
}
