package storage

import (
	"context"
	"path/filepath"
	"testing"
)

// openFresh opens a brand-new database in t.TempDir() and runs every migration.
func openFresh(t *testing.T) *Database {
	t.Helper()
	db, err := NewDatabase(filepath.Join(t.TempDir(), "fresh.db"))
	if err != nil {
		t.Fatalf("NewDatabase: %v", err)
	}
	t.Cleanup(func() { db.Close() })
	if err := db.ApplyMigrations(); err != nil {
		t.Fatalf("ApplyMigrations on a new database: %v", err)
	}
	return db
}

func countProfiles(t *testing.T, db *Database) int {
	t.Helper()
	var n int
	if err := db.DB.QueryRow(`SELECT COUNT(*) FROM profiles`).Scan(&n); err != nil {
		t.Fatalf("counting profiles: %v", err)
	}
	return n
}

// A new database starts with no profile: the user creates and names the first.
func TestFreshDatabaseHasNoProfiles(t *testing.T) {
	db := openFresh(t)
	if n := countProfiles(t, db); n != 0 {
		t.Errorf("profiles on a new database = %d, want 0", n)
	}
	var n int
	if err := db.DB.QueryRow(`SELECT COUNT(*) FROM settings WHERE key = 'active_profile_id'`).Scan(&n); err != nil {
		t.Fatalf("reading settings: %v", err)
	}
	if n != 0 {
		t.Errorf("settings has %d active_profile_id row(s), want none", n)
	}
}

// Every step that runs after the schema migrations at startup copes with an
// empty profiles table and does not create a profile of its own.
func TestReconcileRunsOnEmptyProfiles(t *testing.T) {
	db := openFresh(t)
	before := countProfiles(t, db)
	ctx := context.Background()
	if err := db.ReconcileSchema(ctx); err != nil {
		t.Fatalf("ReconcileSchema: %v", err)
	}
	if err := MigrateActionsToWorkflows(ctx, db.DB); err != nil {
		t.Fatalf("MigrateActionsToWorkflows: %v", err)
	}
	if err := db.ApplyMigrations(); err != nil {
		t.Fatalf("second ApplyMigrations: %v", err)
	}
	if after := countProfiles(t, db); after != before || after != 0 {
		t.Errorf("profiles before startup steps = %d, after = %d, want 0 both times", before, after)
	}
}

// A database created before the seed was removed keeps its 'default'
// profile and the rows that belong to it.
func TestExistingDefaultProfileSurvives(t *testing.T) {
	db, err := NewDatabase(filepath.Join(t.TempDir(), "old.db"))
	if err != nil {
		t.Fatalf("NewDatabase: %v", err)
	}
	t.Cleanup(func() { db.Close() })
	// An old database: migrations up to 011 applied, with the row 011 used to seed.
	applyMigrationsBelow(t, db, 12)
	for _, stmt := range []string{
		`INSERT OR IGNORE INTO profiles (id, name) VALUES ('default', 'Default')`,
		`INSERT INTO workflows (id, name, profile_id) VALUES ('wf-1', 'Old workflow', 'default')`,
	} {
		if _, err := db.DB.Exec(stmt); err != nil {
			t.Fatalf("seeding the old database: %v", err)
		}
	}
	if err := db.ApplyMigrations(); err != nil {
		t.Fatalf("ApplyMigrations on the old database: %v", err)
	}
	if err := db.ReconcileSchema(context.Background()); err != nil {
		t.Fatalf("ReconcileSchema: %v", err)
	}
	if err := db.ApplyMigrations(); err != nil {
		t.Fatalf("second ApplyMigrations: %v", err)
	}

	var id, name string
	if err := db.DB.QueryRow(`SELECT id, name FROM profiles`).Scan(&id, &name); err != nil {
		t.Fatalf("reading the profile: %v", err)
	}
	if id != "default" || name != "Default" {
		t.Errorf("profile = (%q, %q), want (default, Default)", id, name)
	}
	if n := countProfiles(t, db); n != 1 {
		t.Errorf("profiles = %d, want 1", n)
	}
	var owner, wfName string
	if err := db.DB.QueryRow(`SELECT profile_id, name FROM workflows WHERE id = 'wf-1'`).Scan(&owner, &wfName); err != nil {
		t.Fatalf("reading the workflow: %v", err)
	}
	if owner != "default" || wfName != "Old workflow" {
		t.Errorf("workflow = (%q, %q), want (default, Old workflow)", owner, wfName)
	}
}
