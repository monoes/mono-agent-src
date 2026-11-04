package main

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/monoes/mono-agent/internal/coderhost"
	_ "modernc.org/sqlite"
)

// A same-uid process that holds a lock on the database of a live coder host past the busy timeout must not make
// a recovery verb see "no turn" (#1365 item 3). A rollback-journal database is used: WAL never blocks readers.

// lockDB holds BEGIN EXCLUSIVE on a journal_mode=DELETE copy of the database at path until the returned
// release (also run at cleanup) is called.
func lockDB(t *testing.T, path string) (release func()) {
	t.Helper()
	db, err := sql.Open("sqlite", "file:"+path)
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	conn, err := db.Conn(ctx)
	if err != nil {
		t.Fatal(err)
	}
	for _, q := range []string{"PRAGMA journal_mode=DELETE", "BEGIN EXCLUSIVE"} {
		if _, err := conn.ExecContext(ctx, q); err != nil {
			t.Fatalf("%s: %v", q, err)
		}
	}
	var once bool
	release = func() {
		if once {
			return
		}
		once = true
		_, _ = conn.ExecContext(ctx, "ROLLBACK")
		conn.Close()
		db.Close()
	}
	t.Cleanup(release)
	return release
}

// busySetup points the registry at a directory of the test's own and shortens the wait for a lock.
func busySetup(t *testing.T) string {
	t.Helper()
	if coderhost.ProcStamp(os.Getpid()) == "" {
		t.Skip("process ancestry cannot be read here")
	}
	dir := filepath.Join(t.TempDir(), "coder-hosts")
	coderhost.SetRegistryDirForTest(t, dir)
	coderhost.SetBusyTimeoutForTest(t, 200*time.Millisecond)
	return dir
}

// registerLive registers db as the database of this very process acting as a live host.
func registerLive(t *testing.T, db string) {
	t.Helper()
	unregister, err := coderhost.RegisterHostDB(db)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(unregister)
}

// registerDead records db as the database of a host that is certainly gone (its pid carries another start stamp).
func registerDead(t *testing.T, dir, db string) {
	t.Helper()
	canon, err := filepath.EvalSymlinks(db)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	raw, _ := json.Marshal(map[string]any{"db": canon, "pid": os.Getpid(), "stamp": "a-stamp-this-pid-never-had"})
	sum := sha256.Sum256([]byte(canon))
	if err := os.WriteFile(filepath.Join(dir, hex.EncodeToString(sum[:])), raw, 0o600); err != nil {
		t.Fatal(err)
	}
}

// entryFileOf is the registry entry file of the database at path (the file name is the hash of its canonical path).
func entryFileOf(t *testing.T, dir, path string) string {
	t.Helper()
	canon, err := filepath.EvalSymlinks(path)
	if err != nil {
		t.Fatal(err)
	}
	sum := sha256.Sum256([]byte(canon))
	return filepath.Join(dir, hex.EncodeToString(sum[:]))
}

func wantRetryText(t *testing.T, err error, db, entryFile string) {
	t.Helper()
	if err == nil {
		t.Fatal("a recovery verb saw no turn although the live host's database is locked")
	}
	if !errors.Is(err, coderhost.ErrDatabaseBusy) {
		t.Fatalf("not the busy error: %v", err)
	}
	want := "the coder host that owns " + db + " is busy (database locked); wait and retry the command; the registry entry for it is " +
		entryFile + ": if that host is not running, delete this file and run the command again"
	if err.Error() != want {
		t.Fatalf("message %q, want %q", err.Error(), want)
	}
	if isOperatorOnly(err) {
		t.Fatalf("the busy refusal must be a plain error, not operator_only: %v", err)
	}
}

// (a) the named database is the live host's and is locked.
func TestARecoveryVerbIsToldToRetryWhenTheNamedLiveHostDatabaseIsLocked(t *testing.T) {
	dir := busySetup(t)
	db := newHILTestDB(t)
	registerLive(t, db)
	lockDB(t, db)
	cfg := &globalConfig{DBPath: db}
	_, err := cfg.callerInATurnFor(true)
	wantRetryText(t, err, db, entryFileOf(t, dir, db))
	why, busy := cfg.gateRecovery()
	if busy == nil || why == "" {
		t.Fatalf("gateRecovery did not report the busy host: %q %v", why, busy)
	}
}

// (a2) the process names another database; the live host's registered one is locked.
func TestARecoveryVerbIsToldToRetryWhenARegisteredLiveHostDatabaseIsLocked(t *testing.T) {
	dir := busySetup(t)
	host := newHILTestDB(t)
	registerLive(t, host)
	lockDB(t, host)
	cfg := &globalConfig{DBPath: newHILTestDB(t)}
	canon, _ := filepath.EvalSymlinks(host)
	_, err := cfg.callerInATurnFor(true)
	wantRetryText(t, err, canon, entryFileOf(t, dir, host))
}

// (b) the same lock on the database of a dead host: the operator is let through.
func TestARecoveryVerbIgnoresALockOnADeadHostsDatabase(t *testing.T) {
	dir := busySetup(t)
	db := newHILTestDB(t)
	registerDead(t, dir, db)
	lockDB(t, db)
	for name, named := range map[string]string{"named": db, "registered": newHILTestDB(t)} {
		in, err := (&globalConfig{DBPath: named}).callerInATurnFor(true)
		if in || err != nil {
			t.Fatalf("%s: a dead host's lock locked the operator out: in=%v err=%v", name, in, err)
		}
	}
}

// (b2) a locked database no registered host owns is no turn either.
func TestARecoveryVerbIgnoresALockOnADatabaseNoHostOwns(t *testing.T) {
	busySetup(t)
	db := newHILTestDB(t)
	lockDB(t, db)
	if in, err := (&globalConfig{DBPath: db}).callerInATurnFor(true); in || err != nil {
		t.Fatalf("in=%v err=%v", in, err)
	}
}

// (c) the lock released: the normal verdict.
func TestARecoveryVerbIsNormalOnceTheLockIsReleased(t *testing.T) {
	busySetup(t)
	db := newHILTestDB(t)
	registerLive(t, db)
	release := lockDB(t, db)
	cfg := &globalConfig{DBPath: db}
	if _, err := cfg.callerInATurnFor(true); err == nil {
		t.Fatal("setup: the lock did not refuse")
	}
	release()
	if in, err := cfg.callerInATurnFor(true); in || err != nil {
		t.Fatalf("after the release: in=%v err=%v", in, err)
	}
}

// (d) the plain flavour still refuses, for the named database and for a registered one.
func TestThePlainFlavourStillRefusesALockedDatabase(t *testing.T) {
	busySetup(t)
	host := newHILTestDB(t)
	registerLive(t, host)
	lockDB(t, host)
	if _, err := (&globalConfig{DBPath: host}).callerInATurnFor(false); err == nil {
		t.Fatal("named: the plain flavour let a locked database through")
	}
	if _, err := (&globalConfig{DBPath: newHILTestDB(t)}).callerInATurnFor(false); err == nil {
		t.Fatal("registered: the plain flavour let a locked database through")
	}
}

// (e) a turn recorded in a readable database still wins over a busy one.
func TestATurnInAReadableDatabaseWinsOverABusyLiveHostDatabase(t *testing.T) {
	busySetup(t)
	turnDB := registeredTurnDB(t) // sets its own registry directory; the busy seam stays
	coderhost.SetBusyTimeoutForTest(t, 200*time.Millisecond)
	locked := newHILTestDB(t)
	registerLive(t, locked)
	lockDB(t, locked)
	for name, named := range map[string]string{"named busy": locked, "named elsewhere": newHILTestDB(t), "named turn": turnDB} {
		in, err := (&globalConfig{DBPath: named}).callerInATurnFor(true)
		if err != nil || !in {
			t.Fatalf("%s: the turn was hidden by a busy database: in=%v err=%v", name, in, err)
		}
	}
}

// (f) the message names the one database and its entry file, and nothing of another host.
func TestTheBusyMessageNamesOnlyTheDatabaseAndItsEntryFile(t *testing.T) {
	dir := busySetup(t)
	other := newHILTestDB(t)
	registerLive(t, other) // another host, readable
	host := newHILTestDB(t)
	registerLive(t, host)
	lockDB(t, host)
	_, err := (&globalConfig{DBPath: newHILTestDB(t)}).callerInATurnFor(true)
	canon, _ := filepath.EvalSymlinks(host)
	wantRetryText(t, err, canon, entryFileOf(t, dir, host))
	_, err = (&globalConfig{DBPath: host}).callerInATurnFor(true)
	wantRetryText(t, err, host, entryFileOf(t, dir, host))
	for _, bad := range []string{other, filepath.Base(entryFileOf(t, dir, other)), "sqlite", "SQLITE"} {
		if strings.Contains(err.Error(), bad) {
			t.Fatalf("the message leaks %q: %v", bad, err)
		}
	}
}

// The registry is writable by the same OS user, so the path an entry records is untrusted text: control
// characters and newlines in it never reach the operator's terminal.
func TestTheBusyMessageStripsControlCharactersFromARecordedPath(t *testing.T) {
	dir := busySetup(t)
	host := filepath.Join(t.TempDir(), "hil\n\x1b[31mred\r\u2028x.db")
	writeMigratedDB(t, host)
	registerLive(t, host)
	lockDB(t, host)
	_, err := (&globalConfig{DBPath: newHILTestDB(t)}).callerInATurnFor(true)
	if err == nil || !errors.Is(err, coderhost.ErrDatabaseBusy) {
		t.Fatalf("not the busy error: %v", err)
	}
	for _, r := range err.Error() {
		if r < 0x20 || r == 0x7f || r == 0x2028 || r == 0x2029 {
			t.Fatalf("a control character reached the message: %q", err.Error())
		}
	}
	want := filepath.Join(filepath.Dir(host), "hil[31mredx.db")
	canon, _ := filepath.EvalSymlinks(filepath.Dir(host))
	want = filepath.Join(canon, "hil[31mredx.db")
	if !strings.Contains(err.Error(), "owns "+want+" is busy") || !strings.Contains(err.Error(), entryFileOf(t, dir, host)) {
		t.Fatalf("the sanitized message is not the expected one: %q", err.Error())
	}
}

// A registry directory with a space and a single quote in its name: the entry file in the message is
// shell-quoted so the operator can paste it into rm.
func TestTheBusyMessageQuotesTheEntryFileOfAnOddlyNamedRegistry(t *testing.T) {
	if coderhost.ProcStamp(os.Getpid()) == "" {
		t.Skip("process ancestry cannot be read here")
	}
	dir := filepath.Join(t.TempDir(), "co der's hosts")
	coderhost.SetRegistryDirForTest(t, dir)
	coderhost.SetBusyTimeoutForTest(t, 200*time.Millisecond)
	db := newHILTestDB(t)
	registerLive(t, db)
	lockDB(t, db)
	_, err := (&globalConfig{DBPath: db}).callerInATurnFor(true)
	if !errors.Is(err, coderhost.ErrDatabaseBusy) {
		t.Fatalf("not the busy error: %v", err)
	}
	file := entryFileOf(t, dir, db)
	want := "'" + strings.ReplaceAll(file, "'", `'\''`) + "'"
	if !strings.Contains(err.Error(), "is "+want+": if that host") {
		t.Fatalf("the entry file is not shell-quoted: %q (want %s)", err.Error(), want)
	}
}
