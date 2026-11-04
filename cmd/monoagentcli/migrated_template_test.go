package main

import (
	"os"
	"testing"

	"github.com/monoes/mono-agent/internal/storage"
)

// Every copy of the migrated template is its own database: a row written to one is in no other, the
// bytes carry the whole schema without a -wal or -shm beside them, and the template itself stays as
// migrated.
func TestMigratedTemplateCopiesAreIndependentAndSelfContained(t *testing.T) {
	a, b := newHILTestDB(t), newHILTestDB(t)
	if a == b {
		t.Fatal("two copies share one path")
	}
	for _, p := range []string{a, b} {
		for _, side := range []string{"-wal", "-shm"} {
			if _, err := os.Stat(p + side); err == nil {
				t.Fatalf("%s%s exists before the copy was opened", p, side)
			}
		}
	}
	da, err := storage.NewDatabase(a)
	if err != nil {
		t.Fatal(err)
	}
	defer da.Close()
	if err := da.ApplyMigrations(); err != nil { // already migrated: must be a no-op, not a failure
		t.Fatal(err)
	}
	if _, err := da.DB.Exec(`INSERT INTO workflows (id, name, is_active, profile_id) VALUES ('only-in-a','A',1,'default')`); err != nil {
		t.Fatal(err)
	}
	db, err := storage.NewDatabase(b)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	var n int
	if err := db.DB.QueryRow(`SELECT COUNT(*) FROM workflows WHERE id = 'only-in-a'`).Scan(&n); err != nil || n != 0 {
		t.Fatalf("a row written to one copy shows in another: n=%d err=%v", n, err)
	}
	var profiles int
	if err := db.DB.QueryRow(`SELECT COUNT(*) FROM profiles WHERE id = 'default'`).Scan(&profiles); err != nil || profiles != 1 {
		t.Fatalf("the default profile of the migration is missing: n=%d err=%v", profiles, err)
	}
	c := newHILTestDB(t)
	dc, err := storage.NewDatabase(c)
	if err != nil {
		t.Fatal(err)
	}
	defer dc.Close()
	if err := dc.DB.QueryRow(`SELECT COUNT(*) FROM workflows`).Scan(&n); err != nil || n != 0 {
		t.Fatalf("a later copy is not as migrated: workflows=%d err=%v", n, err)
	}
}
