package account_test

import (
	"context"
	"testing"
	"time"

	"github.com/monoes/mono-agent/internal/account"
	"github.com/monoes/mono-agent/internal/account/accounttest"
)

// Two invariants that keep a refresh token in doubt from ever being presented (A24), checked
// over every kind of pass, on a clock that reads before the session's last attempt (set back)
// and on one after it:
//
//   - once a session is in doubt (it has a marker or is unconfirmed), no later write of the guard
//     moves LastAttempt back: a clock that reads before it is the evidence that the clock went
//     back, and the age of a marker cannot be told then (pendingExpired). The write that starts
//     the doubt, a fresh stamp, starts the evidence at the stamp (markPending), as with nothing in
//     doubt the last attempt follows the clock;
//   - unconfirmed is left only by a sign-in (NewSession): it is what keeps a token that a drop
//     gave up from being presented when that token is still, or again, on disk.

// doubt is a session with a refresh token in doubt, built by what the guard did, and the last
// attempt it records.
type doubt struct {
	name  string
	build func(t *testing.T) (r *lostRig, last time.Time)
	// unconfirmed: the session is unconfirmed, so no grant may go out whatever the clock says
	unconfirmed bool
}

var doubts = []doubt{
	{"a marker after three retries", func(t *testing.T) (*lostRig, time.Time) {
		r := retriedThreeTimes(t)
		return r, r.t0.Add(210 * time.Second)
	}, false},
	{"a marker and unconfirmed, the token stuck on disk", func(t *testing.T) (*lostRig, time.Time) {
		r := newLostRig(t)
		r.loseTheFirstAnswer(t)
		r.e.f.Clock.Advance(241 * time.Second)
		_, _ = r.passWith(&stuckRefreshFile{Store: account.OpenStore(r.e.dir, r.e.seal), stuck: true})
		if sess := r.e.session(); sess.LastResult != "unconfirmed" || sess.PendingSince.IsZero() {
			t.Fatalf("setup: %s, want unconfirmed with the marker", describe(sess))
		}
		return r, r.t0.Add(241 * time.Second)
	}, true},
	{"unconfirmed, the token gone", func(t *testing.T) (*lostRig, time.Time) {
		r := newLostRig(t)
		r.loseTheFirstAnswer(t)
		r.e.f.Clock.Advance(241 * time.Second)
		_, _ = r.command()
		if sess := r.e.session(); sess.LastResult != "unconfirmed" || !r.refreshFileGone() {
			t.Fatalf("setup: %s, want unconfirmed and no refresh token", describe(sess))
		}
		return r, r.t0.Add(241 * time.Second)
	}, true},
	{"unconfirmed, the token back on disk", func(t *testing.T) (*lostRig, time.Time) {
		r := newLostRig(t)
		r.loseTheFirstAnswer(t)
		r.e.f.Clock.Advance(241 * time.Second)
		_, _ = r.command()
		if err := r.e.store.SaveRefresh("rt-1"); err != nil {
			t.Fatal(err)
		}
		return r, r.t0.Add(241 * time.Second)
	}, true},
	{"unconfirmed, a healthy access token", func(t *testing.T) (*lostRig, time.Time) {
		e, srv, net := strayMarker(t, 241*time.Second) // the access token has 50 minutes left
		r := &lostRig{e: e, srv: srv, net: net, t0: e.f.Clock.Now()}
		_, _ = r.command()
		if sess := r.e.session(); sess.LastResult != "unconfirmed" || sess.AccessToken == "" {
			t.Fatalf("setup: %s, want unconfirmed with the access token kept", describe(sess))
		}
		return r, r.t0
	}, true},
}

// passKinds is every kind of pass of another process: the store it sees and what monoes.me does.
var passKinds = []struct {
	name  string
	store func(r *lostRig) account.Store
	fate  []fate
}{
	{"the key store does not answer", func(r *lostRig) account.Store {
		return &keyStoreDown{Store: account.OpenStore(r.e.dir, r.e.seal), down: true}
	}, nil},
	{"refresh.enc can be neither replaced nor removed", func(r *lostRig) account.Store {
		return &stuckRefreshFile{Store: account.OpenStore(r.e.dir, r.e.seal), stuck: true}
	}, nil},
	{"monoes.me answers", func(r *lostRig) account.Store { return account.OpenStore(r.e.dir, r.e.seal) }, []fate{arrives}},
	{"the answer is lost", func(r *lostRig) account.Store { return account.OpenStore(r.e.dir, r.e.seal) }, []fate{lost}},
	{"the request never leaves", func(r *lostRig) account.Store { return account.OpenStore(r.e.dir, r.e.seal) }, []fate{unsent}},
	{"an error of no known type", func(r *lostRig) account.Store { return account.OpenStore(r.e.dir, r.e.seal) }, []fate{plain}},
}

func TestNoPassMovesTheLastAttemptBackOrLeavesUnconfirmedButASignIn(t *testing.T) {
	for _, d := range doubts {
		for _, p := range passKinds {
			for _, c := range []struct {
				name string
				at   time.Duration // from the last attempt
			}{{"the clock set back before the last attempt", -time.Minute}, {"the clock after it", 30 * time.Second}} {
				t.Run(d.name+"/"+p.name+"/"+c.name, func(t *testing.T) {
					r, last := d.build(t)
					grants := r.net.grants()
					r.e.f.Clock.Set(last.Add(c.at))
					r.net.then(p.fate...)
					_, _ = r.passWith(p.store(r))
					sess := r.e.session()
					if (!sess.PendingSince.IsZero() || sess.LastResult == "unconfirmed") && sess.LastAttempt.Before(last) {
						t.Errorf("stored session = %s: the last attempt went back from %v while the session is in doubt", describe(sess), last)
					}
					if d.unconfirmed && sess.LastResult != "unconfirmed" {
						t.Errorf("stored session = %s: unconfirmed was left without a sign-in", describe(sess))
					}
					if (d.unconfirmed || c.at < 0) && r.net.grants() != grants {
						t.Errorf("%d grants, want the %d of before: a token in doubt was presented", r.net.grants(), grants)
					}
					if r.srv.isRevoked() {
						t.Error("the account was revoked")
					}
				})
			}
		}
	}
}

// A sign-in is what leaves unconfirmed: a new session from a token monoes.me issued, saved with
// its refresh token, replaces it whatever the dropped session held.
func TestOnlyASignInLeavesUnconfirmed(t *testing.T) {
	for _, d := range doubts {
		if !d.unconfirmed {
			continue
		}
		t.Run(d.name, func(t *testing.T) {
			r, _ := d.build(t)
			login := r.e.f.Token(accounttest.TokenOptions{Lifetime: time.Hour})
			sess, err := account.NewSession(account.HostURL, login, &account.User{ID: "user-1"}, r.e.f.Clock.Now())
			if err != nil {
				t.Fatal(err)
			}
			if err := r.e.store.SaveRefresh("rt-login"); err != nil {
				t.Fatal(err)
			}
			r.e.save(sess)
			r.srv.accept("rt-login")
			r.e.f.Clock.Advance(56 * time.Minute) // due by the CLI's margin
			st, err := r.command()
			if err != nil || st.State != account.StateOK || r.e.session().LastResult != "ok" || r.e.rawPending() != "" {
				t.Fatalf("after the sign-in = %s/%q, %v, stored %s, want ok", st.State, st.Reason, err, describe(r.e.session()))
			}
			if got := count(r.srv.presented(), "rt-login"); got != 1 || r.srv.isRevoked() {
				t.Fatalf("monoes.me was presented the new token %d times (revoked %t), want once", got, r.srv.isRevoked())
			}
		})
	}
}

// The rule is for a session in doubt only. With nothing in doubt a failure recorded on a clock
// that went back is the attempt the negative cache counts from, as it always was: one that fails
// at monoes.me (a fresh stamp starts its evidence at the clock, markPending) and one whose key
// store does not answer (no stamp is written then: recordAttempt alone decides).
func TestWithNothingInDoubtTheLastAttemptFollowsTheClockBack(t *testing.T) {
	ctx := context.Background()
	for _, c := range []struct {
		name string
		pass func(e *env) *account.Guard
	}{
		{"an attempt that fails settled", func(e *env) *account.Guard { return e.newGuard(0) }},
		{"a key store that does not answer", func(e *env) *account.Guard {
			return e.guardWith(e.ref, &keyStoreDown{Store: account.OpenStore(e.dir, e.seal), down: true})
		}},
	} {
		t.Run(c.name, func(t *testing.T) {
			e := newEnv(t)
			e.signIn(2*time.Hour, time.Hour) // in grace: due
			e.ref.set(func(r *fakeRefresher) { r.err = transient(account.ReasonUnreachable) })
			if _, err := e.g.EnsureFresh(ctx); err != nil {
				t.Fatal(err)
			}
			back := e.session().LastAttempt.Add(-10 * time.Minute)
			e.f.Clock.Set(back)
			if _, err := c.pass(e).EnsureFresh(ctx); err != nil { // due: a stored time after the clock holds nothing off
				t.Fatal(err)
			}
			if sess := e.session(); !sess.LastAttempt.Equal(back) || e.rawPending() != "" {
				t.Fatalf("stored session = %s, want the second attempt recorded on the clock that went back, %v, and no marker", describe(sess), back)
			}
			calls := e.ref.calls.Load()
			e.f.Clock.Advance(10 * time.Second)
			if _, err := e.newGuard(0).EnsureFresh(ctx); err != nil {
				t.Fatal(err)
			}
			if n := e.ref.calls.Load(); n != calls {
				t.Fatalf("%d calls, want %d: the negative cache counts from the attempt on the clock that went back", n, calls)
			}
		})
	}
}

// The high-water write is the one write of a pass that finds nothing to try, and it reads the
// session again under the lock. Over a session with a marker it also raises the last attempt to
// the clock, never lowering it, so that a running refresher keeps the evidence of a clock set back
// at most a minute old; it changes nothing else, and over a session that a drop left unconfirmed
// without a marker it changes the mark alone. (On a clock that reads before the last attempt it
// writes nothing at all: every write of an attempt has raised the mark to within a minute of it,
// and the mark is written only once it is a minute stale.)

// While the refresher backs off from its own lost answer, it keeps the mark current, and the last
// attempt with it.
func TestAHighWaterWriteWhileTheRefresherBacksOffRaisesTheLastAttemptAndKeepsTheMarker(t *testing.T) {
	e, srv, net, g := loopMachine(t, 2*time.Hour) // in grace: every pass is due
	net.then(lost, lost)
	t0 := e.f.Clock.Now()
	g.StartRefresher(context.Background())
	waitForGrants(t, net, 1, "the first attempt")
	expectGrants(t, e, net, 1, "the first lost answer")
	e.f.Clock.Advance(31 * time.Second)
	waitForGrants(t, net, 2, "the retry")
	expectGrants(t, e, net, 2, "the retry, lost again") // the next attempt is at +91 s
	waitForAttempt(t, e.store, t0.Add(31*time.Second), "the record of the retry")
	before := e.session()
	e.f.Clock.Advance(59 * time.Second) // +90 s: the mark (t0) is a minute and a half stale
	waitForHW(t, e, t0.Add(90*time.Second), "the high-water write while the refresher backs off")
	after := e.session()
	if !after.PendingSince.Equal(t0) || !after.LastAttempt.Equal(t0.Add(90*time.Second)) || after.LastResult != before.LastResult {
		t.Fatalf("stored session = %s, was %s: want the marker and the result kept and the last attempt raised to the mark", describe(after), describe(before))
	}
	if net.grants() != 2 || srv.isRevoked() {
		t.Fatalf("%d grants (revoked %t), want the two of before", net.grants(), srv.isRevoked())
	}
}

// A pass that finds nothing due over a session that a drop left unconfirmed, with its access
// token still good, keeps the mark current and nothing else.
func TestAHighWaterWriteKeepsUnconfirmedAndTheLastAttempt(t *testing.T) {
	e, srv, net := strayMarker(t, 241*time.Second) // the access token has 50 minutes left
	r := &lostRig{e: e, srv: srv, net: net, t0: e.f.Clock.Now()}
	if st, err := r.command(); err != nil || st.State != account.StateOK {
		t.Fatalf("the drop = %s/%q, %v, want ok on the access token that is left", st.State, st.Reason, err)
	}
	before := r.e.session()
	r.e.f.Clock.Advance(2 * time.Minute)
	if st, err := r.command(); err != nil || st.State != account.StateOK {
		t.Fatalf("the pass = %s/%q, %v, want ok", st.State, st.Reason, err)
	}
	after := r.e.session()
	if !after.HW.Equal(r.e.f.Clock.Now()) {
		t.Fatalf("hw = %v, want %v: the pass was meant to write the mark", after.HW, r.e.f.Clock.Now())
	}
	if after.LastResult != "unconfirmed" || !after.LastAttempt.Equal(before.LastAttempt) || !after.PendingSince.IsZero() || r.net.grants() != 0 {
		t.Fatalf("stored session = %s, was %s, with %d grants: the high-water write changed more than the mark", describe(after), describe(before), r.net.grants())
	}
}
