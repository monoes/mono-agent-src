package storage

import (
	"path/filepath"
	"testing"
)

// MigrationsApplied counts what this run applied: all of them on a fresh file, none on the next call.
func TestMigrationsAppliedCountsOnlyTheLastRun(t *testing.T) {
	db, err := NewDatabase(filepath.Join(t.TempDir(), "n.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	if err := db.ApplyMigrations(); err != nil {
		t.Fatal(err)
	}
	first := db.MigrationsApplied()
	var rows int
	if err := db.DB.QueryRow(`SELECT COUNT(*) FROM schema_migrations`).Scan(&rows); err != nil {
		t.Fatal(err)
	}
	if first == 0 || first != rows {
		t.Fatalf("first run applied %d, schema_migrations has %d", first, rows)
	}
	if err := db.ApplyMigrations(); err != nil {
		t.Fatal(err)
	}
	if n := db.MigrationsApplied(); n != 0 {
		t.Errorf("second run applied %d, want 0", n)
	}
}
