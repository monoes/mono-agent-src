package orgsvc

import (
	"testing"

	"github.com/monoes/mono-agent/internal/storage"
	"github.com/monoes/mono-agent/internal/testdb"
)

func rootIDs(t *testing.T, db *storage.Database) []string {
	t.Helper()
	roots, err := ProfileRoots(db.DB)
	if err != nil {
		t.Fatal(err)
	}
	var ids []string
	for _, r := range roots {
		ids = append(ids, r.ProfileID)
	}
	return ids
}

// The daemon spans the profiles that exist: a missing "default" is not added.
func TestProfileRootsNeverInventsTheDefaultProfile(t *testing.T) {
	dbPath := t.TempDir() + "/org.db"
	testdb.MustSeed(dbPath)
	db, err := storage.NewDatabase(dbPath)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	if err := db.ApplyMigrations(); err != nil {
		t.Fatal(err)
	}
	if _, err := db.DB.Exec(`INSERT INTO profiles (id, name) VALUES ('work', 'Work')`); err != nil {
		t.Fatal(err)
	}
	if got := rootIDs(t, db); len(got) != 2 || got[0] != "default" || got[1] != "work" {
		t.Fatalf("with a default profile: %v", got)
	}
	if _, err := db.DB.Exec(`PRAGMA foreign_keys = OFF`); err != nil {
		t.Fatal(err)
	}
	if _, err := db.DB.Exec(`DELETE FROM profiles WHERE id = 'default'`); err != nil {
		t.Fatal(err)
	}
	if got := rootIDs(t, db); len(got) != 1 || got[0] != "work" {
		t.Fatalf("without a default profile: %v, want only work", got)
	}
}
