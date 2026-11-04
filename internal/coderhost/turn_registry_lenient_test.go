package coderhost

import (
	"context"
	"database/sql"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// The lenient scan (the recovery verbs) skips an unreadable live-host entry and still judges the entries
// after it; the strict scan stops at it. The unreadable entry sorts first on purpose.
var errUnreadable = errors.New("file is not a database")

func TestTheLenientScanFindsATurnPastAnUnreadableEntryAndTheStrictScanStopsAtIt(t *testing.T) {
	needAncestry(t)
	dir := registryDirOf(t)
	base := t.TempDir()
	junk, turn, clean := filepath.Join(base, "a-junk.db"), filepath.Join(base, "z-turn.db"), filepath.Join(base, "m-clean.db")
	for _, p := range []string{junk, turn, clean} {
		writeEntry(t, p, "x", 0o600)
	}
	for _, p := range []string{junk, turn, clean} {
		registerRaw(t, dir, canonicalOrSelf(t, p), os.Getpid(), ProcStamp(os.Getpid()))
	}
	prev := registeredDBCheck
	t.Cleanup(func() { registeredDBCheck = prev })
	registeredDBCheck = func(p string) (bool, error) {
		switch filepath.Base(p) {
		case "a-junk.db":
			return false, errUnreadable
		case "z-turn.db":
			return true, nil
		}
		return false, nil
	}

	if in, skipped, _ := CallerInARegisteredTurnLenient(""); !in || len(skipped) != 1 || !strings.Contains(skipped[0].Error(), "a-junk.db") {
		t.Fatalf("lenient: in=%v skipped=%v; want the turn found and the junk entry reported", in, skipped)
	}
	if in, err := CallerInARegisteredTurn(""); in || err == nil || !strings.Contains(err.Error(), "a-junk.db") {
		t.Fatalf("strict: in=%v err=%v; want the unreadable entry to refuse", in, err)
	}

	registeredDBCheck = func(p string) (bool, error) {
		if filepath.Base(p) == "a-junk.db" {
			return false, errUnreadable
		}
		return false, nil
	}
	if in, skipped, _ := CallerInARegisteredTurnLenient(""); in || len(skipped) != 1 {
		t.Fatalf("lenient with no readable turn: in=%v skipped=%v; want not in a turn, one entry skipped", in, skipped)
	}
}

// A locked database must answer ErrDatabaseBusy inside registeredCheckTimeout: the registry check waits for the
// lock only two thirds of it, so a scan reports a busy live host instead of abandoning the check as "no answer".
func TestTheRegistryCheckAnswersBusyBeforeItIsAbandoned(t *testing.T) {
	needAncestry(t)
	registryDirOf(t)
	db := filepath.Join(t.TempDir(), "host.db")
	sdb, err := sql.Open("sqlite", "file:"+db)
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	conn, err := sdb.Conn(ctx)
	if err != nil {
		t.Fatal(err)
	}
	for _, q := range []string{"PRAGMA journal_mode=DELETE", "CREATE TABLE coder_sessions (id INTEGER)", "BEGIN EXCLUSIVE"} {
		if _, err := conn.ExecContext(ctx, q); err != nil {
			t.Fatalf("%s: %v", q, err)
		}
	}
	t.Cleanup(func() { _, _ = conn.ExecContext(ctx, "ROLLBACK"); conn.Close(); sdb.Close() })
	unregister, err := RegisterHostDB(db)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(unregister)

	prevTimeout := registeredCheckTimeout
	registeredCheckTimeout = 600 * time.Millisecond // the lock wait is capped at 400 ms; the default 5 s is far above
	t.Cleanup(func() { registeredCheckTimeout = prevTimeout })
	start := time.Now()
	in, skipped, busy := CallerInARegisteredTurnLenient("")
	if in || len(skipped) != 0 || len(busy) != 1 || !errors.Is(busy[0], ErrDatabaseBusy) {
		t.Fatalf("in=%v skipped=%v busy=%v: a locked live host was not reported busy within the bound", in, skipped, busy)
	}
	if took := time.Since(start); took >= registeredCheckTimeout {
		t.Fatalf("answered after %s, not inside the %s bound", took, registeredCheckTimeout)
	}
}
