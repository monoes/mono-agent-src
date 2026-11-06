package testdb

import (
	"os"
	"path/filepath"
	"testing"
)

// Every test gets a database of its own: what one writes must not reach another,
// or tests that share the template would depend on the order they run in.
func TestOpenGivesEveryTestADatabaseOfItsOwn(t *testing.T) {
	a, b := Open(t), Open(t)
	if _, err := a.DB.Exec(`CREATE TABLE marker (x INTEGER)`); err != nil {
		t.Fatal(err)
	}
	var n int
	if err := b.DB.QueryRow(`SELECT count(*) FROM sqlite_master WHERE name = 'marker'`).Scan(&n); err != nil {
		t.Fatal(err)
	}
	if n != 0 {
		t.Error("a table made in one database appeared in another")
	}
}

// The template is what ApplyMigrations makes, so a database from it is already at
// the latest migration: there is nothing left for ApplyMigrations to do.
func TestOpenIsMigrated(t *testing.T) {
	db := Open(t)
	var name string
	if err := db.DB.QueryRow(`SELECT name FROM sqlite_master WHERE type = 'table' AND name = 'api_keys'`).Scan(&name); err != nil {
		t.Fatalf("the newest migration is not in the database: %v", err)
	}
	if err := db.ApplyMigrations(); err != nil {
		t.Errorf("a migrated database must migrate again without error: %v", err)
	}
}

func TestPathIsAFreshFileEachTime(t *testing.T) {
	a, b := Path(t), Path(t)
	if a == b {
		t.Fatalf("two calls returned the same path %s", a)
	}
	for _, p := range []string{a, b} {
		if fi, err := os.Stat(p); err != nil || fi.Size() == 0 {
			t.Errorf("%s is not a database file: %v", p, err)
		}
	}
}

// MustSeed fills a path that has no file, and leaves a file that is there alone.
func TestMustSeedFillsAMissingPathAndKeepsAnExistingFile(t *testing.T) {
	dir := t.TempDir()
	missing := filepath.Join(dir, "new.db")
	MustSeed(missing)
	if fi, err := os.Stat(missing); err != nil || fi.Size() == 0 {
		t.Fatalf("a missing path was not seeded: %v", err)
	}
	kept := filepath.Join(dir, "old.db")
	if err := os.WriteFile(kept, []byte("older"), 0o600); err != nil {
		t.Fatal(err)
	}
	MustSeed(kept)
	if b, _ := os.ReadFile(kept); string(b) != "older" {
		t.Fatal("an existing file was overwritten")
	}
}
