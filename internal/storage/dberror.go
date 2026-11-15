package storage

import (
	"errors"
	"fmt"
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
func ExplainOpenError(err error, dir string) error {
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
	case errors.Is(err, syscall.ENOSPC) || has("SQLITE_FULL", "disk is full", "no space left"):
		hint = fmt.Sprintf("the disk holding %s is full: free disk space, then retry", dir)
	case has("SQLITE_CORRUPT", "SQLITE_NOTADB", "malformed", "not a database"):
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

// migrateLockTimeout bounds the wait for another process that is applying
// migrations (replaced in tests).
var migrateLockTimeout = 30 * time.Second

var errMigrationLockTimeout = errors.New("migration lock held")

// lockFileTimeout is lockFile that gives up after timeout: it polls tryLockFile.
func lockFileTimeout(f *os.File, timeout time.Duration) error {
	deadline := time.Now().Add(timeout)
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
		time.Sleep(50 * time.Millisecond)
	}
}
