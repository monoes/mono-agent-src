package storage

import (
	"errors"
	"fmt"
	"io"
	"os"
	"strings"
	"syscall"
	"time"
)

// openError puts a short next step in front of the original error, which stays
// reachable through Unwrap (errors.Is / errors.As keep working).
type openError struct {
	hint string
	err  error
}

func (e *openError) Error() string { return e.hint + ": " + e.err.Error() }
func (e *openError) Unwrap() error { return e.err }

// ExplainOpenError names the next step for the failures of opening the database
// that a person can fix (locked, disk full, read-only, cannot open, damaged);
// dir is the folder the database lives in. Anything else passes through unchanged.
func ExplainOpenError(err error, dir string) error { return explain(err, dir, false) }

// ExplainMigrateError is ExplainOpenError for the errors of applying migrations: a migration's own text may contain
// any word, so only errnos and SQLite result codes (SQLITE_*) are classified.
func ExplainMigrateError(err error, dir string) error { return explain(err, dir, true) }

func explain(err error, dir string, strict bool) error {
	if err == nil {
		return nil
	}
	msg := err.Error()
	has := func(subs ...string) bool {
		for _, s := range subs {
			if strings.Contains(msg, s) {
				return true
			}
		}
		return false
	}
	var hint string
	switch {
	case strict:
		hint = explainCode(err, dir, has)
		if hint == "" {
			return err
		}
	case errors.Is(err, syscall.ENOSPC) || has("SQLITE_FULL", "disk is full", "no space left"):
		hint = fmt.Sprintf("the disk holding %s is full: free disk space, then retry", dir)
	case has("SQLITE_CORRUPT", "SQLITE_NOTADB", "database disk image is malformed", "file is not a database"):
		hint = fmt.Sprintf("the database in %s is damaged: restore it from a backup", dir)
	case errors.Is(err, syscall.EROFS) || has("SQLITE_READONLY", "readonly database", "read-only"):
		hint = fmt.Sprintf("the database in %s is read-only: check the permissions of that folder and its files", dir)
	case errors.Is(err, errMigrationLockTimeout):
		return err // already says what to do
	case has("SQLITE_BUSY", "database is locked"):
		hint = fmt.Sprintf("the database in %s is in use by another process: close the app or the daemon that is holding it, then retry", dir)
	case errors.Is(err, os.ErrPermission) || errors.Is(err, syscall.EACCES) || has("SQLITE_CANTOPEN", "unable to open database file", "permission denied"):
		hint = fmt.Sprintf("cannot open the database in %s: check that the folder exists and that you can read and write it (permissions)", dir)
	default:
		return err
	}
	return &openError{hint: hint, err: err}
}

// explainCode is the strict classification: errnos and SQLITE_* codes only.
func explainCode(err error, dir string, has func(...string) bool) string {
	switch {
	case errors.Is(err, syscall.ENOSPC) || has("SQLITE_FULL"):
		return fmt.Sprintf("the disk holding %s is full: free disk space, then retry", dir)
	case has("SQLITE_CORRUPT", "SQLITE_NOTADB"):
		return fmt.Sprintf("the database in %s is damaged: restore it from a backup", dir)
	case errors.Is(err, syscall.EROFS) || has("SQLITE_READONLY"):
		return fmt.Sprintf("the database in %s is read-only: check the permissions of that folder and its files", dir)
	case has("SQLITE_BUSY") && !errors.Is(err, errMigrationLockTimeout):
		return fmt.Sprintf("the database in %s is in use by another process: close the app or the daemon that is holding it, then retry", dir)
	}
	return ""
}

// The first start after an upgrade may migrate a big database while other calls wait, so the wait is long; a notice
// goes to stderr (never stdout: --json consumers) once the wait passes migrateLockNoticeAfter. All replaced in tests.
var (
	migrateLockTimeout               = 5 * time.Minute
	migrateLockNoticeAfter           = 2 * time.Second
	migrateLockNotice      io.Writer = os.Stderr
)

var errMigrationLockTimeout = errors.New("migration lock held")

// lockFileTimeout is lockFile that gives up after timeout: it polls tryLockFile.
func lockFileTimeout(f *os.File, timeout time.Duration) error {
	start := time.Now()
	deadline := start.Add(timeout)
	noticed := false
	for {
		ok, err := tryLockFile(f)
		if err != nil {
			return err
		}
		if ok {
			return nil
		}
		if time.Now().After(deadline) {
			return fmt.Errorf("%w for %s: another monoagentcli or the app is applying migrations to this database; "+
				"close it (or the daemon) and retry", errMigrationLockTimeout, timeout)
		}
		if !noticed && time.Since(start) >= migrateLockNoticeAfter {
			noticed = true
			fmt.Fprintln(migrateLockNotice, "waiting for another monoagentcli or the app to finish migrating the database...")
		}
		time.Sleep(50 * time.Millisecond)
	}
}
