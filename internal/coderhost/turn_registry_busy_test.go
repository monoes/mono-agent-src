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

	sqlite3 "modernc.org/sqlite/lib"
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

// A scripted check on one registered database: each call answers the next outcome.
func scripted(t *testing.T, outcomes ...error) *int {
	t.Helper()
	calls := new(int)
	prev := registeredDBCheck
	registeredDBCheck = func(string, time.Duration) (bool, error) {
		i := *calls
		*calls++
		if i >= len(outcomes) {
			i = len(outcomes) - 1
		}
		return false, outcomes[i]
	}
	t.Cleanup(func() { registeredDBCheck = prev })
	return calls
}

func registerOne(t *testing.T) {
	t.Helper()
	path, release := lockedDB(t, "one.db")
	release()
	unregister, err := RegisterHostDB(path)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(unregister)
}

// The classification: on a LIVE host a non-answer (timeout, lock, SQLITE_PROTOCOL, an unknown code) is busy, in
// pass 1 and in pass 2; damage (not a database, corrupt, cannot open, missing table, not a file) is skipped.
func TestANonAnswerOnALiveHostIsBusyInBothPassesAndDamageIsSkipped(t *testing.T) {
	needAncestry(t)
	timeout := fmt.Errorf("%w within 1s", ErrNoAnswer)
	protocol, unknown := &coded{15}, &coded{99}
	damaged := []error{&coded{26}, &coded{11}, &coded{14}, &coded{1}, &coded{3}, &coded{10}, errors.New("x is not a regular file")}
	for _, c := range []struct {
		name     string
		outcomes []error // pass 1, then pass 2
		busy     bool
	}{
		{"timeout in pass 1", []error{timeout}, true},
		{"timeout in pass 2", []error{ErrDatabaseBusy, timeout}, true},
		{"protocol in pass 1 and 2", []error{protocol, protocol}, true},
		{"protocol in pass 1, locked in pass 2", []error{protocol, ErrDatabaseBusy}, true},
		{"locked in pass 1, protocol in pass 2", []error{ErrDatabaseBusy, protocol}, true},
		{"an unknown code in pass 1", []error{unknown, unknown}, true},
		{"locked, then damaged", []error{ErrDatabaseBusy, &coded{26}}, false},
		{"locked, then readable", []error{ErrDatabaseBusy, nil}, false},
	} {
		t.Run(c.name, func(t *testing.T) {
			registryDirOf(t)
			SetBusyTimeoutForTest(t, 50*time.Millisecond)
			registerOne(t)
			scripted(t, c.outcomes...)
			in, skipped, busy := CallerInARegisteredTurnLenient("")
			if in || (len(busy) == 1) != c.busy {
				t.Fatalf("in=%v busy=%v skipped=%v, want busy=%v", in, busy, skipped, c.busy)
			}
			if !c.busy && len(busy) != 0 {
				t.Fatalf("busy = %v", busy)
			}
		})
	}
	for _, d := range damaged {
		t.Run(fmt.Sprintf("damage %v", d), func(t *testing.T) {
			registryDirOf(t)
			registerOne(t)
			scripted(t, d)
			in, skipped, busy := CallerInARegisteredTurnLenient("")
			if in || len(busy) != 0 || len(skipped) != 1 {
				t.Fatalf("a damaged database of a live host: in=%v skipped=%v busy=%v; it must be skipped (the operator is let through)", in, skipped, busy)
			}
		})
	}
}

// The same non-answers on a DEAD host are skipped, and a dead host is asked once, without waiting.
func TestANonAnswerOnADeadHostIsSkipped(t *testing.T) {
	needAncestry(t)
	for _, e := range []error{fmt.Errorf("%w within 1s", ErrNoAnswer), &coded{15}, ErrDatabaseBusy} {
		t.Run(e.Error(), func(t *testing.T) {
			dir := registryDirOf(t)
			path, release := lockedDB(t, "dead.db")
			release()
			registerRaw(t, dir, canonicalOrSelf(t, path), 1, "no-such-start-stamp")
			setLiveness(t, func(int) string { return "another-stamp" }, func(int) error { return nil })
			calls := scripted(t, e)
			in, skipped, busy := CallerInARegisteredTurnLenient("")
			if in || len(skipped) != 0 || len(busy) != 0 || *calls != 1 {
				t.Fatalf("in=%v skipped=%v busy=%v calls=%d", in, skipped, busy, *calls)
			}
		})
	}
}

// A lock released and retaken between statements gives the check mixed outcomes: whatever it sees, the verdict
// is never "damaged, let through" and never a turn.
func TestAToggledLockNeverMakesALiveHostLookDamaged(t *testing.T) {
	needAncestry(t)
	registryDirOf(t)
	SetBusyTimeoutForTest(t, 20*time.Millisecond)
	path := filepath.Join(t.TempDir(), "toggle.db")
	sdb, err := sql.Open("sqlite", "file:"+path)
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	conn, err := sdb.Conn(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := conn.ExecContext(ctx, "PRAGMA journal_mode=DELETE"); err != nil {
		t.Fatal(err)
	}
	if _, err := conn.ExecContext(ctx, "CREATE TABLE coder_sessions (id INTEGER, turn_pid INTEGER, turn_stamp TEXT)"); err != nil {
		t.Fatal(err)
	}
	stop, stopped := make(chan struct{}), make(chan struct{})
	go func() {
		defer close(stopped)
		for {
			select {
			case <-stop:
				return
			default:
			}
			if _, err := conn.ExecContext(ctx, "BEGIN EXCLUSIVE"); err == nil {
				time.Sleep(40 * time.Millisecond)
				_, _ = conn.ExecContext(ctx, "ROLLBACK")
			}
			time.Sleep(3 * time.Millisecond)
		}
	}()
	t.Cleanup(func() { close(stop); <-stopped; conn.Close(); sdb.Close() })
	unregister, err := RegisterHostDB(path)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(unregister)
	var sawBusy, sawClear int
	for i := 0; i < 40; i++ {
		in, skipped, busy := CallerInARegisteredTurnLenient("")
		if in || len(skipped) != 0 || len(busy) > 1 {
			t.Fatalf("round %d: in=%v skipped=%v busy=%v", i, in, skipped, busy)
		}
		if len(busy) == 1 {
			sawBusy++
		} else {
			sawClear++
		}
	}
	t.Logf("toggled lock: %d busy, %d clear", sawBusy, sawClear)
	if sawBusy == 0 {
		t.Fatal("the lock never made a round busy: the test does not exercise the toggling")
	}
}

// The entry file is shell-quoted in the message when it needs it, with the sanitized path.
func TestTheBusyMessageShellQuotesAnEntryFileWithASpaceAndAQuote(t *testing.T) {
	e := &BusyHostError{DB: "/d.db", File: "/x/co der's hosts/ab\ncd"}
	want := `the registry entry for it is '/x/co der'\''s hosts/abcd': if that host`
	if !strings.Contains(e.Error(), want) {
		t.Fatalf("%q does not contain %q", e.Error(), want)
	}
	plain := &BusyHostError{DB: "/d.db", File: "/x/hosts/abcd"}
	if !strings.Contains(plain.Error(), "is /x/hosts/abcd: if") {
		t.Fatalf("a plain path was quoted: %q", plain.Error())
	}
	many := &BusyHostError{DB: "/d.db", File: "/a b", Others: []string{"/c"}}
	if !strings.Contains(many.Error(), "'/a b', /c:") {
		t.Fatalf("%q", many.Error())
	}
}

// Extended result codes are classified before the primary one: the lock and recovery forms of IOERR and
// READONLY are non-answers of a live host, while the plain codes (and AUTH) stay damage. The constants are the
// driver's own.
func TestExtendedCodesAreClassifiedBeforeThePrimaryCode(t *testing.T) {
	needAncestry(t)
	transient := map[string]int{
		"IOERR_LOCK": sqlite3.SQLITE_IOERR_LOCK, "IOERR_BLOCKED": sqlite3.SQLITE_IOERR_BLOCKED,
		"IOERR_RDLOCK": sqlite3.SQLITE_IOERR_RDLOCK, "IOERR_SHMLOCK": sqlite3.SQLITE_IOERR_SHMLOCK,
		"IOERR_CHECKRESERVEDLOCK": sqlite3.SQLITE_IOERR_CHECKRESERVEDLOCK,
		"IOERR_SHMOPEN":           sqlite3.SQLITE_IOERR_SHMOPEN, "IOERR_SHMSIZE": sqlite3.SQLITE_IOERR_SHMSIZE,
		"IOERR_SHMMAP": sqlite3.SQLITE_IOERR_SHMMAP, "IOERR_NOMEM": sqlite3.SQLITE_IOERR_NOMEM,
		"READONLY_RECOVERY": sqlite3.SQLITE_READONLY_RECOVERY, "READONLY_CANTINIT": sqlite3.SQLITE_READONLY_CANTINIT,
		"READONLY_CANTLOCK": sqlite3.SQLITE_READONLY_CANTLOCK,
	}
	if len(transient) != len(transientExtendedCodes) {
		t.Fatalf("the table holds %d codes, the test %d", len(transientExtendedCodes), len(transient))
	}
	damage := map[string]int{
		"IOERR": sqlite3.SQLITE_IOERR, "READONLY": sqlite3.SQLITE_READONLY, "AUTH": sqlite3.SQLITE_AUTH,
		"CANTOPEN": sqlite3.SQLITE_CANTOPEN, "CORRUPT": sqlite3.SQLITE_CORRUPT, "NOTADB": sqlite3.SQLITE_NOTADB,
		"READONLY_DBMOVED": sqlite3.SQLITE_READONLY_DBMOVED, "READONLY_ROLLBACK": sqlite3.SQLITE_READONLY_ROLLBACK,
		"IOERR_READ": sqlite3.SQLITE_IOERR_READ, "CANTOPEN_ISDIR": sqlite3.SQLITE_CANTOPEN_ISDIR, "AUTH_USER": sqlite3.SQLITE_AUTH_USER,
	}
	for name, code := range transient {
		t.Run("live "+name, func(t *testing.T) {
			registryDirOf(t)
			registerOne(t)
			scripted(t, &coded{int(code)})
			in, skipped, busy := CallerInARegisteredTurnLenient("")
			if in || len(busy) != 1 || len(skipped) != 0 {
				t.Fatalf("%s (%d): in=%v busy=%v skipped=%v, want busy", name, code, in, busy, skipped)
			}
		})
		t.Run("dead "+name, func(t *testing.T) {
			dir := registryDirOf(t)
			path, release := lockedDB(t, "dead.db")
			release()
			registerRaw(t, dir, canonicalOrSelf(t, path), 1, "no-such-start-stamp")
			setLiveness(t, func(int) string { return "another-stamp" }, func(int) error { return nil })
			scripted(t, &coded{int(code)})
			in, skipped, busy := CallerInARegisteredTurnLenient("")
			if in || len(busy) != 0 || len(skipped) != 0 {
				t.Fatalf("%s: in=%v busy=%v skipped=%v, want skipped silently", name, in, busy, skipped)
			}
		})
	}
	for name, code := range damage {
		t.Run("live damage "+name, func(t *testing.T) {
			registryDirOf(t)
			registerOne(t)
			scripted(t, &coded{int(code)})
			in, skipped, busy := CallerInARegisteredTurnLenient("")
			if in || len(busy) != 0 || len(skipped) != 1 {
				t.Fatalf("%s (%d): in=%v busy=%v skipped=%v, want skipped (damage)", name, code, in, busy, skipped)
			}
		})
	}
}
