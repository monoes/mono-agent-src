package account_test

import (
	"context"
	"encoding/json"
	"errors"
	"github.com/monoes/mono-agent/internal/testhome"
	"os"
	"path/filepath"
	"reflect"
	"testing"
	"time"

	"github.com/monoes/mono-agent/internal/account"
)

// A machine that never signed in keeps the clock-guard record too, from the enforcement date on
// (A25). The date is judged on max(now, hw), and hw lives only in session.json: with no
// session.json a clock set back to before the date would un-enforce the gate for a machine that
// was refused, or that never signed in. So the first guard pass on or after the date (EnsureFresh,
// Refresh or the refresher) saves a session with no token, {v, host, hw}, the record that a
// logout leaves. Nothing is written while the package is dormant or before the date.

// Every test below states its date: none relies on the fixture's.

// theRecord is what the guard writes for a machine that has no session, spelled as JSON.
func theRecord(t *testing.T, e *env, hw time.Time) {
	t.Helper()
	data, err := readReplaced(filepath.Join(e.dir, "session.json"))
	if err != nil {
		t.Fatal(err)
	}
	var keys map[string]any
	if err := json.Unmarshal(data, &keys); err != nil {
		t.Fatal(err)
	}
	want := map[string]any{"v": float64(1), "host": account.HostURL, "access_token": "", "hw": hw.UTC().Format(time.RFC3339Nano)}
	if !reflect.DeepEqual(keys, want) {
		t.Fatalf("session.json = %v, want exactly the token-less record %v: a host and a mark, nothing else", keys, want)
	}
}

// dirNames lists the files of a directory.
func dirNames(t *testing.T, dir string) []string {
	t.Helper()
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	var names []string
	for _, e := range entries {
		names = append(names, e.Name())
	}
	return names
}

// A machine with no session, once the date has been reached: the first pass writes the record,
// exactly one file besides the lock, and Evaluate, Status and Require judge it from its mark.
func TestAMachineWithNoSessionKeepsTheClockGuardRecordOnceTheDateIsReached(t *testing.T) {
	for _, ep := range entryPoints {
		t.Run(ep.name, func(t *testing.T) {
			e := newEnv(t)
			date := e.f.Clock.Now().Add(-time.Hour)
			account.SetEnforceFromForTest(t, date)
			now := e.f.Clock.Now()
			st, err := ep.call(e.g, context.Background())
			if err != nil || st.State != account.StateLocked || st.Reason != account.ReasonNotLoggedIn || !st.Enforced || e.ref.calls.Load() != 0 {
				t.Fatalf("%s = %s, %v with %d network refreshes, want locked/not_logged_in, enforced and no call", ep.name, describeStatus(st), err, e.ref.calls.Load())
			}
			theRecord(t, e, now)
			if got, want := dirNames(t, e.dir), []string{"session.json", "session.lock"}; !reflect.DeepEqual(got, want) {
				t.Fatalf("the directory holds %v, want %v: the record, the lock and never a refresh token", got, want)
			}
			sess := e.session()
			if st := account.Evaluate(sess, now); st.State != account.StateLocked || st.Reason != account.ReasonNotLoggedIn || !st.Enforced {
				t.Fatalf("Evaluate of the record = %s, want locked/not_logged_in, enforced", describeStatus(st))
			}
			// A clock set back to before the date is still refused: the record holds the mark.
			e.f.Clock.Set(date.Add(-30 * 24 * time.Hour))
			e.f.Clock.Advance(account.PollInterval)
			g := e.newGuard(0)
			if st := g.Status(); st.State != account.StateLocked || st.Reason != account.ReasonNotLoggedIn || !st.Enforced || st.Allowed() {
				t.Fatalf("Status with the clock set back a month before the date = %s, want locked/not_logged_in, enforced and not allowed", describeStatus(st))
			}
			if err := g.Require(context.Background()); !account.IsLoginRequired(err) {
				t.Fatalf("Require with the clock set back = %v, want a login required error", err)
			}
			if st := account.Evaluate(sess, e.f.Clock.Now()); !st.Enforced || st.Allowed() {
				t.Fatalf("Evaluate with the clock set back = %s, want enforced and not allowed", describeStatus(st))
			}
		})
	}
}

// Without the record the same clock change un-enforces the gate: the accepted case of the spec, a
// clock set back before the first check, which is the first pass of a guard on or after the date.
func TestWithoutTheRecordAClockSetBackBeforeTheDateUnenforcesTheGate(t *testing.T) {
	e := newEnv(t)
	date := e.f.Clock.Now().Add(-time.Hour)
	account.SetEnforceFromForTest(t, date)
	e.f.Clock.Set(date.Add(-time.Hour)) // before the first pass: nothing to remember
	if _, err := e.g.EnsureFresh(context.Background()); err != nil {
		t.Fatal(err)
	}
	if st := e.g.Status(); st.Enforced || !st.Allowed() {
		t.Fatalf("Status = %s, want not enforced: the clock was set back before the first pass", describeStatus(st))
	}
	if _, err := os.Stat(e.dir); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("a pass before the date created %s (stat err %v)", e.dir, err)
	}
}

// Nothing is written before the date or while dormant, not even the directory, and the lock is not taken.
func TestNoRecordIsWrittenBeforeTheDateOrWhileDormant(t *testing.T) {
	for _, ep := range entryPoints {
		for _, c := range []struct {
			name string
			date func(now time.Time) time.Time
		}{
			{"the date is an hour ahead", func(now time.Time) time.Time { return now.Add(time.Hour) }},
			{"the date is a nanosecond ahead", func(now time.Time) time.Time { return now.Add(time.Nanosecond) }},
			{"the package is dormant", func(time.Time) time.Time { return time.Time{} }},
		} {
			t.Run(ep.name+"/"+c.name, func(t *testing.T) {
				e := newEnv(t)
				account.SetEnforceFromForTest(t, c.date(e.f.Clock.Now()))
				spy := e.spy()
				g := e.guardOver(spy, 0)
				if _, err := ep.call(g, context.Background()); err != nil {
					t.Fatal(err)
				}
				if lock, save := spy.calls("Lock"), spy.calls("Save"); lock != 0 || save != 0 {
					t.Fatalf("%d locks and %d writes before the date, want none", lock, save)
				}
				if _, err := os.Stat(e.dir); !errors.Is(err, os.ErrNotExist) {
					t.Fatalf("a pass created %s (stat err %v)", e.dir, err)
				}
			})
		}
	}
}

// The date itself counts: the record is written at the instant the date is reached.
func TestTheRecordIsWrittenAtTheInstantTheDateIsReached(t *testing.T) {
	e := newEnv(t)
	account.SetEnforceFromForTest(t, e.f.Clock.Now())
	if _, err := e.g.EnsureFresh(context.Background()); err != nil {
		t.Fatal(err)
	}
	theRecord(t, e, e.f.Clock.Now())
}

// The record is one attempt per minute per guard, whether or not it could be written, and a failure
// is not reported.
func TestARecordThatCannotBeWrittenIsNotReportedAndIsTriedAgainAMinuteLater(t *testing.T) {
	e := newEnv(t)
	account.SetEnforceFromForTest(t, e.f.Clock.Now().Add(-time.Hour))
	fs := &failingSaveStore{Store: account.OpenStore(e.dir, e.seal), fail: true}
	g := e.guardOver(fs, 0)
	call := func(when string, want int) {
		t.Helper()
		if _, err := g.EnsureFresh(context.Background()); err != nil {
			t.Fatalf("%s: %v: a record that could not be written is not reported", when, err)
		}
		if n := fs.attempts(); n != want {
			t.Fatalf("%s: %d writes of the record so far, want %d", when, n, want)
		}
	}
	call("the first call", 1)
	call("straight after it", 1)
	e.f.Clock.Advance(59 * time.Second)
	call("59 seconds after it", 1)
	e.f.Clock.Advance(time.Second)
	call("a minute to the second after it", 2)
	fs.mu.Lock()
	fs.fail = false
	fs.mu.Unlock()
	e.f.Clock.Advance(time.Minute)
	call("a minute later, with a disk that works", 3)
	theRecord(t, e, e.f.Clock.Now())
	// Written: the next pass has a session, and writes no more often than the mark does.
	e.f.Clock.Advance(30 * time.Second)
	call("30 seconds after the record", 3)
}

// A session that appeared between the guard's cached nothing and its re-read under the lock
// is not overwritten: the record is for a machine that has no session at that moment.
func TestARecordNeverOverwritesASessionThatAppearedUnderTheLock(t *testing.T) {
	e := newEnv(t)
	account.SetEnforceFromForTest(t, e.f.Clock.Now().Add(-time.Hour))
	spy := e.spy()
	spy.afterLock = func() { e.signIn(10*time.Minute, time.Hour) } // another process signed in while this one waited for the lock
	g := e.guardOver(spy, 0)
	if st := g.Status(); st.Reason != account.ReasonNotLoggedIn {
		t.Fatalf("Status = %s, want nothing cached: the test cannot tell otherwise", describeStatus(st))
	}
	st, err := g.EnsureFresh(context.Background())
	if err != nil || st.State != account.StateOK {
		t.Fatalf("EnsureFresh = %s, %v, want ok: the session that appeared is taken in", describeStatus(st), err)
	}
	if sess := e.session(); sess.AccessToken == "" || sess.User == nil || sess.User.ID != "user-1" || sess.LastResult != "ok" {
		t.Fatalf("stored session = %s, want the login that appeared, not the record", describe(sess))
	}
}

// A file that cannot be read is not a machine with no session: nothing replaces it.
func TestARecordNeverReplacesAFileThatCannotBeRead(t *testing.T) {
	e := newEnv(t)
	account.SetEnforceFromForTest(t, e.f.Clock.Now().Add(-time.Hour))
	damaged := e.corrupt()
	if _, err := e.g.EnsureFresh(context.Background()); err != nil {
		t.Fatal(err)
	}
	if !mustMtime(t, e.store).Equal(damaged) {
		t.Fatal("the guard wrote over a session.json it could not read")
	}
}

// A refused session, and a machine whose session vanished under the guard, keep what they do: the
// record is for a machine whose cached session is none, not for one that lost its file.
func TestARecordIsWrittenOnlyWhenTheCachedSessionIsNone(t *testing.T) {
	e := newEnv(t)
	account.SetEnforceFromForTest(t, e.f.Clock.Now().Add(-time.Hour))
	e.signIn(10*time.Minute, time.Hour)
	if st := e.g.Status(); st.State != account.StateOK {
		t.Fatalf("Status = %s, want ok", describeStatus(st))
	}
	if err := os.Remove(filepath.Join(e.dir, "session.json")); err != nil { // the file vanishes under a cached session
		t.Fatal(err)
	}
	if st, err := e.g.EnsureFresh(context.Background()); err != nil || st.State != account.StateOK {
		t.Fatalf("EnsureFresh = %s, %v, want ok: the cached session still stands", describeStatus(st), err)
	}
	if _, err := os.Stat(filepath.Join(e.dir, "session.json")); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("a pass brought session.json back (stat err %v)", err)
	}
}

// A login replaces the record: a full session is saved over it, whatever mark the record held, and the
// verdict is ok again. (NewSession starts the mark at the token's iat, by design.)
func TestALoginReplacesTheRecord(t *testing.T) {
	e := newEnv(t)
	account.SetEnforceFromForTest(t, e.f.Clock.Now().Add(-time.Hour))
	e.f.Clock.Advance(3 * time.Hour)
	if _, err := e.g.EnsureFresh(context.Background()); err != nil {
		t.Fatal(err)
	}
	theRecord(t, e, e.f.Clock.Now())
	e.signIn(10*time.Minute, time.Hour) // a login stores a full session over the record
	e.f.Clock.Advance(account.PollInterval)
	if st := e.g.Status(); st.State != account.StateOK {
		t.Fatalf("Status after the login = %s, want ok", describeStatus(st))
	}
	if sess := e.session(); sess.AccessToken == "" || sess.LastResult != "ok" {
		t.Fatalf("stored session = %s, want the login's", describe(sess))
	}
}

// The background refresher is a guard pass too: on a machine with no session, once the date has been
// reached, it writes the record, once.
func TestTheRefresherKeepsTheClockGuardRecordOfAMachineWithNoSession(t *testing.T) {
	e := newEnv(t)
	account.SetEnforceFromForTest(t, e.f.Clock.Now().Add(-time.Hour))
	now := e.f.Clock.Now()
	g := e.guardOn(e.ref, loopPoll)
	g.StartRefresher(context.Background())
	waitForHW(t, e, now, "the record")
	theRecord(t, e, now)
	quiet()
	if got := dirNames(t, e.dir); !reflect.DeepEqual(got, []string{"session.json", "session.lock"}) {
		t.Fatalf("the directory holds %v, want the record and the lock", got)
	}
	if n := e.ref.calls.Load(); n != 0 {
		t.Fatalf("%d network refreshes for a machine with no session", n)
	}
}

// A gated command refused on an empty HOME leaves exactly account/session.lock and
// account/session.json from the enforcement date on: the first guard pass of a machine that
// never signed in. Reads, and a command that makes no guard pass (doctor), still leave it empty:
// TestNothingIsCreatedOnAnEmptyHome in process_test.go.
func TestAGatedPassOnAnEmptyHomeFromTheDateOnLeavesExactlyTheRecord(t *testing.T) {
	home := t.TempDir()
	testhome.SetHome(t, home)
	e := newEnv(t)
	account.SetEnforceFromForTest(t, e.f.Clock.Now().Add(-time.Hour))
	ctx := context.Background()

	g := account.NewGuard(account.GuardOptions{Refresher: e.ref, Now: e.f.Clock.Now}) // the default store, under HOME
	t.Cleanup(g.Close)
	_ = g.Status()
	_ = g.Require(ctx)
	if entries, _ := os.ReadDir(home); len(entries) != 0 {
		t.Fatalf("reading the verdict created %v on an empty HOME", entries)
	}
	if st, err := g.EnsureFresh(ctx); err != nil || st.Reason != account.ReasonNotLoggedIn || !st.Enforced {
		t.Fatalf("EnsureFresh = %s, %v, want locked/not_logged_in, enforced", describeStatus(st), err)
	}
	var files []string
	err := filepath.WalkDir(home, func(path string, d os.DirEntry, err error) error {
		if err == nil && !d.IsDir() {
			rel, _ := filepath.Rel(home, path)
			files = append(files, filepath.ToSlash(rel))
		}
		return err
	})
	if err != nil {
		t.Fatal(err)
	}
	if want := []string{".monoagent/account/session.json", ".monoagent/account/session.lock"}; !reflect.DeepEqual(files, want) {
		t.Fatalf("HOME holds %v, want %v and nothing else", files, want)
	}
	if n := e.ref.calls.Load(); n != 0 {
		t.Fatalf("%d network refreshes with no session", n)
	}
}

// The guard knows the record it has just written: a poll that finds the file unchanged reads
// nothing, so the record does not cost a read five seconds later.
func TestTheGuardDoesNotReReadTheRecordItJustWrote(t *testing.T) {
	e := newEnv(t)
	account.SetEnforceFromForTest(t, e.f.Clock.Now().Add(-time.Hour))
	cs := newCountingStore(account.OpenStore(e.dir, e.seal))
	g := e.guardOver(cs, 0)
	if _, err := g.EnsureFresh(context.Background()); err != nil {
		t.Fatal(err)
	}
	theRecord(t, e, e.f.Clock.Now())
	reads := cs.calls("Load")
	e.f.Clock.Advance(account.PollInterval)
	if st := g.Status(); st.Reason != account.ReasonNotLoggedIn || !st.Enforced {
		t.Fatalf("Status = %s, want locked/not_logged_in, enforced", describeStatus(st))
	}
	if n := cs.calls("Load"); n != reads {
		t.Fatalf("%d reads after the poll, %d before: the guard read the file it had just written", n, reads)
	}
}
