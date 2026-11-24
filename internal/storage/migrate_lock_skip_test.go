//go:build !windows

package storage

import (
	"os"
	"path/filepath"
	"testing"
)

// With nothing pending, an open does not take the migration lock, so a state directory that cannot be
// written (here: a lock file the process may only read) does not fail an open that has nothing to do.
func TestApplyMigrationsSkipsTheLockWhenNothingIsPending(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("root ignores file modes")
	}
	path := filepath.Join(t.TempDir(), "done.db")
	db, err := NewDatabase(path)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	if err := db.ApplyMigrations(); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(path+".migrate.lock", 0o400); err != nil {
		t.Fatal(err)
	}
	if err := db.ApplyMigrations(); err != nil {
		t.Fatalf("a second open with nothing pending failed on the lock: %v", err)
	}
}
