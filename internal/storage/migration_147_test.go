package storage

import (
	"path/filepath"
	"testing"
)

// Migration 147 links a stored dynamic-org worker to the mission task it worked
// (ai_chat_org_workers.mission_session / mission_task), so an operator can retire it. It is additive: a database at
// the previous head keeps its rows, with the link empty (0), a fresh one has the columns, and a second run changes nothing.
func TestMigration147LinksStoredWorkersToTheirMissionTask(t *testing.T) {
	check := func(t *testing.T, db *Database, wantOld bool) {
		t.Helper()
		for _, col := range []string{"mission_session", "mission_task"} {
			var typ, dflt string
			var notnull int
			err := db.DB.QueryRow(`SELECT type, "notnull", COALESCE(dflt_value, '') FROM pragma_table_info('ai_chat_org_workers') WHERE name = ?`, col).Scan(&typ, &notnull, &dflt)
			if err != nil || typ != "INTEGER" || notnull != 1 || dflt != "0" {
				t.Fatalf("%s: type %q notnull %d default %q err %v", col, typ, notnull, dflt, err)
			}
		}
		if wantOld {
			var s, k int64
			var report string
			if err := db.DB.QueryRow(`SELECT mission_session, mission_task, report FROM ai_chat_org_workers WHERE agent_id = 'old'`).Scan(&s, &k, &report); err != nil || s != 0 || k != 0 || report != "kept" {
				t.Fatalf("a worker stored before the migration: %d %d %q %v", s, k, report, err)
			}
		}
	}
	t.Run("from the previous head, twice", func(t *testing.T) {
		db, err := NewDatabase(filepath.Join(t.TempDir(), "monoagent.db"))
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { db.Close() })
		applyMigrationsBelow(t, db, 147)
		if _, err := db.DB.Exec(`INSERT INTO ai_chat_conversations (id, profile_id, backend, created_at, updated_at) VALUES ('c', 'default', 'monomind', 'n', 'n')`); err != nil {
			t.Fatal(err)
		}
		if _, err := db.DB.Exec(`INSERT INTO ai_chat_org_workers (profile_id, conversation_id, agent_id, turn_id, runtime, report, updated_at) VALUES ('default', 'c', 'old', 't', 'claude', 'kept', 'n')`); err != nil {
			t.Fatal(err)
		}
		for run := 1; run <= 2; run++ {
			if err := db.ApplyMigrations(); err != nil {
				t.Fatalf("apply migrations, run %d: %v", run, err)
			}
			check(t, db, true)
		}
	})
	t.Run("fresh, twice", func(t *testing.T) {
		db, err := NewDatabase(filepath.Join(t.TempDir(), "monoagent.db"))
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { db.Close() })
		for run := 1; run <= 2; run++ {
			if err := db.ApplyMigrations(); err != nil {
				t.Fatalf("apply migrations, run %d: %v", run, err)
			}
			check(t, db, false)
		}
	})
}
