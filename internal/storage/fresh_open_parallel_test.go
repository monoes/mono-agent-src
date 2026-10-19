package storage

import (
	"context"
	"database/sql"
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"
)

// A fresh database's first ping is I/O, not a lock wait: opening its first
// connection switches the new file to WAL, a write with fsyncs that took up
// to 13.9s on a loaded disk here (median ~60ms). Tests in parallel failed in
// NewDatabase with "stayed locked for 10s: context deadline exceeded" for a
// file nothing else had open, because the retry deadline also cut off that
// first ping. Here eight fresh databases open and migrate at once, each
// first ping slowed past the retry window, beside a database and a migration
// lock that other holders keep: every one opens, and nothing waits on the
// locks held elsewhere.
func TestFreshDatabasesOpenInParallelWhileOthersHoldLocks(t *testing.T) {
	oldDeadline, oldPing := pingDeadline, pingContext
	defer func() { pingDeadline, pingContext = oldDeadline, oldPing }()
	pingDeadline = 100 * time.Millisecond
	slowIO := 400 * time.Millisecond // longer than the retry window
	pingContext = func(ctx context.Context, db *sql.DB) error {
		time.Sleep(slowIO)
		return oldPing(ctx, db)
	}

	dir := t.TempDir()
	// Held elsewhere for the whole test: another database's write lock and
	// another database's migration lock.
	other, err := sql.Open("sqlite", "file:"+filepath.Join(dir, "other.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer other.Close()
	conn, err := other.Conn(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	if _, err := conn.ExecContext(context.Background(), `BEGIN EXCLUSIVE`); err != nil {
		t.Fatal(err)
	}
	defer conn.ExecContext(context.Background(), `ROLLBACK`) //nolint:errcheck
	lf, err := os.OpenFile(filepath.Join(dir, "other.db.migrate.lock"), os.O_CREATE|os.O_RDWR, 0o600)
	if err != nil {
		t.Fatal(err)
	}
	defer lf.Close()
	if err := lockFile(lf); err != nil {
		t.Fatal(err)
	}
	defer unlockFile(lf) //nolint:errcheck

	const n = 8
	var wg sync.WaitGroup
	errs := make(chan error, n)
	for i := 0; i < n; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			db, err := NewDatabase(filepath.Join(dir, fmt.Sprintf("fresh-%d.db", i)))
			if err != nil {
				errs <- err
				return
			}
			defer db.Close()
			if err := db.ApplyMigrations(); err != nil {
				errs <- fmt.Errorf("fresh-%d: migrations: %w", i, err)
			}
		}(i)
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		t.Error(err)
	}
}
