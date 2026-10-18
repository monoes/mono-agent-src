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

// The deadline is wall clock: a ping that ignores its context and holds on
// (as opening a connection does, waiting out the busy timeout) is abandoned
// when the deadline passes, not awaited.
func TestNewDatabaseDeadlineCoversAPingStillWaiting(t *testing.T) {
	oldDeadline, oldPing := pingDeadline, pingContext
	defer func() { pingDeadline, pingContext = oldDeadline, oldPing }()
	pingDeadline = 200 * time.Millisecond
	release := make(chan struct{})
	defer close(release)
	pingContext = func(ctx context.Context, db *sql.DB) error {
		select { // holds on well past the deadline, whatever ctx says
		case <-release:
		case <-time.After(5 * time.Second):
		}
		return errors.New("database is locked (5) (SQLITE_BUSY)")
	}
	start := time.Now()
	_, err := NewDatabase(filepath.Join(t.TempDir(), "held.db"))
	if err == nil || !strings.Contains(err.Error(), "stayed locked") {
		t.Fatalf("err = %v, want the locked error", err)
	}
	if elapsed := time.Since(start); elapsed > 2*time.Second {
		t.Fatalf("NewDatabase took %s with a %s deadline: it waited for the ping", elapsed, pingDeadline)
	}
}
