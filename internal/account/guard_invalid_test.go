package account_test

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"reflect"
	"testing"
	"time"

	"github.com/monoes/mono-agent/internal/account"
)

// A session.json that is there and cannot be used (it does not parse, it is of a version
// this build does not read, it is not a regular file, it is larger than the store reads)
// is what every process reads, and every new process judges it locked(invalid). A guard
// that has run for a while judges it the same at its next poll: it drops the session it had
// cached. Holding on to it would keep a daemon allowed until the grace ends while every new
// process is locked, and a refusal that another process stored and that was then written
// over would be lost to it. A read that fails on the disk (permission denied, an I/O error)
// says nothing about what the file holds, and the guard keeps what it has
// (TestAReadThatFailsOnTheDiskNeverChangesAWorkingVerdict).

// unusable are the ways session.json can be there and unusable.
var unusable = []struct {
	name  string
	spoil func(t *testing.T, path string)
}{
	{"text that is not JSON", func(t *testing.T, p string) { overwrite(t, p, []byte("{")) }},
	{"a version this build does not read", func(t *testing.T, p string) {
		overwrite(t, p, []byte(`{"v":2,"host":"https://monoes.me","access_token":"x"}`))
	}},
	{"a file larger than the store reads", func(t *testing.T, p string) {
		overwrite(t, p, append([]byte(`{"v":1}`), bytes.Repeat([]byte(" "), 64<<10)...))
	}},
	{"a directory", func(t *testing.T, p string) {
		if err := os.Remove(p); err != nil {
			t.Fatal(err)
		}
		if err := os.Mkdir(p, 0o700); err != nil {
			t.Fatal(err)
		}
	}},
}

func overwrite(t *testing.T, path string, data []byte) {
	t.Helper()
	if err := os.WriteFile(path, data, 0o600); err != nil {
		t.Fatal(err)
	}
}

// spoil makes session.json unusable in the way u says, with a modification time no earlier
// write had, and returns that time.
func (e *env) spoil(how func(*testing.T, string)) time.Time {
	e.t.Helper()
	how(e.t, filepath.Join(e.dir, "session.json"))
	e.touch()
	return mustMtime(e.t, e.store)
}

// The finding of the security review: a daemon holds a good session; another process is
// refused and stores the refusal; the file is then written over and cannot be used. At its
// next poll the daemon is locked(invalid) as every new process is, and allows nothing.
func TestAnUnusableSessionFileLocksALongRunningGuardAtItsNextPoll(t *testing.T) {
	for _, u := range unusable {
		t.Run(u.name, func(t *testing.T) {
			e := newEnv(t)
			e.signIn(10*time.Minute, time.Hour)
			daemon := e.g
			if st := daemon.Status(); st.State != account.StateOK {
				t.Fatalf("Status = %s, want ok", describeStatus(st))
			}
			e.refuse()       // another process is refused and stores it...
			e.spoil(u.spoil) // ...and the file is written over before the daemon looks
			e.f.Clock.Advance(account.PollInterval)
			st := daemon.Status()
			if st.State != account.StateLocked || st.Reason != account.ReasonInvalid || st.Allowed() {
				t.Fatalf("the daemon's Status = %s, want locked/invalid and nothing allowed, as a new process judges the file", describeStatus(st))
			}
			if err := daemon.Require(context.Background()); !account.IsLoginRequired(err) {
				t.Fatalf("the daemon's Require = %v, want a login required error", err)
			}
			if fresh := e.newGuard(0).Status(); !reflect.DeepEqual(fresh, st) {
				t.Fatalf("a new process says %s, the daemon %s: they read the same file", describeStatus(fresh), describeStatus(st))
			}
		})
	}
}

// The file is repaired (a sign-in writes a session over it), in the same tick of a coarse
// file clock as the damage: the guard reads it at its next poll and is ok again.
func TestAnUnusableSessionFileThatIsRepairedIsTakenInAtTheNextPoll(t *testing.T) {
	e := newEnv(t)
	e.signIn(10*time.Minute, time.Hour)
	if st := e.g.Status(); st.State != account.StateOK {
		t.Fatalf("Status = %s, want ok", describeStatus(st))
	}
	damaged := e.corrupt()
	e.f.Clock.Advance(account.PollInterval)
	if st := e.g.Status(); st.State != account.StateLocked || st.Reason != account.ReasonInvalid {
		t.Fatalf("Status = %s, want locked/invalid", describeStatus(st))
	}
	sess := e.signIn(5*time.Minute, time.Hour)
	if err := os.Chtimes(filepath.Join(e.dir, "session.json"), damaged, damaged); err != nil {
		t.Fatal(err)
	}
	e.f.Clock.Advance(account.PollInterval)
	if st := e.g.Status(); st.State != account.StateOK || !st.IssuedAt.Equal(sess.HW) {
		t.Fatalf("Status after the repair = %s, want ok and the repaired session", describeStatus(st))
	}
}

// A guard whose cached session was dropped for an unusable file is a guard with no session,
// and from the enforcement date on such a guard keeps the clock-guard record (A25). It must
// not write it over the unusable file: no writer of the guard writes after a failed read,
// and only a sign-in (or B1b's logout, which replaces an unreadable file with a token-less
// record) replaces it. Nor does a refresh, which needs a session it can read.
func TestALongRunningGuardWritesNothingOverAnUnusableSessionFile(t *testing.T) {
	for _, ep := range entryPoints {
		t.Run(ep.name, func(t *testing.T) {
			e := newEnv(t)
			account.SetEnforceFromForTest(t, e.f.Clock.Now().Add(-time.Hour))
			e.signIn(2*time.Hour, time.Hour) // in grace: due, so the refresh would run if it could
			if st := e.g.Status(); st.State != account.StateGrace {
				t.Fatalf("Status = %s, want grace", describeStatus(st))
			}
			damaged := e.corrupt()
			e.f.Clock.Advance(account.PollInterval)
			if st := e.g.Status(); st.Reason != account.ReasonInvalid {
				t.Fatalf("Status = %s, want locked/invalid: the cached session is dropped", describeStatus(st))
			}
			for i := 0; i < 3; i++ {
				e.f.Clock.Advance(2 * time.Minute) // past the mark's minute and the negative cache each time
				if _, err := ep.call(e.g, context.Background()); err != nil {
					t.Fatalf("pass %d: %v, want no error: a mark that cannot be written is not reported", i+1, err)
				}
				if got := mustMtime(t, e.store); !got.Equal(damaged) {
					t.Fatalf("pass %d wrote over the unusable session.json", i+1)
				}
			}
			if data, err := readReplaced(filepath.Join(e.dir, "session.json")); err != nil || string(data) != "{not json" {
				t.Fatalf("session.json = %q (%v), want the unusable text left as it was", data, err)
			}
			if n := e.ref.calls.Load(); n != 0 {
				t.Fatalf("%d network refreshes, want none", n)
			}
		})
	}
}

// The same for the refresher: its passes neither refresh nor keep the record over the file.
func TestARefresherWritesNothingOverAnUnusableSessionFile(t *testing.T) {
	e := newEnv(t)
	account.SetEnforceFromForTest(t, e.f.Clock.Now().Add(-time.Hour))
	e.signIn(2*time.Hour, time.Hour) // in grace: every pass is due while the session is cached
	g := e.guardOn(e.ref, loopPoll)
	if st := g.Status(); st.State != account.StateGrace {
		t.Fatalf("Status = %s, want grace", describeStatus(st))
	}
	var damaged time.Time
	e.underLock(func() { damaged = e.corrupt() })
	e.f.Clock.Advance(account.PollInterval)
	if st := g.Status(); st.Reason != account.ReasonInvalid {
		t.Fatalf("Status = %s, want locked/invalid", describeStatus(st))
	}
	g.StartRefresher(context.Background())
	for i := 0; i < 3; i++ {
		e.f.Clock.Advance(2 * time.Minute)
		quiet(t, e.loops...)
		if got := mustMtime(t, e.store); !got.Equal(damaged) {
			t.Fatalf("the refresher wrote over the unusable session.json at its pass %d", i+1)
		}
	}
	if n := e.ref.calls.Load(); n != 0 {
		t.Fatalf("%d network refreshes, want none", n)
	}
}
