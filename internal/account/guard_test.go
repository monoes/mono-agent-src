package account_test

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/monoes/mono-agent/internal/account"
	"github.com/monoes/mono-agent/internal/account/accounttest"
)

func TestStatusFollowsTheClock(t *testing.T) {
	e := newEnv(t)
	e.signIn(10*time.Minute, time.Hour) // iat -10m, exp +50m, grace until +23h50m
	start := e.f.Clock.Now()
	steps := []struct {
		at     time.Duration
		state  account.State
		reason account.Reason
	}{
		{0, account.StateOK, ""},
		{49*time.Minute + 59*time.Second, account.StateOK, ""},
		{50 * time.Minute, account.StateGrace, account.ReasonUnreachable},
		{23*time.Hour + 49*time.Minute + 59*time.Second, account.StateGrace, account.ReasonUnreachable},
		{23*time.Hour + 50*time.Minute, account.StateLocked, account.ReasonExpired},
	}
	for _, s := range steps {
		e.f.Clock.Set(start.Add(s.at))
		if st := e.g.Status(); st.State != s.state || st.Reason != s.reason {
			t.Fatalf("at +%v: Status = %s/%q, want %s/%q", s.at, st.State, st.Reason, s.state, s.reason)
		}
	}
}

func TestStatusReReadsTheFileOnlyWhenThePollIsDue(t *testing.T) {
	e := newEnv(t)
	e.signIn(10*time.Minute, time.Hour)
	if st := e.g.Status(); st.State != account.StateOK {
		t.Fatalf("Status = %s", st.State)
	}
	// Another process ends the login.
	e.save(&account.Session{V: 1, Host: account.HostURL, User: &account.User{ID: "user-1"}, State: "refused"})
	if st := e.g.Status(); st.State != account.StateOK {
		t.Fatalf("Status = %s before the poll is due: the cached session must answer", st.State)
	}
	e.f.Clock.Advance(account.PollInterval)
	if st := e.g.Status(); st.Reason != account.ReasonRefused {
		t.Fatalf("Status = %s/%q after the poll, want locked/refused", st.State, st.Reason)
	}
}

func TestRequire(t *testing.T) {
	e := newEnv(t)
	ctx := context.Background()
	e.signIn(10*time.Minute, time.Hour)
	if err := e.g.Require(ctx); err != nil {
		t.Fatalf("Require while ok: %v", err)
	}
	e.f.Clock.Advance(30 * time.Hour)
	err := e.g.Require(ctx)
	var lr *account.LoginRequiredError
	if !errors.As(err, &lr) || lr.Status.State != account.StateLocked || lr.Status.Reason != account.ReasonExpired || !account.IsLoginRequired(err) {
		t.Fatalf("Require after 30 hours = %v, want a LoginRequiredError with locked/expired", err)
	}
	account.SetEnforceFromForTest(t, time.Time{})
	if err := e.g.Require(ctx); err != nil {
		t.Fatalf("Require while dormant must allow whatever the state: %v", err)
	}
	if st := e.g.Status(); st.State != account.StateLocked || st.Enforced {
		t.Fatalf("while dormant the state is still computed: %+v", st)
	}
}

func TestOnRefusedFiresOncePerRefusal(t *testing.T) {
	e := newEnv(t)
	e.signIn(10*time.Minute, time.Hour)
	got := make(chan account.Status, 8)
	e.g.OnRefused(func(st account.Status) { got <- st })
	if st := e.g.Status(); st.State != account.StateOK {
		t.Fatal(st)
	}
	refuse := func() {
		e.save(&account.Session{V: 1, Host: account.HostURL, User: &account.User{ID: "user-1"}, State: "refused"})
		e.f.Clock.Advance(account.PollInterval)
		e.g.Status()
	}
	refuse()
	select {
	case st := <-got:
		if st.State != account.StateLocked || st.Reason != account.ReasonRefused {
			t.Fatalf("callback got %s/%q", st.State, st.Reason)
		}
	case <-time.After(testWait):
		t.Fatal("OnRefused did not fire")
	}
	e.g.Status()
	e.g.Status()
	settle(t, e.loops...)
	if len(got) != 0 {
		t.Fatal("OnRefused fired again for the same refusal")
	}
	late := make(chan account.Status, 1)
	e.g.OnRefused(func(st account.Status) { late <- st })
	select {
	case <-late:
	case <-time.After(testWait):
		t.Fatal("a callback registered during a refusal must fire at once")
	}
	// A new sign-in ends the refusal; a second refusal is a new transition.
	e.signIn(time.Minute, time.Hour)
	e.f.Clock.Advance(account.PollInterval)
	if st := e.g.Status(); st.State != account.StateOK {
		t.Fatalf("after signing in again: %s/%q", st.State, st.Reason)
	}
	refuse()
	select {
	case <-got:
	case <-time.After(testWait):
		t.Fatal("OnRefused did not fire for the second refusal")
	}
}

func TestACorruptSessionFileIsLockedInvalidAndASignInRepairsIt(t *testing.T) {
	e := newEnv(t)
	if err := os.MkdirAll(e.dir, 0o700); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(e.dir, "session.json")
	if err := os.WriteFile(path, []byte("{not json"), 0o600); err != nil {
		t.Fatal(err)
	}
	if st := e.g.Status(); st.State != account.StateLocked || st.Reason != account.ReasonInvalid {
		t.Fatalf("a corrupt session.json: Status = %s/%q, want locked/invalid", st.State, st.Reason)
	}
	e.signIn(10*time.Minute, time.Hour)
	e.f.Clock.Advance(account.PollInterval)
	if st := e.g.Status(); st.State != account.StateOK {
		t.Fatalf("after a sign-in over the corrupt file: %s/%q", st.State, st.Reason)
	}
	// A file that later turns corrupt is judged at the next poll as every new process
	// judges it, whatever this guard had cached (a read that fails on the disk keeps the
	// session that works: TestAReadThatFailsOnTheDiskNeverChangesAWorkingVerdict).
	if err := os.WriteFile(path, []byte("{not json"), 0o600); err != nil {
		t.Fatal(err)
	}
	e.touch()
	e.f.Clock.Advance(account.PollInterval)
	if st := e.g.Status(); st.State != account.StateLocked || st.Reason != account.ReasonInvalid {
		t.Fatalf("a corrupt file over a working session: Status = %s/%q, want locked/invalid", st.State, st.Reason)
	}
}

// A logout removes session.json and refresh.enc under the lock. A guard that
// had the session cached sees "no session" at its next poll, and keeps working.
func TestAGuardNoticesASessionThatWasRemoved(t *testing.T) {
	e := newEnv(t)
	e.signIn(10*time.Minute, time.Hour)
	if st := e.g.Status(); st.State != account.StateOK {
		t.Fatalf("Status = %s", st.State)
	}
	if err := os.Remove(filepath.Join(e.dir, "session.json")); err != nil {
		t.Fatal(err)
	}
	if err := e.store.DeleteRefresh(); err != nil {
		t.Fatal(err)
	}
	e.f.Clock.Advance(account.PollInterval)
	if st := e.g.Status(); st.State != account.StateLocked || st.Reason != account.ReasonNotLoggedIn {
		t.Fatalf("after the logout: Status = %s/%q, want locked/not_logged_in", st.State, st.Reason)
	}
	e.signIn(time.Minute, time.Hour) // a later sign-in is picked up
	e.f.Clock.Advance(account.PollInterval)
	if st := e.g.Status(); st.State != account.StateOK {
		t.Fatalf("after signing in again: %s/%q", st.State, st.Reason)
	}
}

func TestAGuardWithNothingStoredIsNotLoggedIn(t *testing.T) {
	f := accounttest.New(t)
	g := account.NewGuard(account.GuardOptions{Store: account.OpenStore(filepath.Join(t.TempDir(), "account"), account.NewMemorySealer()), Now: f.Clock.Now})
	t.Cleanup(g.Close)
	st := g.Status()
	if st.State != account.StateLocked || st.Reason != account.ReasonNotLoggedIn || !st.Enforced || st.Allowed() {
		t.Fatalf("Status = %+v", st)
	}
}
