package storage

import (
	"path/filepath"
	"testing"
)

// Migration 155 adds the governor's slot, queue and state tables (empty) and their guard triggers. It is additive: a
// database at 154 keeps its doomsday rows, a fresh one has the tables, and a second run changes nothing.
func TestMigration155AddsTheGovernorTablesAndLeavesTheDoomsdayRowsAlone(t *testing.T) {
	check := func(t *testing.T, db *Database, wantOld bool) {
		t.Helper()
		for _, tbl := range []string{"doomsday_slots", "doomsday_queue", "doomsday_governor_state"} {
			var n int
			if err := db.DB.QueryRow(`SELECT COUNT(*) FROM ` + tbl).Scan(&n); err != nil || n != 0 {
				t.Fatalf("%s: %d rows, %v (the migration inserts none)", tbl, n, err)
			}
		}
		var triggers int
		if err := db.DB.QueryRow(`SELECT COUNT(*) FROM sqlite_master WHERE type = 'trigger' AND (name LIKE 'doomsday_slots_%' OR name LIKE 'doomsday_queue_%')`).Scan(&triggers); err != nil || triggers != 13 {
			t.Fatalf("%d governor triggers, %v, want 13", triggers, err)
		}
		if wantOld {
			var id string
			if err := db.DB.QueryRow(`SELECT charter_id FROM doomsday_charters WHERE profile = 'p'`).Scan(&id); err != nil || id != "dd-old" {
				t.Fatalf("a charter stored before the migration: %q %v", id, err)
			}
		}
	}
	t.Run("from the previous head, twice", func(t *testing.T) {
		db, err := NewDatabase(filepath.Join(t.TempDir(), "monoagent.db"))
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { db.Close() })
		applyMigrationsBelow(t, db, 155)
		if _, err := db.DB.Exec(`INSERT INTO doomsday_charters(profile,generation,charter_id,canon_version,charter_hash,body,mac,created_at) VALUES ('p',1,'dd-old',2,x'00','{}',x'00',1)`); err != nil {
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
