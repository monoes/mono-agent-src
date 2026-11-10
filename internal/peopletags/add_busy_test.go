package peopletags

import (
	"context"
	"path/filepath"
	"testing"

	"github.com/monoes/mono-agent/internal/dbconn/dbconntest"
	"github.com/monoes/mono-agent/internal/storage"
)

// Add reads (person, tag, link count) and then writes. A writer that commits between the two must not make it fail
// with SQLITE_BUSY_SNAPSHOT (517, which busy_timeout does not wait for): the write lock is taken first (#1460).
func TestAddSurvivesAWriterCommittingBetweenItsReadAndItsWrite(t *testing.T) {
	path := filepath.Join(t.TempDir(), "t.db")
	d, err := storage.NewDatabase(path)
	if err != nil {
		t.Fatal(err)
	}
	if err := d.ApplyMigrations(); err != nil {
		t.Fatal(err)
	}
	if _, err := d.DB.Exec(`INSERT INTO people (id, profile_id, platform, platform_username) VALUES ('p1', 'prof', 'X', 'p1')`); err != nil {
		t.Fatal(err)
	}
	d.Close()

	db, trip := dbconntest.OpenPath(t, path)
	competing := dbconntest.CompetingWriter(t, path)
	fired := false
	trip.BeforeExec = func(string) {
		if !fired {
			fired = true
			competing()
		}
	}
	if _, err := Add(context.Background(), db, "prof", "p1", "hot", ""); err != nil {
		t.Fatalf("Add failed when a writer committed between its read and its write: %v", err)
	}
	if !fired {
		t.Fatal("the hook never ran")
	}
}
