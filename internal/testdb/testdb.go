// Package testdb gives tests a migrated database without paying for the
// migrations each time.
//
// Applying every migration to a new database costs a few milliseconds, and about
// two seconds of CPU under the race detector, which CI runs: a package of
// a hundred tests that each make one spends minutes on it, and slows every other
// package that runs beside it. The migrations run once per test binary here, and
// each test gets a copy of the result, which is about twenty times cheaper.
package testdb

import (
	"os"
	"path/filepath"
	"sync"
	"testing"

	"github.com/monoes/mono-agent/internal/storage"
)

var (
	once     sync.Once
	template []byte
	buildErr error
)

// build migrates a database once and returns the bytes of its file.
func build() ([]byte, error) {
	dir, err := os.MkdirTemp("", "monoagent-testdb-")
	if err != nil {
		return nil, err
	}
	defer os.RemoveAll(dir)
	path := filepath.Join(dir, "template.db")
	db, err := storage.NewDatabase(path)
	if err != nil {
		return nil, err
	}
	if err := db.ApplyMigrations(); err != nil {
		db.Close()
		return nil, err
	}
	// Everything must be in the one file that is copied, not in a write-ahead log.
	if _, err := db.DB.Exec(`PRAGMA wal_checkpoint(TRUNCATE)`); err != nil {
		db.Close()
		return nil, err
	}
	if err := db.Close(); err != nil {
		return nil, err
	}
	return os.ReadFile(path)
}

// MustSeed puts a copy of the migrated database at path unless a file is
// already there, for fixtures that open a database by a path of their own and
// then call ApplyMigrations (which finds nothing left to do). A file that
// exists, such as an older schema a test wrote on purpose, is left alone.
func MustSeed(path string) {
	if _, err := os.Stat(path); err == nil {
		return
	}
	once.Do(func() { template, buildErr = build() })
	if buildErr != nil {
		panic("testdb: migrating the template database: " + buildErr.Error())
	}
	if err := os.WriteFile(path, template, 0o600); err != nil {
		panic("testdb: " + err.Error())
	}
}

// Path returns the path of a migrated database of its own, in t.TempDir().
func Path(t testing.TB) string {
	t.Helper()
	once.Do(func() { template, buildErr = build() })
	if buildErr != nil {
		t.Fatalf("testdb: migrating the template database: %v", buildErr)
	}
	path := filepath.Join(t.TempDir(), "test.db")
	if err := os.WriteFile(path, template, 0o600); err != nil {
		t.Fatalf("testdb: %v", err)
	}
	return path
}

// Open is Path followed by opening the database, which is closed when the test
// ends. ApplyMigrations finds nothing left to do.
func Open(t testing.TB) *storage.Database {
	t.Helper()
	db, err := storage.NewDatabase(Path(t))
	if err != nil {
		t.Fatalf("testdb: %v", err)
	}
	t.Cleanup(func() { db.Close() })
	if err := db.ApplyMigrations(); err != nil {
		t.Fatalf("testdb: %v", err)
	}
	return db
}
