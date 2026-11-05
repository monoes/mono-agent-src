package store

import (
	"testing"

	"github.com/monoes/mono-agent/internal/storage"
	"github.com/zalando/go-keyring"
)

// An install that opened a run before the record existed is filled from its runs (which are never deleted) when the migration
// is applied by the real migration runner, and applying it again changes nothing.
func TestRunSeenMigrationBackfillsFromExistingRuns(t *testing.T) {
	keyring.MockInit()
	d, err := storage.NewDatabase(t.TempDir() + "/dd.db")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { d.DB.Close() })
	if err := d.ApplyMigrations(); err != nil {
		t.Fatal(err)
	}
	enter(t, New(d.DB, testKeys()), "p")
	for i := 0; i < 2; i++ {
		if i == 0 { // the first pass starts from an install that had runs and no record
			if _, err := d.DB.Exec("DROP TABLE doomsday_run_seen"); err != nil {
				t.Fatal(err)
			}
		}
		if _, err := d.DB.Exec("DELETE FROM schema_migrations WHERE version = 158"); err != nil {
			t.Fatal(err)
		}
		if err := d.ApplyMigrations(); err != nil {
			t.Fatalf("pass %d: %v", i, err)
		}
		var n int
		var gen int64
		if err := d.DB.QueryRow(`SELECT COUNT(*), MIN(first_generation) FROM doomsday_run_seen WHERE profile = 'p'`).Scan(&n, &gen); err != nil || n != 1 || gen != 1 {
			t.Fatalf("pass %d: backfill: %d %d %v", i, n, gen, err)
		}
	}
}
