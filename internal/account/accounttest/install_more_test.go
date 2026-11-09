package accounttest

import (
	"context"
	"fmt"
	"github.com/monoes/mono-agent/internal/testhome"
	"os"
	"runtime"
	"slices"
	"testing"
	"time"

	"github.com/monoes/mono-agent/internal/account"
)

// recordingTB is a testing.TB that keeps the cleanups it is given instead of
// queueing them on the real test, so a test can end "a test" when it chooses,
// and that remembers every temporary directory it was asked for. Whatever it
// does not override is the real test's.
type recordingTB struct {
	testing.TB
	cleanups []func()
	dirs     []string
}

func (r *recordingTB) Cleanup(f func()) { r.cleanups = append(r.cleanups, f) }

// Setenv does nothing, as everything the recorder is given stays off the real test
// (the fixtures mark the test with t.Setenv; TestFixturesPanicWhenMixedWithTParallel tries that on the real one).
func (r *recordingTB) Setenv(key, value string) {}

func (r *recordingTB) TempDir() string {
	dir := r.TB.TempDir()
	r.dirs = append(r.dirs, dir)
	return dir
}

// end runs what the "test" registered, the last registered first, as the
// testing package does. It is safe to call twice.
func (r *recordingTB) end() {
	for len(r.cleanups) > 0 {
		f := r.cleanups[len(r.cleanups)-1]
		r.cleanups = r.cleanups[:len(r.cleanups)-1]
		f()
	}
}

var allModes = []struct {
	name string
	mode Mode
}{
	{"SignedIn", SignedIn},
	{"InGrace", InGrace},
	{"LockedNoLogin", LockedNoLogin},
	{"LockedRefused", LockedRefused},
	{"Dormant", Dormant},
}

// theUser is the account of every mode that has a session.
var theUser = account.User{ID: "user-1", Email: "user@example.test", Username: "user"}

func kids(keys []account.Key) []string {
	out := []string{}
	for _, k := range keys {
		out = append(out, k.KID)
	}
	return out
}

func expectStatus(t *testing.T, g *account.Guard, state account.State, reason account.Reason) account.Status {
	t.Helper()
	st := g.Status()
	if st.State != state || st.Reason != reason {
		t.Fatalf("Status = %s/%q, want %s/%q", st.State, st.Reason, state, reason)
	}
	return st
}

// Install changes four process-wide things (the guard, the enforcement date,
// the trusted keys, strictness) and builds a guard of its own. All of it goes
// back when the test ends, whatever the mode.
func TestInstallRestoresEverythingWhenItsTestEnds(t *testing.T) {
	account.InstallForTest(t, nil)
	past := time.Now().Add(-time.Hour)
	account.SetEnforceFromForTest(t, past) // the gate is enforced, and nothing asks for strictness
	keysBefore := kids(account.TrustedKeys())
	ctx := context.Background()
	for _, c := range allModes {
		t.Run(c.name, func(t *testing.T) {
			rec := &recordingTB{TB: t}
			t.Cleanup(rec.end) // a failed Install must not leave its hooks behind
			g, f := InstallWithFixture(rec, c.mode)
			if account.Current() != g {
				t.Fatal("the guard is not the process guard while the test runs")
			}
			if got := kids(account.TrustedKeys()); !slices.Equal(got, []string{f.Key.KID}) {
				t.Fatalf("while the test runs the trusted keys are %v, want only the fixture's", got)
			}
			rec.end()
			if account.Current() != nil {
				t.Fatalf("the process guard was not put back: %v", account.Current())
			}
			if got := account.EnforceDate(); !got.Equal(past) {
				t.Fatalf("the enforcement date is %v after the test, want %v", got, past)
			}
			if got := kids(account.TrustedKeys()); !slices.Equal(got, keysBefore) {
				t.Fatalf("the trusted keys are %v after the test, want %v", got, keysBefore)
			}
			// Strictness is off again: the gate is enforced and no guard is installed,
			// yet a test binary is let through (D24).
			if err := account.Require(ctx); err != nil {
				t.Fatalf("strictness outlived the test: Require = %v", err)
			}
		})
	}
}

// A test that starts the refresher on the guard Install built must not leave it
// running: the cleanup closes the guard, which stops the refresher and waits for
// it. The refresher is a goroutine, so it is seen as one: the count falls when
// the test ends. A guard that was closed before the test ended would never have
// started it, and the count would not fall either.
func TestInstallClosesTheGuardWhenItsTestEnds(t *testing.T) {
	rec := &recordingTB{TB: t}
	t.Cleanup(rec.end)
	g := Install(rec, SignedIn)
	t.Cleanup(g.Close) // a backstop, so a failure below does not leave the goroutine behind
	g.StartRefresher(context.Background())
	running := settledGoroutines()
	rec.end()
	waitFor(t, "the cleanup to stop the refresher", func() bool { return runtime.NumGoroutine() < running })
}

// settledGoroutines is the goroutine count once two samples 5 ms apart agree.
func settledGoroutines() int {
	n := runtime.NumGoroutine()
	for i := 0; i < 200; i++ {
		time.Sleep(5 * time.Millisecond)
		m := runtime.NumGoroutine()
		if m == n {
			return n
		}
		n = m
	}
	return n
}

func waitFor(t *testing.T, what string, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(3 * time.Second)
	for !cond() {
		if time.Now().After(deadline) {
			t.Fatalf("timed out waiting for %s", what)
		}
		time.Sleep(2 * time.Millisecond)
	}
}

// The guard reads a store in a temporary directory of its own. A mode with a
// session writes session.json there and nothing else; the two modes without one
// write nothing at all. What is written is what a real sign-in, or a real
// refusal, would have written.
func TestInstallStoresExactlyTheSessionOfItsMode(t *testing.T) {
	for _, c := range allModes {
		t.Run(c.name, func(t *testing.T) {
			rec := &recordingTB{TB: t}
			t.Cleanup(rec.end)
			g, f := InstallWithFixture(rec, c.mode)
			now := f.Clock.Now()
			if len(rec.dirs) != 1 {
				t.Fatalf("Install asked for %d temporary directories, want its one store", len(rec.dirs))
			}
			entries, err := os.ReadDir(rec.dirs[0])
			if err != nil {
				t.Fatal(err)
			}
			names := []string{}
			for _, e := range entries {
				names = append(names, e.Name())
			}
			sess, err := account.OpenStore(rec.dirs[0], account.NewMemorySealer()).Load()
			if err != nil {
				t.Fatalf("the stored session cannot be read: %v", err)
			}
			if c.mode == LockedNoLogin || c.mode == Dormant {
				if len(names) != 0 || sess != nil {
					t.Fatalf("a mode with no login stored %v (a session: %v)", names, sess != nil)
				}
				if st := g.Status(); st.User != nil {
					t.Fatalf("a mode with no login has a user: %+v", st.User)
				}
				return
			}
			if !slices.Equal(names, []string{"session.json"}) || sess == nil {
				t.Fatalf("the store holds %v, want only session.json", names)
			}
			if sess.V != 1 || sess.Host != account.HostURL || sess.User == nil || *sess.User != theUser {
				t.Fatalf("the stored session is version %d for %s as %+v, want version 1 for %s as %+v", sess.V, sess.Host, sess.User, account.HostURL, theUser)
			}
			if st := g.Status(); st.User == nil || *st.User != theUser {
				t.Fatalf("the verdict's user is %+v, want %+v", st.User, theUser)
			}
			switch c.mode {
			case SignedIn, InGrace:
				r, err := account.Verify(sess.AccessToken, now)
				if err != nil {
					t.Fatalf("the stored token does not verify: %v", err)
				}
				wantIssued := now
				wantResult := "ok"
				if c.mode == InGrace {
					wantIssued, wantResult = now.Add(-2*time.Hour), "unreachable"
				}
				if !r.IssuedAt.Equal(wantIssued) || !r.ExpiresAt.Equal(wantIssued.Add(time.Hour)) || r.Sub != "user-1" {
					t.Fatalf("the token is %+v, want a one-hour token of user-1 issued at %v", r, wantIssued)
				}
				if !sess.HW.Equal(wantIssued) || !sess.LastAttempt.Equal(now) || sess.LastResult != wantResult || sess.State != "" {
					t.Fatalf("the session state is hw=%v attempt=%v result=%q state=%q, want hw=%v attempt=%v result=%q state=%q",
						sess.HW, sess.LastAttempt, sess.LastResult, sess.State, wantIssued, now, wantResult, "")
				}
			case LockedRefused:
				if sess.State != "refused" || sess.AccessToken != "" || sess.LastResult != "refused" || !sess.LastAttempt.Equal(now) {
					t.Fatalf("the refusal marker has state %q, result %q, attempt %v and a token: %v, want what a refusal writes: state and result refused, no token, attempted %v",
						sess.State, sess.LastResult, sess.LastAttempt, sess.AccessToken != "", now)
				}
			}
		})
	}
}

// Nothing the fixture does goes near the default store under HOME.
func TestInstallNeverTouchesTheDefaultStore(t *testing.T) {
	home := t.TempDir()
	testhome.SetHome(t, home)
	for _, c := range allModes {
		t.Run(c.name, func(t *testing.T) {
			g := Install(t, c.mode)
			_ = g.Status()
			_ = account.Require(context.Background())
			_ = account.CurrentStatus()
		})
	}
	if entries, _ := os.ReadDir(home); len(entries) != 0 {
		names := []string{}
		for _, e := range entries {
			names = append(names, e.Name())
		}
		t.Fatalf("an empty HOME gained %v", names)
	}
}

// Dormant is the zero enforcement date, not a date that is still ahead: nothing
// is enforced, nothing warns, and a refresher would not start.
func TestInstallDormantHasNoEnforcementDate(t *testing.T) {
	for _, c := range allModes {
		t.Run(c.name, func(t *testing.T) {
			g := Install(t, c.mode)
			st := g.Status()
			if dormant := c.mode == Dormant; account.EnforceDate().IsZero() != dormant || st.EnforceFrom.IsZero() != dormant || st.Enforced == dormant {
				t.Fatalf("EnforceDate = %v, Status = %+v: want a zero date and an unenforced verdict only for Dormant", account.EnforceDate(), st)
			}
		})
	}
}

// Each mode keeps its promise as the fixture clock moves: the one-hour token
// ends at the hour, the 24 hours of grace run from the token's iat.
func TestInstallModesAsTheClockMoves(t *testing.T) {
	t.Run("SignedIn is a one-hour token issued now", func(t *testing.T) {
		g, f := InstallWithFixture(t, SignedIn)
		now := f.Clock.Now()
		st := expectStatus(t, g, account.StateOK, account.ReasonNone)
		if !st.IssuedAt.Equal(now) || !st.ValidUntil.Equal(now.Add(time.Hour)) {
			t.Fatalf("the token is valid from %v to %v, want one hour from %v", st.IssuedAt, st.ValidUntil, now)
		}
		f.Clock.Set(now.Add(time.Hour - time.Second))
		expectStatus(t, g, account.StateOK, account.ReasonNone)
		f.Clock.Set(now.Add(time.Hour))
		expectStatus(t, g, account.StateGrace, account.ReasonUnreachable)
		f.Clock.Set(st.GraceUntil.Add(-time.Second))
		expectStatus(t, g, account.StateGrace, account.ReasonUnreachable)
		f.Clock.Set(st.GraceUntil)
		expectStatus(t, g, account.StateLocked, account.ReasonExpired)
	})
	t.Run("InGrace has an expired token and a grace that is still running", func(t *testing.T) {
		g, f := InstallWithFixture(t, InGrace)
		now := f.Clock.Now()
		st := expectStatus(t, g, account.StateGrace, account.ReasonUnreachable)
		if st.ValidUntil.After(now) || !now.Before(st.GraceUntil) {
			t.Fatalf("valid until %v, grace until %v at %v: want an expired token inside its grace", st.ValidUntil, st.GraceUntil, now)
		}
		f.Clock.Set(st.GraceUntil.Add(-time.Second))
		expectStatus(t, g, account.StateGrace, account.ReasonUnreachable)
		f.Clock.Set(st.GraceUntil)
		expectStatus(t, g, account.StateLocked, account.ReasonExpired)
	})
	for _, c := range []struct {
		name   string
		mode   Mode
		reason account.Reason
	}{{"LockedNoLogin", LockedNoLogin, account.ReasonNotLoggedIn}, {"LockedRefused", LockedRefused, account.ReasonRefused}} {
		t.Run(c.name+" stays locked", func(t *testing.T) {
			g, f := InstallWithFixture(t, c.mode)
			f.Clock.Advance(48 * time.Hour)
			expectStatus(t, g, account.StateLocked, c.reason)
		})
	}
	t.Run("Dormant allows everything whenever it is", func(t *testing.T) {
		g, f := InstallWithFixture(t, Dormant)
		for _, step := range []time.Duration{0, 48 * time.Hour, -96 * time.Hour} {
			f.Clock.Advance(step)
			if st := g.Status(); st.Enforced || !st.Allowed() {
				t.Fatalf("at %v: %+v", f.Clock.Now(), st)
			}
		}
	})
}

// The refusal marker the fixture stores is one the guard treats as a refusal:
// OnRefused fires, as it does for the marker of a real refresh that monoes.me
// answered with invalid_grant.
func TestInstallLockedRefusedFiresOnRefused(t *testing.T) {
	g := Install(t, LockedRefused)
	got := make(chan account.Status, 1)
	g.OnRefused(func(st account.Status) { got <- st })
	select {
	case st := <-got:
		if st.State != account.StateLocked || st.Reason != account.ReasonRefused || st.User == nil || *st.User != theUser {
			t.Fatalf("OnRefused was told %+v", st)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("OnRefused did not fire for the stored refusal marker")
	}
}

// fatalRecorder is a recordingTB whose Fatalf records its message and ends the
// goroutine it is called in, as the real one does, so that a test can watch a
// fixture refuse without failing itself.
type fatalRecorder struct {
	recordingTB
	msg string
}

func (r *fatalRecorder) Fatalf(format string, args ...any) {
	r.msg = fmt.Sprintf(format, args...)
	runtime.Goexit()
}

// A Mode that is not one of the five is a mistake in the test, and the fixture
// fails the test on the spot instead of quietly building a guard of some other
// mode. The zero Mode stays SignedIn (index section 3.3).
func TestInstallFailsLoudlyOnAnUnknownMode(t *testing.T) {
	for _, m := range []Mode{-1, Dormant + 1, 99} {
		rec := &fatalRecorder{recordingTB: recordingTB{TB: t}}
		t.Cleanup(rec.end)
		done := make(chan struct{})
		go func() {
			defer close(done)
			InstallWithFixture(rec, m)
		}()
		<-done
		rec.end()
		if want := fmt.Sprintf("accounttest: unknown mode %d", m); rec.msg != want {
			t.Errorf("Mode %d: the fixture said %q, want %q", m, rec.msg, want)
		}
	}
}

func TestTheZeroModeIsSignedIn(t *testing.T) {
	var zero Mode
	if zero != SignedIn {
		t.Fatalf("the zero Mode is %d, want SignedIn (%d)", zero, SignedIn)
	}
	expectStatus(t, Install(t, zero), account.StateOK, account.ReasonNone)
}
