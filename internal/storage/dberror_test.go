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

func TestExplainMigrateErrorKeysOnSQLiteCodes(t *testing.T) {
	dir := "d"
	misleading := []string{
		"migration 41: malformed JSON in column x",
		"migration 12: read-only view cannot be altered",
		"migration 7: permission denied for role",
		"migration 9: disk is full of stale rows",
		"migration 3: database is locked by design doc",
		"migration 5: unable to open database file template",
		"migration 6: file is not a database template",
	}
	for _, m := range misleading {
		err := errors.New(m)
		if got := ExplainMigrateError(err, dir); got != err {
			t.Errorf("%q was reclassified: %v", m, got)
		}
	}
	if got := ExplainOpenError(errors.New("bad malformed JSON"), dir); got.Error() != "bad malformed JSON" {
		t.Errorf("bare malformed must not mean damaged: %v", got)
	}
	for _, c := range []struct {
		in   error
		want string
	}{
		{errors.New("x (5) (SQLITE_BUSY)"), "close the app or the daemon"},
		{errors.New("x (13) (SQLITE_FULL)"), "free disk space"},
		{errors.New("x (8) (SQLITE_READONLY)"), "permissions"},
		{errors.New("x (11) (SQLITE_CORRUPT)"), "restore it from a backup"},
		{fmt.Errorf("w: %w", syscall.ENOSPC), "free disk space"},
		{fmt.Errorf("w: %w", syscall.EROFS), "permissions"},
	} {
		got := ExplainMigrateError(c.in, dir)
		if !strings.Contains(got.Error(), c.want) || !errors.Is(got, c.in) {
			t.Errorf("%v: got %v", c.in, got)
		}
	}
}

func TestMigrationLockNoticeIsPrintedOnceOnStderrWhenWaiting(t *testing.T) {
	oldN, oldW := migrateLockNoticeAfter, migrateLockNotice
	defer func() { migrateLockNoticeAfter, migrateLockNotice = oldN, oldW }()
	migrateLockNoticeAfter = 100 * time.Millisecond
	var buf strings.Builder
	migrateLockNotice = &buf

	f := func(t *testing.T) *os.File {
		fh, err := os.OpenFile(filepath.Join(t.TempDir(), "n.lock"), os.O_CREATE|os.O_RDWR, 0o600)
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { fh.Close() })
		return fh
	}
	// immediate acquire: silent
	if err := lockFileTimeout(f(t), time.Second); err != nil || buf.Len() != 0 {
		t.Fatalf("err=%v notice=%q", err, buf.String())
	}
	// held: once
	a := f(t)
	b, _ := os.OpenFile(a.Name(), os.O_RDWR, 0)
	defer b.Close()
	if err := lockFile(b); err != nil {
		t.Fatal(err)
	}
	if err := lockFileTimeout(a, 500*time.Millisecond); !errors.Is(err, errMigrationLockTimeout) {
		t.Fatalf("got %v", err)
	}
	if n := strings.Count(buf.String(), "waiting for another monoagentcli or the app"); n != 1 {
		t.Fatalf("notice printed %d times: %q", n, buf.String())
	}
}

func TestMigrationLockBoundIsFiveMinutes(t *testing.T) {
	if migrateLockTimeout != 5*time.Minute {
		t.Fatalf("bound = %s", migrateLockTimeout)
	}
}
