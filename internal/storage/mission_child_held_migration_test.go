package storage

import (
	"path/filepath"
	"testing"

	"github.com/monoes/mono-agent/data"
)

// Migration 150 only adds the view v_mission_child_held: on a database that is at the version before it, on a
// fresh one, and when run twice; rows already there are untouched and the view reads them at once.
func TestMissionChildHeldViewMigrationAppliesOnHeadAndFreshDatabases(t *testing.T) {
	head, err := NewDatabase(filepath.Join(t.TempDir(), "head.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer head.Close()
	applyMigrationsBelow(t, head, 150)
	seedResSession(t, head.DB)
	if _, err := head.DB.Exec(`UPDATE task_sessions SET conversation_id = 'lead-1', budget_usd = 5 WHERE id = 1`); err != nil {
		t.Fatal(err)
	}
	if _, err := head.DB.Exec(`INSERT INTO coder_sessions (profile_id, conversation_id, parent_conversation_id, budget_usd, created_at, updated_at) VALUES ('p', 'kid', 'lead-1', 2, 'x', 'x')`); err != nil {
		t.Fatal(err)
	}
	if err := head.ApplyMigrations(); err != nil {
		t.Fatal(err)
	}
	var held float64
	var lost int
	if err := head.DB.QueryRow(`SELECT held_usd, lost FROM v_mission_child_held WHERE profile_id = 'p' AND session_id = 1`).Scan(&held, &lost); err != nil || held != 2 || lost != 0 {
		t.Fatalf("view on the head database = %v lost %d, %v: want 2, 0", held, lost, err)
	}
	if err := head.ApplyMigrations(); err != nil {
		t.Fatal(err)
	}
	raw, err := data.MigrationsFS.ReadFile("migrations/150_mission_child_held.sql")
	if err != nil {
		t.Fatal(err)
	}
	for _, s := range splitStatements(string(raw)) {
		if _, err := head.DB.Exec(s); err != nil {
			t.Fatalf("re-running the migration over the live view: %v", err)
		}
	}
	fresh := resDB(t)
	var n int
	if err := fresh.DB.QueryRow(`SELECT COUNT(*) FROM v_mission_child_held`).Scan(&n); err != nil || n != 0 {
		t.Fatalf("view on a fresh database: %d rows, %v", n, err)
	}
	var typ string
	if err := fresh.DB.QueryRow(`SELECT type FROM sqlite_master WHERE name = 'v_mission_child_held'`).Scan(&typ); err != nil || typ != "view" {
		t.Fatalf("v_mission_child_held is a %q, %v", typ, err)
	}
}
