package storage

import (
	"path/filepath"
	"strings"
	"testing"

	"github.com/monoes/mono-agent/data"
)

// Migration 139 adds the mission child tables (internal/coderhost/missionchild_store.go). It is additive:
// a database at the previous head keeps its rows, a fresh one gets the tables, and a second run changes nothing.
func TestMigration139AddsTheMissionChildTables(t *testing.T) {
	check := func(t *testing.T, db *Database) {
		t.Helper()
		for _, table := range []string{"coder_mission_policy", "coder_mission_children", "coder_mission_events"} {
			var n int
			if err := db.DB.QueryRow(`SELECT COUNT(*) FROM sqlite_master WHERE type = 'table' AND name = ?`, table).Scan(&n); err != nil || n != 1 {
				t.Fatalf("%s: n=%d err=%v", table, n, err)
			}
		}
		ins := func(sql string, args ...any) error { _, err := db.DB.Exec(sql, args...); return err }
		const child = `INSERT INTO coder_mission_children (profile_id, session_id, task_id, lead_conversation_id, origin, size, state, end_reason, created_at, updated_at) VALUES ('p', 1, ?, 'c', ?, ?, ?, ?, 'n', 'n')`
		if err := ins(child, 1, "lead", "", "marked", ""); err != nil {
			t.Fatal(err)
		}
		for name, c := range map[string][]any{
			"a second row for the same task": {1, "lead", "", "marked", ""},
			"an unknown origin":              {2, "model", "", "marked", ""},
			"an unknown size":                {3, "lead", "huge", "marked", ""},
			"an unknown state":               {4, "lead", "", "running", ""},
			"an unknown end reason":          {5, "lead", "", "ended", "because"},
		} {
			if err := ins(child, c...); err == nil {
				t.Errorf("accepted %s", name)
			}
		}
		const pol = `INSERT INTO coder_mission_policy (profile_id, session_id, lead_conversation_id, mode, updated_at) VALUES ('p', ?, 'c', ?, 'n')`
		if err := ins(pol, 1, "all"); err != nil {
			t.Fatal(err)
		}
		if err := ins(pol, 2, "everything"); err == nil {
			t.Error("accepted an unknown mode")
		}
		if err := ins(pol, 1, "off"); err == nil {
			t.Error("accepted a second policy for one session")
		}
	}

	t.Run("fresh database", func(t *testing.T) {
		db, err := NewDatabase(filepath.Join(t.TempDir(), "monoagent.db"))
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { db.Close() })
		if err := db.ApplyMigrations(); err != nil {
			t.Fatal(err)
		}
		check(t, db)
	})

	t.Run("database at the previous head", func(t *testing.T) {
		db, err := NewDatabase(filepath.Join(t.TempDir(), "monoagent.db"))
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { db.Close() })
		applyMigrationsBelow(t, db, 139)
		var gone int
		if err := db.DB.QueryRow(`SELECT COUNT(*) FROM sqlite_master WHERE name LIKE 'coder_mission_%'`).Scan(&gone); err != nil || gone != 0 {
			t.Fatalf("the tables exist before the migration: %d %v", gone, err)
		}
		if _, err := db.DB.Exec(`INSERT INTO coder_sessions (profile_id, conversation_id, created_at, updated_at) VALUES ('p', 'keep', 'n', 'n')`); err != nil {
			t.Fatal(err)
		}
		for run := 1; run <= 2; run++ {
			if err := db.ApplyMigrations(); err != nil {
				t.Fatalf("apply migrations, run %d: %v", run, err)
			}
		}
		var conv string
		if err := db.DB.QueryRow(`SELECT conversation_id FROM coder_sessions`).Scan(&conv); err != nil || conv != "keep" {
			t.Fatalf("a session row did not survive: %q %v", conv, err)
		}
		check(t, db)
	})
}

// The migration adds tables only: it must not alter tasks or task_sessions.
func TestMigration139TouchesNoExistingTable(t *testing.T) {
	raw, err := data.MigrationsFS.ReadFile("migrations/139_coder_mission_children.sql")
	if err != nil {
		t.Fatal(err)
	}
	for _, line := range strings.Split(string(raw), "\n") {
		l := strings.ToUpper(strings.TrimSpace(line))
		if strings.HasPrefix(l, "--") || l == "" {
			continue
		}
		if strings.HasPrefix(l, "ALTER ") || strings.HasPrefix(l, "DELETE ") || strings.HasPrefix(l, "UPDATE ") || strings.HasPrefix(l, "INSERT ") {
			t.Errorf("migration 139 is additive tables only, found: %s", line)
		}
	}
}
