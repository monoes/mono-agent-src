package storage

import (
	"path/filepath"
	"testing"
)

// ApplyMigrations must not leave the pool with a second connection: every new
// connection re-parses the whole schema (the CLI start cost, several times
// more under -race), and the steps after the schema migrations can reuse the
// migration connection once it is back in the pool. The returned connection
// must also have foreign keys enforced again.
func TestApplyMigrations_UsesOneConnectionAndRestoresForeignKeys(t *testing.T) {
	db, err := NewDatabase(filepath.Join(t.TempDir(), "monoagent.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })

	for _, round := range []string{"fresh database", "already migrated database"} {
		if err := db.ApplyMigrations(); err != nil {
			t.Fatalf("%s: %v", round, err)
		}
		if got := db.DB.Stats().OpenConnections; got != 1 {
			t.Fatalf("%s: %d pool connections after ApplyMigrations, want 1", round, got)
		}
		var fk int
		if err := db.DB.QueryRow("PRAGMA foreign_keys").Scan(&fk); err != nil || fk != 1 {
			t.Fatalf("%s: foreign_keys = %d (err %v) on the pooled connection, want 1", round, fk, err)
		}
	}
}
