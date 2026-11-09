package account_test

import (
	"context"
	"errors"
	"github.com/monoes/mono-agent/internal/testhome"
	"os"
	"path/filepath"
	"reflect"
	"sync/atomic"
	"testing"
	"time"

	"github.com/monoes/mono-agent/internal/account"
	"github.com/monoes/mono-agent/internal/account/accounttest"
)

func TestAGuardReadsNothingUntilAskedAndLooksAtTheFileOncePerPoll(t *testing.T) {
	e := newEnv(t)
	e.signIn(10*time.Minute, time.Hour)
	cs := newCountingStore(account.OpenStore(e.dir, e.seal))
	g := e.guardOver(cs, 0)
	cs.expectReads(t, "a new guard", 0, 0)

	g.Status()
	cs.expectReads(t, "the first Status", 1, 1)
	for range 5 {
		g.Status()
		g.Require(context.Background())
	}
	cs.expectReads(t, "more calls inside the poll", 1, 1)

	e.f.Clock.Advance(account.PollInterval - time.Nanosecond)
	g.Status()
	cs.expectReads(t, "a call a nanosecond before the poll is due", 1, 1)
	e.f.Clock.Advance(time.Nanosecond)
	g.Status()
	cs.expectReads(t, "the first call once the poll is due: the file is stat-ed and, unchanged, not read", 2, 1)
	g.Status()
	cs.expectReads(t, "the next call", 2, 1)

	// The poll counts from the last look at the file, not from the last call: a
	// caller that asks every few seconds must not keep it from ever being due.
	e.save(e.session()) // the same session, in a file with a new modification time
	e.f.Clock.Advance(3 * time.Second)
	g.Status()
	cs.expectReads(t, "3 seconds into the poll", 2, 1)
	e.f.Clock.Advance(3 * time.Second)
	g.Status()
	cs.expectReads(t, "6 seconds after the last look: the poll is due and the file changed", 3, 2)

	// A clock that went back leaves the last look in the future: that is a due
	// poll, or the guard would wait out the difference before it noticed anything.
	e.f.Clock.Advance(-time.Hour)
	g.Status()
	cs.expectReads(t, "a clock set back", 4, 2)

	// A file whose modification time is older than the one the guard holds has
	// changed too: a restored backup, or a write by a process with a slow clock.
	older := mustMtime(t, e.store).Add(-time.Hour)
	if err := os.Chtimes(filepath.Join(e.dir, "session.json"), older, older); err != nil {
		t.Fatal(err)
	}
	e.f.Clock.Advance(account.PollInterval)
	g.Status()
	cs.expectReads(t, "a file with an older modification time", 5, 3)
}

func TestTheFirstStatusReadsOnceWhateverIsStored(t *testing.T) {
	for _, c := range []struct {
		name  string
		setup func(*env)
	}{
		{"nothing stored", func(*env) {}},
		{"a file that is not a session", func(e *env) { e.corrupt() }},
		{"a login", func(e *env) { e.signIn(10*time.Minute, time.Hour) }},
	} {
		t.Run(c.name, func(t *testing.T) {
			e := newEnv(t)
			c.setup(e)
			cs := newCountingStore(account.OpenStore(e.dir, e.seal))
			g := e.guardOver(cs, 0)
			for range 3 {
				g.Status()
			}
			cs.expectReads(t, "three calls at one instant", 1, 1)
		})
	}
}

func TestAGuardHonorsItsPollOption(t *testing.T) {
	for _, c := range []struct {
		name             string
		option, interval time.Duration // what is passed, and the interval the guard must keep
	}{
		{"one second", time.Second, time.Second},
		{"one minute", time.Minute, time.Minute},
		{"none", 0, account.PollInterval},
		{"a negative one", -time.Second, account.PollInterval},
	} {
		t.Run(c.name, func(t *testing.T) {
			e := newEnv(t)
			e.signIn(10*time.Minute, time.Hour)
			cs := newCountingStore(account.OpenStore(e.dir, e.seal))
			g := e.guardOver(cs, c.option)
			g.Status()
			e.f.Clock.Advance(c.interval - time.Nanosecond)
			g.Status()
			cs.expectReads(t, "a nanosecond before the poll is due", 1, 1)
			e.f.Clock.Advance(time.Nanosecond)
			g.Status()
			cs.expectReads(t, "when the poll is due", 2, 1)
		})
	}
}

// The modification time is read before the content, so a write that lands in
// between is read at the next poll. Read after it, the guard would hold the old
// content under the new modification time and never look again.
func TestAWriteDuringTheReadIsReadAtTheNextPoll(t *testing.T) {
	e := newEnv(t)
	e.signIn(10*time.Minute, time.Hour)
	cs := newCountingStore(account.OpenStore(e.dir, e.seal))
	g := e.guardOver(cs, 0)
	cs.afterLoad = func() { e.refuse() }
	if st := g.Status(); st.State != account.StateOK {
		t.Fatalf("Status = %s/%q, want the session the guard read", st.State, st.Reason)
	}
	cs.afterLoad = nil
	e.f.Clock.Advance(account.PollInterval)
	if st := g.Status(); st.Reason != account.ReasonRefused {
		t.Fatalf("Status = %s/%q at the next poll, want locked/refused: the write that landed during the read was lost", st.State, st.Reason)
	}
}

// Two callers that reach the first Status together read the file one after the
// other: a read that finishes late must not be able to overwrite a newer one.
func TestTwoCallersReadTheFileOneAfterTheOther(t *testing.T) {
	e := newEnv(t)
	e.signIn(10*time.Minute, time.Hour)
	cs := newCountingStore(account.OpenStore(e.dir, e.seal))
	g := e.guardOver(cs, 0)
	var reads atomic.Int32
	blocked := make(chan struct{})
	release := make(chan struct{})
	second := make(chan struct{}, 1)
	cs.afterLoad = func() {
		if reads.Add(1) == 1 {
			close(blocked)
			<-release
			return
		}
		second <- struct{}{}
	}
	done := make(chan struct{}, 2)
	go func() {
		g.Status()
		done <- struct{}{}
	}()
	select {
	case <-blocked:
	case <-time.After(testWait):
		t.Fatal("the first Status never read the file")
	}
	go func() {
		g.Status()
		done <- struct{}{}
	}()
	// The second caller has to wait for the first.
	select {
	case <-second:
		t.Fatal("a second read of the file started while the first was still in progress")
	case <-time.After(quietFor):
	}
	close(release)
	for range 2 {
		select {
		case <-done:
		case <-time.After(testWait):
			t.Fatal("a Status call did not return once the read had finished")
		}
	}
	if st := g.Status(); st.State != account.StateOK {
		t.Fatalf("Status = %s/%q", st.State, st.Reason)
	}
}

func TestStatusAndRequireNeitherCallMonoesMeNorWrite(t *testing.T) {
	e := newEnv(t)
	e.signIn(2*time.Hour, time.Hour) // expired an hour ago: a refresh is due, and the guard has a refresher
	cs := newCountingStore(account.OpenStore(e.dir, e.seal))
	g := e.guardOver(cs, 0)
	g.OnRefused(func(account.Status) {})
	before, mtime := snapshot(t, e.dir), mustMtime(t, e.store)
	ctx := context.Background()
	for range 4 {
		g.Status()
		g.Require(ctx)
		e.f.Clock.Advance(account.PollInterval)
	}
	if after, mt := snapshot(t, e.dir), mustMtime(t, e.store); !reflect.DeepEqual(after, before) || !mt.Equal(mtime) {
		t.Fatal("reading the status changed a file of the session directory")
	}

	// Another process ends the login, removes it, and signs in again: the guard
	// reads each of them and still only reads.
	e.refuse()
	e.f.Clock.Advance(account.PollInterval)
	g.Status()
	if err := os.Remove(filepath.Join(e.dir, "session.json")); err != nil {
		t.Fatal(err)
	}
	e.f.Clock.Advance(account.PollInterval)
	g.Require(ctx)
	e.signIn(time.Minute, time.Hour)
	e.f.Clock.Advance(account.PollInterval)
	g.Require(ctx)

	if n := e.ref.calls.Load(); n != 0 {
		t.Fatalf("monoes.me was called %d times by Status and Require", n)
	}
	for _, name := range []string{"Save", "SaveRefresh", "DeleteRefresh", "Lock", "LoadRefresh"} {
		if n := cs.calls(name); n != 0 {
			t.Fatalf("Status and Require called Store.%s %d times", name, n)
		}
	}
	for _, name := range names(t, e.dir) {
		if name == "session.lock" {
			t.Fatal("Status and Require took the session lock")
		}
	}
}

// Nothing in the guard checks a signature at Status: the token is verified when
// the session is loaded. The pinned keys change under it here, which a release
// binary cannot do, to show whether a signature is checked again.
func TestTheTokenIsVerifiedWhenTheSessionIsLoadedNotAtEveryStatus(t *testing.T) {
	e := newEnv(t)
	e.signIn(10*time.Minute, time.Hour)
	if st := e.g.Status(); st.State != account.StateOK {
		t.Fatalf("Status = %s/%q", st.State, st.Reason)
	}
	account.SetTrustedKeysForTest(t, nil)
	for range 3 {
		e.f.Clock.Advance(account.PollInterval)
		if st := e.g.Status(); st.State != account.StateOK {
			t.Fatalf("Status = %s/%q: the token was verified again at Status, against keys that no longer trust it", st.State, st.Reason)
		}
	}
	e.touch() // the same session, in a file that changed: it is read and verified again
	e.f.Clock.Advance(account.PollInterval)
	if st := e.g.Status(); st.State != account.StateLocked || st.Reason != account.ReasonKeyUnknown {
		t.Fatalf("Status = %s/%q after the file changed, want locked/key_unknown: the key is no longer pinned", st.State, st.Reason)
	}
}

// A read that fails on the disk (permission denied, an I/O error) says nothing about
// what session.json holds: the guard keeps the session that worked. (A file whose
// content cannot be used is another matter: guard_invalid_test.go.)
func TestAReadThatFailsOnTheDiskNeverChangesAWorkingVerdict(t *testing.T) {
	e := newEnv(t)
	e.signIn(10*time.Minute, time.Hour)
	cs := newCountingStore(account.OpenStore(e.dir, e.seal))
	g := e.guardOver(cs, 0)
	before := g.Status()
	cs.failLoad(errors.New("simulated: input/output error"))
	e.touch() // the file changed: the next poll reads it, and the read fails
	e.f.Clock.Advance(account.PollInterval)
	if after := g.Status(); !reflect.DeepEqual(after, before) {
		t.Fatalf("a file that cannot be read changed the verdict: %s, was %s", describeStatus(after), describeStatus(before))
	}
	if n := cs.calls("Load"); n != 2 {
		t.Fatalf("%d reads, want the first one and the one that failed", n)
	}
	cs.failLoad(nil)
	sess := e.signIn(time.Minute, time.Hour)
	e.f.Clock.Advance(account.PollInterval)
	if st := g.Status(); st.State != account.StateOK || !st.IssuedAt.Equal(sess.HW) {
		t.Fatalf("after signing in once the disk reads again: %s, want ok and the new session", describeStatus(st))
	}
}

// A guard that could not read the file tries again at every poll, whatever the
// file's modification time says: a repair can land within the same tick of a
// coarse file-system clock as the damage did. (What the guard keeps from a bad
// read is the error, until a read succeeds.)
func TestAFailedReadIsRetriedAtEveryPollWhateverTheModificationTime(t *testing.T) {
	e := newEnv(t)
	mtime := e.corrupt()
	cs := newCountingStore(account.OpenStore(e.dir, e.seal))
	g := e.guardOver(cs, 0)
	for range 3 {
		if st := g.Status(); st.State != account.StateLocked || st.Reason != account.ReasonInvalid {
			t.Fatalf("Status = %s/%q, want locked/invalid", st.State, st.Reason)
		}
	}
	cs.expectReads(t, "three calls at one instant", 1, 1)
	e.f.Clock.Advance(account.PollInterval)
	g.Status()
	cs.expectReads(t, "the next poll, the file unchanged", 2, 2)

	// The repair keeps the modification time of the damage.
	sess := e.signIn(10*time.Minute, time.Hour)
	if err := os.Chtimes(filepath.Join(e.dir, "session.json"), mtime, mtime); err != nil {
		t.Fatal(err)
	}
	e.f.Clock.Advance(account.PollInterval)
	if st := g.Status(); st.State != account.StateOK || !st.IssuedAt.Equal(sess.HW) {
		t.Fatalf("Status = %s after the repair, want ok and the repaired session", describeStatus(st))
	}
}

// A guard whose first read failed has nothing cached, and a file that is then
// removed has the very modification time the guard holds for it (none). The
// guard must still look, or it would report an unreadable login for good, for
// a file that no longer exists.
func TestAFailedFirstReadDoesNotStickOnceTheFileIsRemoved(t *testing.T) {
	e := newEnv(t)
	e.corrupt()
	if st := e.g.Status(); st.State != account.StateLocked || st.Reason != account.ReasonInvalid {
		t.Fatalf("Status = %s, want locked/invalid", describeStatus(st))
	}
	if err := os.Remove(filepath.Join(e.dir, "session.json")); err != nil {
		t.Fatal(err)
	}
	for poll := 1; poll <= 3; poll++ {
		e.f.Clock.Advance(account.PollInterval)
		if st := e.g.Status(); st.State != account.StateLocked || st.Reason != account.ReasonNotLoggedIn {
			t.Fatalf("poll %d after the file was removed: Status = %s, want locked/not_logged_in", poll, describeStatus(st))
		}
	}
	e.signIn(10*time.Minute, time.Hour)
	e.f.Clock.Advance(account.PollInterval)
	if st := e.g.Status(); st.State != account.StateOK {
		t.Fatalf("after a sign-in: Status = %s, want ok", describeStatus(st))
	}
}

// A guard that cannot tell whether the file changed reads it.
func TestAFailedStatReadsTheFileInstead(t *testing.T) {
	e := newEnv(t)
	cs := newCountingStore(account.OpenStore(e.dir, e.seal))
	g := e.guardOver(cs, 0)
	if st := g.Status(); st.Reason != account.ReasonNotLoggedIn {
		t.Fatalf("Status = %s/%q", st.State, st.Reason)
	}
	e.signIn(10*time.Minute, time.Hour)
	cs.mtimeErr = errors.New("stat failed")
	e.f.Clock.Advance(account.PollInterval)
	if st := g.Status(); st.State != account.StateOK {
		t.Fatalf("Status = %s/%q, want the login the guard found by reading the file", st.State, st.Reason)
	}
}

// A good read ends the memory of a bad one: after the login is removed the verdict is
// "not logged in", not the error of a read that no longer applies.
func TestAReadThatSucceedsForgetsEarlierFailures(t *testing.T) {
	e := newEnv(t)
	e.corrupt()
	if st := e.g.Status(); st.Reason != account.ReasonInvalid {
		t.Fatalf("Status = %s/%q", st.State, st.Reason)
	}
	e.signIn(10*time.Minute, time.Hour)
	e.f.Clock.Advance(account.PollInterval)
	if st := e.g.Status(); st.State != account.StateOK {
		t.Fatalf("Status = %s/%q", st.State, st.Reason)
	}
	if err := os.Remove(filepath.Join(e.dir, "session.json")); err != nil {
		t.Fatal(err)
	}
	e.f.Clock.Advance(account.PollInterval)
	if st := e.g.Status(); st.State != account.StateLocked || st.Reason != account.ReasonNotLoggedIn {
		t.Fatalf("Status = %s/%q after the logout, want locked/not_logged_in", st.State, st.Reason)
	}
}

// With no store given the guard reads the session in the home directory, and an
// empty home stays empty.
func TestAGuardWithNoStoreReadsTheSessionInTheHomeDirectory(t *testing.T) {
	f := accounttest.New(t)
	home := t.TempDir()
	testhome.SetHome(t, home)
	g := account.NewGuard(account.GuardOptions{Now: f.Clock.Now})
	t.Cleanup(g.Close)
	if st := g.Status(); st.State != account.StateLocked || st.Reason != account.ReasonNotLoggedIn {
		t.Fatalf("Status = %s/%q on an empty home", st.State, st.Reason)
	}
	if got := names(t, home); len(got) != 0 {
		t.Fatalf("Status created %v in an empty home", got)
	}

	dir, err := account.DefaultDir()
	if err != nil {
		t.Fatal(err)
	}
	sess, err := account.NewSession(account.HostURL, f.Token(accounttest.TokenOptions{}), nil, f.Clock.Now())
	if err != nil {
		t.Fatal(err)
	}
	if err := account.OpenStore(dir, account.NewMemorySealer()).Save(sess); err != nil {
		t.Fatal(err)
	}
	f.Clock.Advance(account.PollInterval)
	if st := g.Status(); st.State != account.StateOK {
		t.Fatalf("Status = %s/%q after a sign-in under the home directory", st.State, st.Reason)
	}
}

func TestAClosedGuardStillAnswersAndCloseIsIdempotent(t *testing.T) {
	e := newEnv(t)
	e.signIn(10*time.Minute, time.Hour)
	cs := newCountingStore(account.OpenStore(e.dir, e.seal))
	g := e.guardOver(cs, 0)
	before := g.Status()
	g.Close()
	g.Close()
	if after := g.Status(); !reflect.DeepEqual(after, before) {
		t.Fatalf("Status = %s after Close, was %s", describeStatus(after), describeStatus(before))
	}
	cs.expectReads(t, "Status after Close, inside the poll", 1, 1)
	e.refuse()
	e.f.Clock.Advance(account.PollInterval)
	if err := g.Require(context.Background()); !account.IsLoginRequired(err) {
		t.Fatalf("Require = %v after Close and a refusal in the file, want a LoginRequiredError: a closed guard still follows the file", err)
	}
}
