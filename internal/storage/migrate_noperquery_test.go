package storage

import (
	"path/filepath"
	"strings"
	"testing"

	"github.com/monoes/mono-agent/internal/dbconn/dbconntest"
)

// A fully migrated database is opened on every CLI call: the apply loop must use the applied set it already read, not
// ask schema_migrations about each of the ~170 migrations again (R12-07).
func TestApplyMigrationsDoesNotQueryPerMigrationWhenUpToDate(t *testing.T) {
	path := filepath.Join(t.TempDir(), "m.db")
	first, err := NewDatabase(path)
	if err != nil {
		t.Fatal(err)
	}
	if err := first.ApplyMigrations(); err != nil {
		t.Fatal(err)
	}
	first.Close()

	db, trip := dbconntest.OpenPath(t, path)
	perVersion := 0
	trip.BeforePrepare = func(q string) {
		if strings.Contains(q, "FROM schema_migrations WHERE version") {
			perVersion++
		}
	}
	if err := (&Database{DB: db, dbPath: path}).ApplyMigrations(); err != nil {
		t.Fatal(err)
	}
	if perVersion != 0 {
		t.Fatalf("%d per-migration queries on an up-to-date database", perVersion)
	}
}
