package storage

import (
	"context"
	"database/sql"
	"errors"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestNewDatabaseGivesUpOnALockedDatabase(t *testing.T) {
	oldDeadline, oldPing := pingDeadline, pingContext
	defer func() { pingDeadline, pingContext = oldDeadline, oldPing }()
	pingDeadline = 300 * time.Millisecond
	calls := 0
	pingContext = func(ctx context.Context, db *sql.DB) error {
		calls++
		return errors.New("database is locked (5) (SQLITE_BUSY)")
	}

	path := filepath.Join(t.TempDir(), "locked.db")
	start := time.Now()
	_, err := NewDatabase(path)
	if err == nil {
		t.Fatal("expected an error for a database that stays locked")
	}
	if elapsed := time.Since(start); elapsed > 2*time.Second {
		t.Fatalf("retried for %s despite a %s deadline", elapsed, pingDeadline)
	}
	if !strings.Contains(err.Error(), "locked") || !strings.Contains(err.Error(), "locked.db") {
		t.Fatalf("error should name the path and say locked: %v", err)
	}
	if calls < 2 {
		t.Fatalf("expected retries before giving up, got %d calls", calls)
	}
}

func TestNewDatabaseRetriesBusyThenSucceeds(t *testing.T) {
	oldPing := pingContext
	defer func() { pingContext = oldPing }()
	calls := 0
	pingContext = func(ctx context.Context, db *sql.DB) error {
		calls++
		if calls < 3 {
			return errors.New("SQLITE_BUSY")
		}
		return oldPing(ctx, db)
	}
	db, err := NewDatabase(filepath.Join(t.TempDir(), "ok.db"))
	if err != nil {
		t.Fatal(err)
	}
	db.DB.Close()
}

// The window ends the retrying: once it has passed no retry starts, and a
// locked database fails after the window and the one retry in flight (each
// waits at most SQLite's busy_timeout, here 300ms).
func TestNewDatabaseRetryWindowEndsTheRetrying(t *testing.T) {
	oldDeadline, oldPing := pingDeadline, pingContext
	defer func() { pingDeadline, pingContext = oldDeadline, oldPing }()
	pingDeadline = 200 * time.Millisecond
	calls := 0
	pingContext = func(ctx context.Context, db *sql.DB) error {
		calls++
		if calls > 1 {
			time.Sleep(300 * time.Millisecond) // a retry sitting out the busy timeout
		}
		return errors.New("database is locked (5) (SQLITE_BUSY)")
	}
	start := time.Now()
	_, err := NewDatabase(filepath.Join(t.TempDir(), "held.db"))
	if err == nil || !strings.Contains(err.Error(), "stayed locked") {
		t.Fatalf("err = %v, want the locked error", err)
	}
	if calls > 3 {
		t.Errorf("%d pings for a %s window of 300ms retries: retries went on after it", calls, pingDeadline)
	}
	if elapsed := time.Since(start); elapsed > 5*time.Second {
		t.Fatalf("NewDatabase took %s for a %s window", elapsed, pingDeadline)
	}
}

// A retry that got the lock and is slow is disk I/O, not a lock: it is
// awaited, even past the window. (Concurrent first starts of a fresh database
// meet exactly this: one answers busy, then its retry switches the file to
// WAL, which took up to 14s on a loaded disk.)
func TestNewDatabaseAwaitsASlowRetryAfterBusy(t *testing.T) {
	oldDeadline, oldPing := pingDeadline, pingContext
	defer func() { pingDeadline, pingContext = oldDeadline, oldPing }()
	pingDeadline = 100 * time.Millisecond
	calls := 0
	pingContext = func(ctx context.Context, db *sql.DB) error {
		calls++
		if calls == 1 {
			return errors.New("database is locked (5) (SQLITE_BUSY)")
		}
		time.Sleep(400 * time.Millisecond) // the WAL switch on a loaded disk
		return oldPing(ctx, db)            // the real ping gives up when ctx has
	}
	db, err := NewDatabase(filepath.Join(t.TempDir(), "slow-retry.db"))
	if err != nil {
		t.Fatalf("a slow retry after a busy answer failed: %v", err)
	}
	db.DB.Close()
}

// The first open is awaited however slow the machine is: a slow answer that
// is not SQLITE_BUSY is not a locked database.
func TestNewDatabaseAwaitsASlowFirstOpen(t *testing.T) {
	oldDeadline, oldPing := pingDeadline, pingContext
	defer func() { pingDeadline, pingContext = oldDeadline, oldPing }()
	pingDeadline = 50 * time.Millisecond
	pingContext = func(ctx context.Context, db *sql.DB) error {
		time.Sleep(300 * time.Millisecond) // a loaded disk
		return oldPing(ctx, db)            // the real ping gives up when ctx has
	}
	db, err := NewDatabase(filepath.Join(t.TempDir(), "slow.db"))
	if err != nil {
		t.Fatalf("a slow but healthy open failed: %v", err)
	}
	db.DB.Close()
}
