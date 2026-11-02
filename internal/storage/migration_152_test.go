package storage

import (
	"path/filepath"
	"testing"
)

// Migration 152 records whether a coder session's recorded turn process was a bound launch (coder_sessions.turn_bound). It is additive: a
// database at the previous head keeps its sessions, with the flag 0, a fresh one has the column, and a second run changes nothing.
func TestMigration152RecordsWhetherTheTurnProcessWasABoundLaunch(t *testing.T) {
	check := func(t *testing.T, db *Database, wantOld bool) {
		t.Helper()
		var typ, dflt string
		var notnull int
		err := db.DB.QueryRow(`SELECT type, "notnull", COALESCE(dflt_value, '') FROM pragma_table_info('coder_sessions') WHERE name = 'turn_bound'`).Scan(&typ, &notnull, &dflt)
		if err != nil || typ != "INTEGER" || notnull != 1 || dflt != "0" {
			t.Fatalf("turn_bound: type %q notnull %d default %q err %v", typ, notnull, dflt, err)
		}
		if wantOld {
			var bound int
			var turn string
			if err := db.DB.QueryRow(`SELECT turn_bound, turn_id FROM coder_sessions WHERE conversation_id = 'old'`).Scan(&bound, &turn); err != nil || bound != 0 || turn != "t1" {
				t.Fatalf("a session stored before the migration: %d %q %v", bound, turn, err)
			}
		}
	}
	t.Run("from the previous head, twice", func(t *testing.T) {
		db, err := NewDatabase(filepath.Join(t.TempDir(), "monoagent.db"))
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { db.Close() })
		applyMigrationsBelow(t, db, 152)
		if _, err := db.DB.Exec(`INSERT INTO coder_sessions (profile_id, conversation_id, state, role, turn_id, turn_pid, turn_stamp, created_at, updated_at) VALUES ('default', 'old', 'running', 'solo', 't1', 777, 'start:5', 'n', 'n')`); err != nil {
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

// A row written by the unreleased development head that kept the discriminator as a "bound:" prefix on the stamp is translated: the
// prefix is stripped and the flag set; plain stamps are left alone.
func TestMigration152TranslatesThePrefixedStampOfAnUnreleasedHead(t *testing.T) {
	db, err := NewDatabase(filepath.Join(t.TempDir(), "monoagent.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	applyMigrationsBelow(t, db, 152)
	for conv, stamp := range map[string]string{"prefixed": "bound:start:5@boot", "plain": "start:6"} {
		if _, err := db.DB.Exec(`INSERT INTO coder_sessions (profile_id, conversation_id, state, role, turn_id, turn_pid, turn_stamp, created_at, updated_at) VALUES ('default', ?, 'running', 'solo', 't1', 777, ?, 'n', 'n')`, conv, stamp); err != nil {
			t.Fatal(err)
		}
	}
	for run := 1; run <= 2; run++ { // twice: the translation is idempotent
		if err := db.ApplyMigrations(); err != nil {
			t.Fatalf("apply migrations, run %d: %v", run, err)
		}
		for conv, want := range map[string]struct {
			stamp string
			bound int
		}{"prefixed": {"start:5@boot", 1}, "plain": {"start:6", 0}} {
			var stamp string
			var bound int
			if err := db.DB.QueryRow(`SELECT turn_stamp, turn_bound FROM coder_sessions WHERE conversation_id = ?`, conv).Scan(&stamp, &bound); err != nil || stamp != want.stamp || bound != want.bound {
				t.Errorf("run %d, %s: stamp %q bound %d (%v), want %q and %d", run, conv, stamp, bound, err, want.stamp, want.bound)
			}
		}
	}
}
