package account_test

import (
	"context"
	"sync"
	"testing"
	"time"

	"github.com/monoes/mono-agent/internal/account"
	"github.com/monoes/mono-agent/internal/account/accounttest"
)

// A clock that went back since the last attempt this machine recorded (A24). The age of a
// marker is read on this machine's clock, and every LastAttempt is written from it too, so
// a clock that reads before the session's LastAttempt has gone back, and the age of the
// marker can no longer be told: an answer lost at t0, retries lost again at +30, +90 and
// +210 s, and a clock stepped back to read t0+100 s make the marker look 100 s old although
// at least 210 s have passed, perhaps far more. The token is dropped, not presented: the
// unsafe outcome is a revocation of every install of the account, the safe one a needless
// sign-in on this machine.

// retriedThreeTimes is an answer lost at t0 whose retries at +30, +90 and +210 s were lost
// again: the marker says t0 and the last attempt t0+210 s, and the clock reads t0+210 s.
func retriedThreeTimes(t *testing.T) *lostRig {
	t.Helper()
	r := newLostRig(t)
	r.loseTheFirstAnswer(t)
	for _, step := range []time.Duration{30 * time.Second, 60 * time.Second, 120 * time.Second} {
		r.net.then(lost)
		r.e.f.Clock.Advance(step)
		if _, err := r.command(); err != nil {
			t.Fatalf("the retry at +%v: %v", r.e.f.Clock.Now().Sub(r.t0), err)
		}
	}
	if sess := r.e.session(); !sess.PendingSince.Equal(r.t0) || !sess.LastAttempt.Equal(r.t0.Add(210*time.Second)) || r.net.grants() != 4 {
		t.Fatalf("after the retries: %s with %d grants, want the marker of t0, the last attempt at +210 s and four grants", describe(sess), r.net.grants())
	}
	return r
}

func TestAClockThatReadsBeforeTheLastAttemptDropsTheTokenInsideTheWindow(t *testing.T) {
	for _, c := range []struct {
		name string
		at   time.Duration // what the clock reads, from t0
	}{
		{"set back to +100 s", 100 * time.Second},
		{"set back to the instant of the marker", 0},
		{"set back a nanosecond before the last attempt", 210*time.Second - time.Nanosecond},
	} {
		t.Run(c.name, func(t *testing.T) {
			r := retriedThreeTimes(t)
			r.e.f.Clock.Set(r.t0.Add(c.at))
			if _, err := r.command(); err != nil {
				t.Fatalf("the pass with the clock at +%v: %v", c.at, err)
			}
			if n := r.net.grants(); n != 4 {
				t.Fatalf("%d grants, want the four of before: a clock that reads before the last attempt cannot tell the age of the marker, and the token may be long past the window", n)
			}
			if !r.refreshFileGone() || r.e.rawPending() != "" {
				t.Fatalf("refresh token gone %t, pending %q, want the token dropped and no marker", r.refreshFileGone(), r.e.rawPending())
			}
			if sess := r.e.session(); sess.LastResult != "unconfirmed" || !sess.LastAttempt.Equal(r.t0.Add(210*time.Second)) {
				t.Fatalf("stored session = %s, want unconfirmed with the last attempt kept at +210 s: it never moves back with the clock while a token is in doubt", describe(sess))
			}
			if r.srv.isRevoked() || count(r.srv.presented(), "rt-1") != 4 {
				t.Fatalf("monoes.me was presented %v (revoked %t), want rt-1 four times and never again", r.srv.presented(), r.srv.isRevoked())
			}
		})
	}
}

// The instant of the last attempt itself is no clock going back: two processes can pass in
// the same tick, and the second one must retry inside the window, not drop a token that
// monoes.me still answers.
func TestTheInstantOfTheLastAttemptIsNoClockGoingBack(t *testing.T) {
	r := retriedThreeTimes(t) // the clock reads +210 s, the instant of the last attempt
	st, err := r.command()
	if err != nil || st.State != account.StateOK || r.net.grants() != 5 || r.srv.isRevoked() {
		t.Fatalf("the pass at the instant of the last attempt = %s/%q, %v with %d grants (revoked %t), want ok after a fifth presentation inside the window", st.State, st.Reason, err, r.net.grants(), r.srv.isRevoked())
	}
	if rt, _ := r.e.store.LoadRefresh(); rt != "rt-rotated-1" || r.e.rawPending() != "" {
		t.Fatalf("refresh.enc holds %q with pending %q, want the rotated token and no marker", rt, r.e.rawPending())
	}
}

// The time of a pass is read once the lock is held and the session read under it: another
// process may have recorded an attempt while this one waited for the lock, and a time read
// before the wait would lie before that attempt and look like a clock that went back.
func TestAnAttemptRecordedWhileThisPassWaitedForTheLockIsNoClockGoingBack(t *testing.T) {
	r := newLostRig(t)
	r.loseTheFirstAnswer(t)
	r.e.f.Clock.Advance(50 * time.Second)
	spy := r.e.spy()
	var once sync.Once
	spy.afterLock = func() {
		once.Do(func() {
			// Meanwhile another process retried at +55 s, lost the answer again and recorded it,
			// and the clock moved on to +60 s.
			sess := r.e.session()
			sess.LastAttempt, sess.LastResult = r.t0.Add(55*time.Second), "unreachable"
			r.e.save(sess)
			r.e.f.Clock.Set(r.t0.Add(60 * time.Second))
		})
	}
	st, err := r.e.guardWith(r.net, spy).EnsureFresh(context.Background())
	if err != nil || st.State != account.StateOK || r.net.grants() != 2 || r.srv.isRevoked() {
		t.Fatalf("the pass = %s/%q, %v with %d grants (revoked %t), want ok after the retry: the attempt of the other process lies before this pass's time", st.State, st.Reason, err, r.net.grants(), r.srv.isRevoked())
	}
	if sess := r.e.session(); sess.LastResult != "ok" || r.e.rawPending() != "" {
		t.Fatalf("stored session = %s, want the answer stored and no marker", describe(sess))
	}
}

// The rule is for a marker only: with no grant in doubt a clock that went back is repaired
// by a refresh, as before, and the token is presented as usual.
func TestAClockThatWentBackWithNoMarkerRefreshesAsUsual(t *testing.T) {
	for _, ep := range entryPoints {
		t.Run(ep.name, func(t *testing.T) {
			e := newEnv(t)
			e.signIn(2*time.Hour, time.Hour) // in grace: due
			e.ref.set(func(r *fakeRefresher) { r.err = transient(account.ReasonUnreachable) })
			if _, err := ep.call(e.g, context.Background()); err != nil {
				t.Fatal(err)
			}
			last := e.session().LastAttempt
			if e.rawPending() != "" || e.ref.calls.Load() != 1 {
				t.Fatalf("the settled failure left pending %q after %d calls, want no marker after one", e.rawPending(), e.ref.calls.Load())
			}
			e.ref.set(func(r *fakeRefresher) { r.err = nil })
			e.f.Clock.Set(last.Add(-10 * time.Minute)) // the clock goes back ten minutes
			st, err := ep.call(e.newGuard(0), context.Background())
			if err != nil || st.State != account.StateOK || e.ref.calls.Load() != 2 {
				t.Fatalf("%s with the clock set back = %s/%q, %v with %d calls, want ok after presenting the token", ep.name, st.State, st.Reason, err, e.ref.calls.Load())
			}
			if rt, _ := e.store.LoadRefresh(); rt != "rt-2" {
				t.Fatalf("refresh.enc holds %q, want the rotated token", rt)
			}
		})
	}
}

// The refresher's own retries at +30, +90 and +210 s, and then the clock goes back to read
// +100 s: the wait for its next attempt, now longer than the longest backoff, ends at once
// (the loop's rule for a clock that went back), and that pass finds a marker it cannot age.
// It drops the token instead of presenting it.
func TestTheRefresherDropsTheTokenWhenTheClockGoesBackBeforeItsLastAttempt(t *testing.T) {
	e, srv, net, g := loopMachine(t, 2*time.Hour) // in grace: every pass is due
	net.then(lost, lost, lost, lost)
	t0 := e.f.Clock.Now()
	g.StartRefresher(context.Background())
	waitForGrants(t, net, 1, "the first attempt")
	expectGrants(t, e, net, 1, "the first lost answer")
	for _, step := range []struct {
		advance time.Duration
		grants  int
	}{{31 * time.Second, 2}, {61 * time.Second, 3}, {121 * time.Second, 4}} { // +31 s, +92 s, +213 s
		e.f.Clock.Advance(step.advance)
		waitForGrants(t, net, step.grants, "the retry")
		expectGrants(t, e, net, step.grants, "the retry, lost again")
	}
	waitForAttempt(t, e.store, t0.Add(213*time.Second), "the record of the retry at +213 s")
	if sess := e.session(); !sess.PendingSince.Equal(t0) || !sess.LastAttempt.Equal(t0.Add(213*time.Second)) {
		t.Fatalf("after the retries: %s, want the marker of the first send and the last attempt at +213 s", describe(sess))
	}

	e.f.Clock.Set(t0.Add(100 * time.Second))
	eventually(t, "a pass of the refresher at the clock that went back", func() bool {
		return e.session().LastResult == "unconfirmed" || net.grants() > 4
	})
	quiet(t, e.loops...)
	if n := net.grants(); n != 4 || srv.isRevoked() {
		t.Fatalf("%d grants (revoked %t), want the four lost ones and no more: the marker cannot be aged on a clock that went back", n, srv.isRevoked())
	}
	if rt, _ := e.store.LoadRefresh(); rt != "" || e.rawPending() != "" {
		t.Fatalf("refresh.enc holds %q with pending %q, want the token dropped and no marker", rt, e.rawPending())
	}
}

// suspendedInTheTokenRead is a process that is suspended (a lid closed, Ctrl-Z, a paused VM)
// while the key store hands it the refresh token: the clock runs on meanwhile.
type suspendedInTheTokenRead struct {
	account.Store
	clock *accounttest.Clock
	for_  time.Duration
}

func (s suspendedInTheTokenRead) LoadRefresh() (string, error) {
	rt, err := s.Store.LoadRefresh()
	s.clock.Advance(s.for_)
	return rt, err
}

// Reading the refresh token can take up to keyStoreTimeout, and any time at all when the
// process is suspended meanwhile, and the grant goes out after it. The marker is judged on
// the clock read after the token, the clock of the send: a retry judged at +200 s whose
// process slept 120 s in the key store would reach monoes.me at +320 s, past its window.
func TestTheMarkerIsJudgedOnTheClockOfTheSendNotOfTheSessionRead(t *testing.T) {
	t.Run("a process suspended past the window drops the token", func(t *testing.T) {
		r := newLostRig(t)
		r.loseTheFirstAnswer(t)
		r.e.f.Clock.Advance(200 * time.Second) // a command at +200 s, inside the window
		st, err := r.passWith(suspendedInTheTokenRead{Store: account.OpenStore(r.e.dir, r.e.seal), clock: r.e.f.Clock, for_: 120 * time.Second})
		if err != nil || r.net.grants() != 1 || r.srv.isRevoked() {
			t.Fatalf("the command = %s/%q, %v with %d grants (revoked %t), want the token not presented at +320 s", st.State, st.Reason, err, r.net.grants(), r.srv.isRevoked())
		}
		if sess := r.e.session(); sess.LastResult != "unconfirmed" || !r.refreshFileGone() || !sess.LastAttempt.Equal(r.t0.Add(320*time.Second)) {
			t.Fatalf("stored session = %s (token gone %t), want the token dropped and the attempt recorded at +320 s", describe(sess), r.refreshFileGone())
		}
	})
	t.Run("a process suspended inside the window retries", func(t *testing.T) {
		r := newLostRig(t)
		r.loseTheFirstAnswer(t)
		r.e.f.Clock.Advance(60 * time.Second)
		st, err := r.passWith(suspendedInTheTokenRead{Store: account.OpenStore(r.e.dir, r.e.seal), clock: r.e.f.Clock, for_: 30 * time.Second})
		if err != nil || st.State != account.StateOK || r.net.grants() != 2 || r.srv.isRevoked() || r.e.rawPending() != "" {
			t.Fatalf("the command = %s/%q, %v with %d grants (revoked %t, pending %q), want ok after the retry at +90 s", st.State, st.Reason, err, r.net.grants(), r.srv.isRevoked(), r.e.rawPending())
		}
	})
}

// A clock that went back BEFORE a grant: an attempt recorded at t0 (offline, nothing in doubt),
// the clock set back 59 s, and then a grant whose answer is lost. Nothing was in doubt when the
// earlier attempt was recorded, so it says nothing about a clock that went back since the stamp:
// the stamp starts the evidence afresh, and the retry 20 s later is a retry, not a drop.
func TestAClockSetBackBeforeAGrantDoesNotDropItsFirstRetry(t *testing.T) {
	r := newLostRig(t)
	r.net.then(unsent)
	if _, err := r.command(); err != nil { // offline at t0: the attempt is recorded, nothing is in doubt
		t.Fatal(err)
	}
	r.e.f.Clock.Set(r.t0.Add(-59 * time.Second)) // the clock is set back
	r.net.then(lost)
	if _, err := r.command(); err != nil { // monoes.me rotates, the answer is lost: the marker at t0-59 s
		t.Fatal(err)
	}
	if sess := r.e.session(); !sess.PendingSince.Equal(r.t0.Add(-59*time.Second)) || sess.LastAttempt.After(sess.PendingSince) {
		t.Errorf("stored session = %s, want the marker at t0-59 s and no attempt recorded after it", describe(sess))
	}
	r.e.f.Clock.Advance(20 * time.Second) // well inside monoes.me's window
	st, err := r.command()
	if err != nil || st.State != account.StateOK || r.srv.isRevoked() {
		t.Fatalf("the retry 20 s after the lost answer = %s/%q, %v (revoked %t), want ok", st.State, st.Reason, err, r.srv.isRevoked())
	}
	if sess := r.e.session(); sess.LastResult != "ok" || r.e.rawPending() != "" || count(r.srv.presented(), "rt-1") != 2 {
		t.Fatalf("stored session = %s, presented %v, want the answer recovered by the retry", describe(sess), r.srv.presented())
	}
}
