package storage

import (
	"database/sql"
	"strings"
	"testing"
)

// Migration 168 adds the judge spend ledger, new columns on the pursuit state, and the rule that a pursuit starts only for a
// goal with a rubric criterion. It is additive: a database at the 167 head keeps its rows and a second run changes nothing.
func TestMigration168IsAdditiveFromTheHeadBelow(t *testing.T) {
	db := open156(t)
	applyMigrationsBelow(t, db, 168)
	if _, err := db.DB.Exec(`INSERT INTO profiles (id, name) VALUES ('p', 'p')`); err != nil {
		t.Fatal(err)
	}
	if _, err := db.DB.Exec(`INSERT INTO task_sessions (profile_id, title, kind, created_by, created_at, updated_at) VALUES ('p', 't', 'mission', 'you', 'x', 'x')`); err != nil {
		t.Fatal(err)
	}
	if _, err := db.DB.Exec(`INSERT INTO mission_pursuit_state (session_id, updated_at) VALUES (1, 'x')`); err != nil {
		t.Fatal(err)
	}
	for run := 1; run <= 2; run++ {
		if err := db.ApplyMigrations(); err != nil {
			t.Fatalf("run %d: %v", run, err)
		}
		var n, bad int
		if err := db.DB.QueryRow(`SELECT COUNT(*) FROM mission_pursuit_spend`).Scan(&n); err != nil || n != 0 {
			t.Fatalf("the ledger: %d rows, %v", n, err)
		}
		if err := db.DB.QueryRow(`SELECT bad_rounds FROM mission_pursuit_state WHERE session_id = 1`).Scan(&bad); err != nil || bad != 0 {
			t.Fatalf("the existing state row after the upgrade: %d %v", bad, err)
		}
	}
	var v int
	if err := db.DB.QueryRow(`SELECT COUNT(*) FROM schema_migrations WHERE version = 168`).Scan(&v); err != nil || v != 1 {
		t.Fatalf("168 recorded %d times, %v", v, err)
	}
}

func exec168(t *testing.T, db *sql.DB, q string, args ...any) {
	t.Helper()
	if _, err := db.Exec(q, args...); err != nil {
		t.Fatalf("%s: %v", q, err)
	}
}

func refused168(t *testing.T, db *sql.DB, want, q string, args ...any) {
	t.Helper()
	_, err := db.Exec(q, args...)
	if err == nil {
		t.Fatalf("the database accepted: %s", q)
	}
	if !strings.Contains(err.Error(), want) {
		t.Fatalf("%s: refused for the wrong reason: %v", q, err)
	}
}

// A goal can only be confirmed with a rubric criterion (164). To prove that the PURSUIT refuses by itself, the test takes the
// goal rule away (a raw writer with the database file could) and shows the pursuit rule still holds.
func TestAPursuitCannotBeEnabledForAGoalWithoutARubricCriterion(t *testing.T) {
	db := openMigrated156(t).DB
	exec168(t, db, `INSERT INTO profiles (id, name) VALUES ('p', 'p')`)
	for i := 1; i <= 3; i++ {
		exec168(t, db, `INSERT INTO task_sessions (profile_id, title, kind, created_by, created_at, updated_at) VALUES ('p', 't', 'mission', 'you', 'x', 'x')`)
		exec168(t, db, `INSERT INTO mission_goals (session_id, state, criteria_hash, proposed_by, created_at, updated_at) VALUES (?, 'proposed', 'h', 'lead', 'x', 'x')`, i)
	}
	// 1: command criterion only; 2: command plus rubric; 3: command only, started through an UPDATE
	exec168(t, db, `INSERT INTO mission_goal_criteria (session_id, pos, kind, spec, hash) VALUES (1, 1, 'command', 'go test', 'a'), (2, 1, 'command', 'go test', 'a'), (2, 2, 'rubric', 'reads well', 'b'), (3, 1, 'command', 'go test', 'a')`)
	exec168(t, db, `DROP TRIGGER mission_goals_forward_only`) // the goal rule is out of the way: only the pursuit's own rule is tested
	exec168(t, db, `UPDATE mission_goals SET state = 'confirmed', confirmed_by = 'you', confirmed_at = 'x'`)

	const why = "rubric criterion"
	refused168(t, db, why, `INSERT INTO mission_pursuit (session_id, enabled, locked_by, created_at, updated_at) VALUES (1, 1, 'you', 'x', 'x')`)
	exec168(t, db, `INSERT INTO mission_pursuit (session_id, enabled, locked_by, created_at, updated_at) VALUES (2, 1, 'you', 'x', 'x')`)
	exec168(t, db, `INSERT INTO mission_pursuit (session_id, enabled, created_at, updated_at) VALUES (3, 0, 'x', 'x')`)
	refused168(t, db, why, `UPDATE mission_pursuit SET enabled = 1, locked_by = 'you' WHERE session_id = 3`)
	// a REPLACE or an upsert goes through the same insert and update rules
	refused168(t, db, why, `INSERT OR REPLACE INTO mission_pursuit (session_id, enabled, locked_by, created_at, updated_at) VALUES (1, 1, 'you', 'x', 'x')`)
	var n int
	if err := db.QueryRow(`SELECT COUNT(*) FROM mission_pursuit WHERE enabled = 1`).Scan(&n); err != nil || n != 1 {
		t.Fatalf("started pursuits: %d, %v (only the goal with a rubric criterion may be)", n, err)
	}
}

func TestTheJudgeLedgerIsSettledOnceAndNeverDeleted(t *testing.T) {
	db := openMigrated156(t).DB
	exec168(t, db, `INSERT INTO profiles (id, name) VALUES ('p', 'p')`)
	exec168(t, db, `INSERT INTO task_sessions (profile_id, title, kind, created_by, created_at, updated_at) VALUES ('p', 't', 'mission', 'you', 'x', 'x')`)
	exec168(t, db, `INSERT INTO mission_pursuit_spend (session_id, kind, at, micro) VALUES (1, 'judge', 'x', 500000)`)
	refused168(t, db, "never changes", `UPDATE mission_pursuit_spend SET at = 'y' WHERE id = 1`)
	refused168(t, db, "never changes", `UPDATE mission_pursuit_spend SET session_id = 1, kind = 'judge', settled = 0, micro = 0 WHERE id = 1`)
	exec168(t, db, `UPDATE mission_pursuit_spend SET micro = 1200, known = 1, settled = 1 WHERE id = 1`)
	refused168(t, db, "never changes", `UPDATE mission_pursuit_spend SET micro = 0 WHERE id = 1`)
	refused168(t, db, "never deleted", `DELETE FROM mission_pursuit_spend WHERE id = 1`)
	refused168(t, db, "", `INSERT INTO mission_pursuit_spend (session_id, kind, at, micro) VALUES (1, 'wake', 'x', 1)`)
	refused168(t, db, "", `INSERT INTO mission_pursuit_spend (session_id, kind, at, micro) VALUES (1, 'judge', 'x', -1)`)
	// the cascade is the one delete that goes through
	exec168(t, db, `DELETE FROM task_sessions WHERE id = 1`)
	var n int
	if err := db.QueryRow(`SELECT COUNT(*) FROM mission_pursuit_spend`).Scan(&n); err != nil || n != 0 {
		t.Fatalf("after the session went: %d rows, %v", n, err)
	}
}
