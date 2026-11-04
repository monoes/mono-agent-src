package coderhost

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// lockedDB creates a journal_mode=DELETE database with a coder_sessions table and holds BEGIN EXCLUSIVE on it
// until the test ends (WAL never blocks readers, so it is not used here).
func lockedDB(t *testing.T, name string) (path string, release func()) {
	t.Helper()
	path = filepath.Join(t.TempDir(), name)
	sdb, err := sql.Open("sqlite", "file:"+path)
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	conn, err := sdb.Conn(ctx)
	if err != nil {
		t.Fatal(err)
	}
	for _, q := range []string{"PRAGMA journal_mode=DELETE", "CREATE TABLE coder_sessions (id INTEGER, turn_pid INTEGER, turn_stamp TEXT)", "BEGIN EXCLUSIVE"} {
		if _, err := conn.ExecContext(ctx, q); err != nil {
			t.Fatalf("%s: %v", q, err)
		}
	}
	var done bool
	release = func() {
		if !done {
			done = true
			_, _ = conn.ExecContext(ctx, "ROLLBACK")
			conn.Close()
			sdb.Close()
		}
	}
	t.Cleanup(release)
	return path, release
}

// A registered live host whose database is not locked: the normal verdict, no refusal.
func TestALiveButUnlockedEntryGivesTheNormalVerdict(t *testing.T) {
	needAncestry(t)
	registryDirOf(t)
	SetBusyTimeoutForTest(t, 200*time.Millisecond)
	path, release := lockedDB(t, "host.db")
	unregister, err := RegisterHostDB(path)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(unregister)
	release()
	if in, skipped, busy := CallerInARegisteredTurnLenient(""); in || len(skipped) != 0 || len(busy) != 0 {
		t.Fatalf("in=%v skipped=%v busy=%v", in, skipped, busy)
	}
}

// A dead host's locked database is skipped WITHOUT waiting for the lock.
func TestADeadHostsLockedEntryIsSkippedWithoutWaiting(t *testing.T) {
	needAncestry(t)
	dir := registryDirOf(t)
	SetBusyTimeoutForTest(t, 5*time.Second)
	prev := registeredCheckTimeout
	registeredCheckTimeout = 9 * time.Second // the bound would allow a 5 s wait
	t.Cleanup(func() { registeredCheckTimeout = prev })
	path, _ := lockedDB(t, "dead.db")
	registerRaw(t, dir, canonicalOrSelf(t, path), 1, "no-such-start-stamp")
	setLiveness(t, func(int) string { return "another-stamp" }, func(int) error { return nil })
	start := time.Now()
	in, skipped, busy := CallerInARegisteredTurnLenient("")
	if took := time.Since(start); took > 500*time.Millisecond {
		t.Fatalf("a dead host's locked database cost %s", took)
	}
	if in || len(skipped) != 0 || len(busy) != 0 {
		t.Fatalf("in=%v skipped=%v busy=%v", in, skipped, busy)
	}
	if _, err := CallerInARegisteredTurn(""); err != nil {
		t.Fatalf("the strict scan: %v", err)
	}
}

// Many busy live entries finish inside the overall budget and are ALL reported busy.
func TestManyBusyLiveEntriesFinishInsideTheBudgetAndAreAllReported(t *testing.T) {
	needAncestry(t)
	registryDirOf(t)
	SetBusyTimeoutForTest(t, 200*time.Millisecond)
	const n, budget = 20, 600 * time.Millisecond
	SetScanBudgetForTest(t, budget)
	for i := 0; i < n; i++ {
		path, _ := lockedDB(t, fmt.Sprintf("host%02d.db", i))
		unregister, err := RegisterHostDB(path)
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(unregister)
	}
	start := time.Now()
	in, _, busy := CallerInARegisteredTurnLenient("")
	took := time.Since(start)
	t.Logf("%d locked live entries: %s (budget %s, lock wait 200ms each would be %s)", n, took, budget, n*200*time.Millisecond)
	if in || len(busy) != n {
		t.Fatalf("in=%v busy=%d, want all %d reported", in, len(busy), n)
	}
	if took > budget+400*time.Millisecond {
		t.Fatalf("the scan took %s, past the %s budget", took, budget)
	}
	err := JoinBusy(busy...)
	msg := err.Error()
	if !strings.HasPrefix(msg, "20 coder hosts are busy") || !strings.Contains(msg, "and 15 more") || strings.Count(msg, "coder-hosts") != busyListMax {
		t.Fatalf("the message does not name the first %d entries and the rest: %q", busyListMax, msg)
	}
	var b *BusyHostError
	if !errors.As(err, &b) || b.JSONErrorFields()["code"] != "host_busy" || len(b.JSONErrorFields()["entries"].([]string)) != busyListMax || b.JSONErrorFields()["busy_count"] != n {
		t.Fatalf("json fields: %v", b.JSONErrorFields())
	}
}

// Every busy live entry is reported, not only the first.
func TestEveryBusyLiveEntryIsReportedNotOnlyTheFirst(t *testing.T) {
	needAncestry(t)
	registryDirOf(t)
	SetBusyTimeoutForTest(t, 100*time.Millisecond)
	for i := 0; i < 3; i++ {
		path, _ := lockedDB(t, fmt.Sprintf("h%d.db", i))
		unregister, err := RegisterHostDB(path)
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(unregister)
	}
	_, _, busy := CallerInARegisteredTurnLenient("")
	if len(busy) != 3 {
		t.Fatalf("busy = %d, want 3", len(busy))
	}
	if msg := JoinBusy(busy...).Error(); !strings.HasPrefix(msg, "3 coder hosts are busy") || strings.Count(msg, "coder-hosts") != 3 {
		t.Fatalf("%q", msg)
	}
}

type coded struct{ code int }

func (e *coded) Error() string { return fmt.Sprintf("sqlite error %d", e.code) }
func (e *coded) Code() int     { return e.code }

// isBusy: SQLITE_BUSY (5) and SQLITE_LOCKED (6) and their extended forms, however wrapped; nothing else. The
// BUSY case is also a real error out of a locked database.
func TestIsBusyCoversBusyAndLockedInEveryForm(t *testing.T) {
	for _, c := range []struct {
		name string
		err  error
		want bool
	}{
		{"BUSY", &coded{5}, true},
		{"LOCKED", &coded{6}, true},
		{"BUSY_RECOVERY", &coded{5 | 1<<8}, true},
		{"BUSY_SNAPSHOT", &coded{5 | 2<<8}, true},
		{"LOCKED_SHAREDCACHE", &coded{6 | 1<<8}, true},
		{"wrapped", fmt.Errorf("query: %w", &coded{6}), true},
		{"joined", errors.Join(errors.New("x"), &coded{5}), true},
		{"ERROR", &coded{1}, false},
		{"CONSTRAINT", &coded{19}, false},
		{"NOTADB", &coded{26}, false},
		{"plain", errors.New("database is locked"), false},
		{"nil", nil, false},
	} {
		if got := isBusy(c.err); got != c.want {
			t.Errorf("%s: isBusy = %v, want %v", c.name, got, c.want)
		}
	}
	path, _ := lockedDB(t, "real.db")
	SetBusyTimeoutForTest(t, 100*time.Millisecond)
	_, err := CallerInATurnAt(path)
	if !errors.Is(err, ErrDatabaseBusy) {
		t.Fatalf("a real locked database: %v", err)
	}
	if strings.Contains(err.Error(), "SQLITE") || strings.Contains(err.Error(), "locked") && !errors.Is(err, ErrDatabaseBusy) {
		t.Fatalf("driver text leaked: %v", err)
	}
}
