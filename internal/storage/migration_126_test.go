package storage

import (
	"path/filepath"
	"testing"
)

// Migration 126 (task sessions) on a database that has a personal board: it adds
// columns and tables, changes no row, and running the migrations again is a no-op.
// The database is built here from the migrations before 126 with synthetic rows.
func TestMigration126TaskSessionsIsAdditiveAndIdempotent(t *testing.T) {
	db, err := NewDatabase(filepath.Join(t.TempDir(), "monoagent.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	applyMigrationsBelow(t, db, 126)
	mustExec := func(q string, args ...any) {
		t.Helper()
		if _, err := db.DB.Exec(q, args...); err != nil {
			t.Fatalf("%s: %v", q, err)
		}
	}
	mustExec(`INSERT INTO profiles (id, name) VALUES ('p1', 'P1')`)
	mustExec(`INSERT INTO tasks (profile_id, title, status, position, created_at, updated_at) VALUES ('p1', 'old card', 'ready', 1024, '2026-10-01T00:00:00Z', '2026-10-01T00:00:00Z')`)

	for run := 1; run <= 2; run++ {
		if err := db.ApplyMigrations(); err != nil {
			t.Fatalf("apply migrations, run %d: %v", run, err)
		}
		var n int
		if err := db.DB.QueryRow(`SELECT COUNT(*) FROM schema_migrations WHERE version = 126`).Scan(&n); err != nil || n != 1 {
			t.Fatalf("run %d: migration 126 recorded %d times (%v)", run, n, err)
		}
	}
	var session, parent any
	var kind, acceptance, title string
	var blocked, verified, retries int
	err = db.DB.QueryRow(`SELECT title, session_id, parent_id, kind, acceptance, blocked, verified, retries FROM tasks WHERE profile_id = 'p1'`).
		Scan(&title, &session, &parent, &kind, &acceptance, &blocked, &verified, &retries)
	if err != nil {
		t.Fatal(err)
	}
	if title != "old card" || session != nil || parent != nil || kind != "task" || acceptance != "" || blocked+verified+retries != 0 {
		t.Fatalf("an old card changed: %q %v %v %q %q %d %d %d", title, session, parent, kind, acceptance, blocked, verified, retries)
	}
	for _, table := range []string{"task_sessions", "task_deps", "task_artifacts", "task_session_events", "task_session_rev"} {
		if _, err := db.DB.Exec(`SELECT COUNT(*) FROM ` + table); err != nil {
			t.Errorf("%s: %v", table, err)
		}
	}
	// tasks.status keeps its CHECK: blocked and verified are flags, not statuses
	if _, err := db.DB.Exec(`UPDATE tasks SET status = 'blocked'`); err == nil {
		t.Error("tasks.status accepted blocked")
	}
	if _, err := db.DB.Exec(`UPDATE tasks SET blocked = 2`); err == nil {
		t.Error("tasks.blocked accepted 2")
	}
	if _, err := db.DB.Exec(`UPDATE tasks SET kind = 'story'`); err == nil {
		t.Error("tasks.kind accepted story")
	}
	// the session idempotency key: old sessions are unkeyed, a repeat in one profile is
	// refused by the index, the same key in another profile is not
	mustExec(`INSERT INTO profiles (id, name) VALUES ('p2', 'P2')`)
	keyed := func(profile, key string) error {
		_, err := db.DB.Exec(`INSERT INTO task_sessions (profile_id, title, created_by, created_at, updated_at, client_id) VALUES (?, 'x', 'you', 't', 't', ?)`, profile, key)
		return err
	}
	if err := keyed("p1", "k"); err != nil {
		t.Fatal(err)
	}
	if err := keyed("p1", "k"); err == nil {
		t.Error("a repeated client id in one profile was accepted")
	}
	if err := keyed("p2", "k"); err != nil {
		t.Errorf("the same client id in another profile: %v", err)
	}
	mustExec(`INSERT INTO task_sessions (profile_id, title, created_by, created_at, updated_at) VALUES ('p1', 'a', 'you', 't', 't')`)
	mustExec(`INSERT INTO task_sessions (profile_id, title, created_by, created_at, updated_at) VALUES ('p1', 'b', 'you', 't', 't')`)
}
