package storage

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"
)

func TestExplainOpenErrorTable(t *testing.T) {
	dir := filepath.Join("home", "u", ".monoagent")
	cases := []struct {
		name string
		err  error
		want string
	}{
		{"busy", errors.New("database is locked (5) (SQLITE_BUSY)"), "close the app or the daemon"},
		{"stayed locked", fmt.Errorf("database x %w for 10s: %w", errStayedLocked, errors.New("SQLITE_BUSY")), "close the app or the daemon"},
		{"full text", errors.New("database or disk is full (13) (SQLITE_FULL)"), "free disk space"},
		{"full errno", fmt.Errorf("write: %w", syscall.ENOSPC), "free disk space"},
		{"readonly text", errors.New("attempt to write a readonly database (8) (SQLITE_READONLY)"), "permissions"},
		{"readonly errno", fmt.Errorf("x: %w", syscall.EROFS), "permissions"},
		{"cantopen", errors.New("unable to open database file: out of memory (14)"), "permissions"},
		{"denied", fmt.Errorf("mkdir: %w", os.ErrPermission), "permissions"},
		{"corrupt", errors.New("database disk image is malformed (11) (SQLITE_CORRUPT)"), "restore it from a backup"},
		{"notadb", errors.New("file is not a database (26) (SQLITE_NOTADB)"), "restore it from a backup"},
	}
	for _, c := range cases {
		got := ExplainOpenError(c.err, dir)
		if got == c.err || !strings.Contains(got.Error(), c.want) {
			t.Errorf("%s: %v, want hint %q", c.name, got, c.want)
			continue
		}
		if !strings.Contains(got.Error(), dir) {
			t.Errorf("%s: folder not named: %v", c.name, got)
		}
		if !strings.Contains(got.Error(), c.err.Error()) {
			t.Errorf("%s: original text lost: %v", c.name, got)
		}
		if !errors.Is(got, c.err) {
			t.Errorf("%s: errors.Is lost", c.name)
		}
	}
	if err := errors.New("some other failure"); ExplainOpenError(err, dir) != err {
		t.Error("an unknown error must pass through unchanged")
	}
	if ExplainOpenError(nil, dir) != nil {
		t.Error("nil must stay nil")
	}
	if !errors.Is(ExplainOpenError(fmt.Errorf("w: %w", syscall.ENOSPC), dir), syscall.ENOSPC) {
		t.Error("errno must stay reachable through the hint")
	}
}

func TestNewDatabaseExplainsAFullDisk(t *testing.T) {
	old := pingContext
	defer func() { pingContext = old }()
	pingContext = func(context.Context, *sql.DB) error {
		return errors.New("database or disk is full (13) (SQLITE_FULL)")
	}
	_, err := NewDatabase(filepath.Join(t.TempDir(), "a.db"))
	if err == nil || !strings.Contains(err.Error(), "free disk space") || !strings.Contains(err.Error(), "SQLITE_FULL") {
		t.Fatalf("got %v", err)
	}
}

func TestApplyMigrationsGivesUpWhenTheLockStaysHeld(t *testing.T) {
	old := migrateLockTimeout
	defer func() { migrateLockTimeout = old }()
	migrateLockTimeout = 300 * time.Millisecond

	path := filepath.Join(t.TempDir(), "held.db")
	db, err := NewDatabase(path)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	holder, err := os.OpenFile(path+".migrate.lock", os.O_CREATE|os.O_RDWR, 0o600)
	if err != nil {
		t.Fatal(err)
	}
	defer holder.Close()
	if err := lockFile(holder); err != nil {
		t.Fatal(err)
	}
	defer unlockFile(holder) //nolint:errcheck

	start := time.Now()
	err = db.ApplyMigrations()
	if !errors.Is(err, errMigrationLockTimeout) {
		t.Fatalf("got %v, want errMigrationLockTimeout", err)
	}
	if time.Since(start) > 5*time.Second {
		t.Fatalf("waited %s", time.Since(start))
	}
	if !strings.Contains(err.Error(), "migrat") || !strings.Contains(err.Error(), "close") {
		t.Fatalf("no next step: %v", err)
	}
}

func TestApplyMigrationsWaitsForAShortLock(t *testing.T) {
	path := filepath.Join(t.TempDir(), "short.db")
	db, err := NewDatabase(path)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	holder, err := os.OpenFile(path+".migrate.lock", os.O_CREATE|os.O_RDWR, 0o600)
	if err != nil {
		t.Fatal(err)
	}
	defer holder.Close()
	if err := lockFile(holder); err != nil {
		t.Fatal(err)
	}
	go func() { time.Sleep(300 * time.Millisecond); unlockFile(holder) }() //nolint:errcheck
	if err := db.ApplyMigrations(); err != nil {
		t.Fatalf("a lock released within the wait must not fail: %v", err)
	}
}
