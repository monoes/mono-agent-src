package coderhost

import (
	"fmt"
	"strings"
	"testing"
	"time"
)

// spellings: the same instant as the released writer (RFC3339Nano), a fixed-width writer, and SQLite's strftime('%f').
func spellings(at time.Time) []string {
	at = at.UTC()
	return []string{at.Format(time.RFC3339Nano), at.Format(tsFixedLayout), at.Format("2006-01-02T15:04:05.000Z")}
}

func TestTsBeforeAndNotBeforeAgreeWithTimeForEverySpelling(t *testing.T) {
	st := newStoreT(t)
	w := time.Date(2026, 10, 31, 12, 0, 0, 0, time.UTC)
	offsets := []time.Duration{-2 * time.Second, -time.Second, -time.Millisecond, -time.Nanosecond, 0, time.Nanosecond, 100 * time.Millisecond,
		500 * time.Millisecond, 510 * time.Millisecond, 999999999, time.Second, 2 * time.Second}
	var cuts []time.Time
	for _, o := range offsets {
		cuts = append(cuts, w.Add(o))
	}
	for _, cut := range cuts {
		for _, o := range offsets {
			at := w.Add(o)
			for _, stored := range spellings(at) {
				if strings.HasSuffix(stored, ".000Z") && at.Nanosecond()%1_000_000 != 0 {
					continue // the millisecond spelling cannot hold this instant
				}
				name := fmt.Sprintf("%s vs cut %s", stored, cut.Format(tsFixedLayout))
				parsed, err := time.Parse(time.RFC3339Nano, stored)
				if err != nil {
					t.Fatal(err)
				}
				for cond, want := range map[string]bool{tsRawBefore("c", cut): parsed.Before(cut), tsRawNotBefore("c", cut): !parsed.Before(cut)} {
					var n int
					if err := st.db.QueryRow(`SELECT COUNT(*) FROM (SELECT ? AS c) WHERE `+cond, stored).Scan(&n); err != nil {
						t.Fatalf("%s: %v", name, err)
					}
					if (n == 1) != want {
						t.Errorf("%s: %s = %v, want %v", name, cond[:12], n == 1, want)
					}
				}
			}
		}
	}
}

func TestTsConditionsSelectTheSameRowsFromAMixedTable(t *testing.T) {
	st := newStoreT(t)
	w := time.Date(2026, 10, 31, 12, 0, 0, 0, time.UTC)
	if _, err := st.db.Exec(`CREATE TABLE mixed (id INTEGER PRIMARY KEY, at TEXT NOT NULL)`); err != nil {
		t.Fatal(err)
	}
	instants := map[int64]time.Time{}
	id := int64(0)
	for _, o := range []time.Duration{-time.Second, -time.Nanosecond, 0, time.Nanosecond, 400 * time.Millisecond, time.Second} {
		for i, s := range spellings(w.Add(o)) {
			if i == 2 && o%time.Millisecond != 0 {
				continue
			}
			id++
			instants[id] = w.Add(o)
			if _, err := st.db.Exec(`INSERT INTO mixed (id, at) VALUES (?, ?)`, id, s); err != nil {
				t.Fatal(err)
			}
		}
	}
	for _, cut := range []time.Time{w, w.Add(time.Nanosecond), w.Add(400 * time.Millisecond)} {
		for _, q := range []struct {
			cond string
			want func(time.Time) bool
		}{{tsRawBefore("at", cut), func(x time.Time) bool { return x.Before(cut) }}, {tsRawNotBefore("at", cut), func(x time.Time) bool { return !x.Before(cut) }}} {
			rows, err := st.db.Query(`SELECT id FROM mixed WHERE ` + q.cond)
			if err != nil {
				t.Fatal(err)
			}
			got := map[int64]bool{}
			for rows.Next() {
				var i int64
				_ = rows.Scan(&i)
				got[i] = true
			}
			rows.Close()
			for i, x := range instants {
				if got[i] != q.want(x) {
					t.Errorf("cut %s row %d (%s): selected = %v, want %v", cut.Format(tsFixedLayout), i, x.Format(tsFixedLayout), got[i], q.want(x))
				}
			}
		}
	}
}

func planOf(t *testing.T, st *Store, q string, args ...any) string {
	t.Helper()
	rows, err := st.db.Query("EXPLAIN QUERY PLAN "+q, args...)
	if err != nil {
		t.Fatalf("%v: %s", err, q)
	}
	defer rows.Close()
	var plan []string
	for rows.Next() {
		var a, b, c int
		var detail string
		_ = rows.Scan(&a, &b, &c, &detail)
		plan = append(plan, detail)
	}
	return strings.Join(plan, " | ")
}

// The conditions add residual filters only: for every table a site reads, the plan of the query with the new condition
// equals the plan of the same query with the lexical comparison it replaced (the extra "unparsable" terms must not push a
// large table from its index to a scan).
func TestTsConditionsKeepTheAccessPathOfTheLexicalQueries(t *testing.T) {
	st := newStoreT(t)
	now := time.Now()
	const lex = "'2026-10-31T12:00:00Z'"
	cases := []struct{ name, prefix, col, neu, old string }{
		{"decisions count", `SELECT COUNT(*) FROM coder_session_decisions WHERE profile_id = 'p' AND conversation_id = 'c' AND made_by = 'model' AND `, "created_at", tsInWindow("created_at", now), "created_at >= " + lex},
		{"person_items count", `SELECT COUNT(*) FROM coder_session_person_items WHERE profile_id = 'p' AND conversation_id = 'c' AND `, "created_at", tsInWindow("created_at", now), "created_at >= " + lex},
		{"spawns count", `SELECT COUNT(*) FROM coder_session_spawns WHERE profile_id = 'p' AND parent = 'm' AND `, "created_at", tsInWindow("created_at", now), "created_at >= " + lex},
		{"spawns cleanup", `SELECT 1 FROM coder_session_spawns WHERE profile_id = 'p' AND parent = 'm' AND `, "created_at", tsOlderThan("created_at", now), "created_at < " + lex},
		{"budget_requests count", `SELECT COUNT(*) FROM coder_budget_requests WHERE profile_id = 'p' AND lead_conversation_id = 'l' AND `, "created_at", tsInWindow("created_at", now), "created_at >= " + lex},
		{"session_tokens live", `SELECT pid, stamp FROM coder_session_tokens WHERE pid > 1 AND `, "expires_at", tsLive("expires_at", now), "expires_at >= " + lex},
		{"session_tokens expired", `SELECT 1 FROM coder_session_tokens WHERE (profile_id = 'p' AND conversation_id = 'c') OR `, "expires_at", tsExpired("expires_at", now), "expires_at < " + lex},
		{"turn_caps drop", `SELECT 1 FROM coder_session_turn_caps WHERE started = 0 AND `, "created_at", tsOlderThan("created_at", now), "created_at < " + lex},
		{"spawn_intents sweep", `SELECT 1 FROM coder_spawn_intents WHERE state = 'pending' AND `, "created_at", tsLapsed("created_at", now), "created_at < " + lex},
		{"questions defaults", `SELECT 1 FROM coder_session_questions WHERE status = 'open' AND default_answer != '' AND `, "created_at", tsOlderThan("created_at", now), "created_at < " + lex},
		{"questions redeliver", `SELECT 1 FROM coder_session_questions WHERE status != 'open' AND command_id = 0 AND answered_at != '' AND `, "answered_at", tsOlderThan("answered_at", now), "answered_at < " + lex},
		{"commands expire", `SELECT 1 FROM coder_session_commands WHERE kind = 'send' AND status = 'pending' AND `, "created_at", tsLapsed("created_at", now), "created_at < " + lex},
		{"mission_children stale", `SELECT 1 FROM coder_mission_children WHERE state = 'spawning' AND child_conversation_id = '' AND `, "updated_at", tsOlderThan("updated_at", now), "updated_at < " + lex},
		{"orphan carves", `SELECT 1 FROM coder_sessions c WHERE c.role = 'child' AND c.budget_usd > 0 AND `, "c.created_at", tsOlderThan("c.created_at", now), "c.created_at < " + lex},
	}
	for _, c := range cases {
		neu, old := planOf(t, st, c.prefix+c.neu), planOf(t, st, c.prefix+c.old)
		t.Logf("%s\n   old: %s\n   new: %s", c.name, old, neu)
		if neu != old {
			t.Errorf("%s: plan changed\n   old: %s\n   new: %s", c.name, old, neu)
		}
	}
}
