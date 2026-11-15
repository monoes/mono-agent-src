package account_test

import (
	"context"
	"path/filepath"
	"reflect"
	"sync/atomic"
	"testing"
	"time"

	"github.com/monoes/mono-agent/internal/account"
	"github.com/monoes/mono-agent/internal/account/accounttest"
)

// The refresher and a grant whose answer was lost (A24). Its own lost answers are retried on
// its schedule of 30, 60, 120 and 240 s, which puts the retries at about +30, +90 and +210 s
// from the first send, inside the window of 240 s. But a marker that ANOTHER process left (a CLI
// whose answer was lost while the daemon held after a refresh or waited out a long backoff)
// would be looked at only after that hold or backoff, past the window, and dropped although
// it was recoverable: so a marker that this loop did not write ends a hold and caps the wait for
// the next attempt at 30 s. The loop tells its own marker from a foreign one by remembering the
// stamp of its own last unsettled attempt.

// waitForGrants waits, in real time, until the net has carried at least n grants.
func waitForGrants(t *testing.T, net *flakyNet, n int, what string) {
	t.Helper()
	eventually(t, what, func() bool { return net.grants() >= n })
}

// expectGrants waits quietFor and then fails unless the net has carried exactly n grants.
func expectGrants(t *testing.T, e *env, net *flakyNet, n int, what string) {
	t.Helper()
	quiet(t, e.loops...)
	if got := net.grants(); got != n {
		t.Fatalf("%s: %d grants, want %d", what, got, n)
	}
}

// loopMachine is an install whose token is age old (an hour of life) with a refresher running
// over the windowServer through the flakyNet.
func loopMachine(t *testing.T, age time.Duration) (*env, *windowServer, *flakyNet, *account.Guard) {
	t.Helper()
	e := newEnv(t)
	e.signIn(age, time.Hour)
	srv := newWindowServer(e.f, 0, "rt-1")
	net := &flakyNet{srv: srv}
	return e, srv, net, e.guardOn(net, loopPoll)
}

// anotherProcessLosesAnAnswer is a CLI that sent a grant with the refresh token on disk and lost the
// answer: monoes.me has rotated, and the marker is on disk. It does what that process does, under the
// session lock.
func anotherProcessLosesAnAnswer(t *testing.T, e *env, srv *windowServer) {
	t.Helper()
	e.underLock(func() {
		rt, err := e.store.LoadRefresh()
		if err != nil || rt == "" {
			t.Fatalf("the other process found no refresh token (%v)", err)
		}
		if _, err := srv.Refresh(context.Background(), rt); err != nil {
			t.Fatalf("monoes.me refused the other process: %v", err)
		}
		e.leavePending(e.f.Clock.Now())
	})
}

// The loop's own lost answers keep the schedule of the backoff: a cap would turn the retries at
// +31 s, +92 s and +213 s into one every 30 s, which is safe inside the window but not what the
// schedule says. The stamp is the first one throughout, and the third retry, 213 s after the first
// send, is inside the window and recovers.
func TestTheRefreshersOwnLostAnswersAreRetriedOnTheBackoffScheduleAndRecoverInsideTheWindow(t *testing.T) {
	e, srv, net, g := loopMachine(t, 2*time.Hour) // in grace: every pass is due
	net.then(lost, lost, lost)
	g.StartRefresher(context.Background())
	waitForGrants(t, net, 1, "the first attempt") // its answer is lost: the marker is written, the next attempt is 30 s away
	expectGrants(t, e, net, 1, "the first lost answer")
	first := e.rawPending()
	if first == "" {
		t.Fatal("no marker after the first lost answer")
	}

	e.f.Clock.Advance(29 * time.Second)
	expectGrants(t, e, net, 1, "29 seconds after the first attempt")
	e.f.Clock.Advance(2 * time.Second) // +31 s
	waitForGrants(t, net, 2, "the first retry")
	expectGrants(t, e, net, 2, "the first retry, lost again")
	e.f.Clock.Advance(30 * time.Second) // +61 s: a loop that capped its own marker would retry here
	expectGrants(t, e, net, 2, "61 seconds after the first attempt, 30 after the retry: the loop's own marker keeps the backoff of 60 seconds")
	e.f.Clock.Advance(31 * time.Second) // +92 s
	waitForGrants(t, net, 3, "the second retry")
	expectGrants(t, e, net, 3, "the second retry, lost again")
	e.f.Clock.Advance(119 * time.Second) // +211 s
	expectGrants(t, e, net, 3, "a second before the backoff of 120 seconds ends")
	e.f.Clock.Advance(2 * time.Second) // +213 s: inside the 240 s window
	waitForGrants(t, net, 4, "the third retry")
	eventually(t, "the third retry to be answered and stored", func() bool { return e.rawPending() == "" && e.session().LastResult == "ok" })
	if got := net.grants(); got != 4 || srv.isRevoked() || !reflect.DeepEqual(srv.presented(), []string{"rt-1", "rt-1", "rt-1", "rt-1"}) {
		t.Fatalf("%d grants (revoked %t, presented %v), want four presentations of the same token, all inside the window", got, srv.isRevoked(), srv.presented())
	}
	if rt, _ := e.store.LoadRefresh(); rt != "rt-rotated-1" {
		t.Fatalf("refresh.enc holds %q, want the rotated token", rt)
	}
}

// A marker that another process left during the hold after a refresh is retried within 30 s of
// the loop learning of it, not when the hold (half an hour) ends.
func TestAMarkerAnotherProcessLeftDuringTheHoldIsRetriedWithinThirtySecondsAndRecovers(t *testing.T) {
	e, srv, net, g := loopMachine(t, 40*time.Minute) // past its half-life: the first pass refreshes
	g.StartRefresher(context.Background())
	waitForGrants(t, net, 1, "the first refresh")
	expectGrants(t, e, net, 1, "the hold")
	waitForHW(t, e, e.f.Clock.Now(), "the first pass that holds to write the mark") // a write of the loop must not race the test's

	anotherProcessLosesAnAnswer(t, e, srv)
	e.f.Clock.Advance(30 * time.Second) // the hold has 30 minutes to go
	waitForGrants(t, net, 2, "the retry of the marker the other process left")
	eventually(t, "the retry to be answered and stored", func() bool { return e.rawPending() == "" })
	if srv.isRevoked() || !reflect.DeepEqual(srv.presented(), []string{"rt-1", "rt-rotated-1", "rt-rotated-1"}) {
		t.Fatalf("monoes.me was presented %v (revoked %t), want the token the other process presented, once more inside the window", srv.presented(), srv.isRevoked())
	}
	if rt, _ := e.store.LoadRefresh(); rt != "rt-rotated-2" {
		t.Fatalf("refresh.enc holds %q, want the token the repeated answer brought", rt)
	}
}

// The same during a long backoff: the loop that waits 240 s for its next attempt (four failures that
// are settled, so with no marker of its own) is not left waiting out the window.
func TestAMarkerAnotherProcessLeftDuringALongBackoffIsRetriedWithinThirtySecondsAndRecovers(t *testing.T) {
	e, srv, net, g := loopMachine(t, 2*time.Hour)
	net.then(unsent, unsent, unsent, unsent)
	t0 := e.f.Clock.Now()
	g.StartRefresher(context.Background())
	waitForGrants(t, net, 1, "the first attempt")
	expectGrants(t, e, net, 1, "the first failure") // the next is 30 s away
	e.f.Clock.Advance(31 * time.Second)
	waitForGrants(t, net, 2, "the second attempt")
	expectGrants(t, e, net, 2, "the second failure") // 60 s
	e.f.Clock.Advance(61 * time.Second)
	waitForGrants(t, net, 3, "the third attempt")
	expectGrants(t, e, net, 3, "the third failure") // 120 s
	e.f.Clock.Advance(121 * time.Second)
	waitForGrants(t, net, 4, "the fourth attempt")
	expectGrants(t, e, net, 4, "the fourth failure") // 240 s: the next attempt is 4 minutes away
	waitForAttempt(t, e.store, t0.Add(213*time.Second), "the record of the fourth failure, which takes back the marker it wrote")
	if e.rawPending() != "" {
		t.Fatal("settled failures left a marker")
	}

	anotherProcessLosesAnAnswer(t, e, srv)
	e.f.Clock.Advance(30 * time.Second) // 210 s of the backoff to go
	waitForGrants(t, net, 5, "the retry of the marker the other process left")
	eventually(t, "the retry to be answered and stored", func() bool { return e.rawPending() == "" && e.session().LastResult == "ok" })
	if srv.isRevoked() || !reflect.DeepEqual(srv.presented(), []string{"rt-1", "rt-1"}) {
		t.Fatalf("monoes.me was presented %v (revoked %t), want the token the other process presented, once more inside the window", srv.presented(), srv.isRevoked())
	}
	if rt, _ := e.store.LoadRefresh(); rt != "rt-rotated-1" {
		t.Fatalf("refresh.enc holds %q, want the token the repeated answer brought", rt)
	}
}

// (j): the loop's own lost answers end, after the schedule, in the drop: no call at the dropping
// pass, the dead token never presented, the account not revoked. A loop that goes on running after the
// drop finds no refresh token on every pass and must record nothing, so that the grace keeps saying
// unconfirmed (and the end of it says locked(unconfirmed), not expired); a new sign-in replaces it all.
func TestTheRefresherDropsATokenItCouldNotConfirmAndTheGraceKeepsSayingWhy(t *testing.T) {
	e, srv, net, g := loopMachine(t, 2*time.Hour) // in grace; its grace ends 22 hours from now
	net.then(lost, lost, lost, lost)
	g.StartRefresher(context.Background())
	start := e.f.Clock.Now()
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
	e.f.Clock.Advance(241 * time.Second) // +454 s: the fourth retry of the schedule
	eventually(t, "the drop", func() bool { return e.session().LastResult == "unconfirmed" })
	e.underLock(func() {}) // the pass that dropped it has ended, so its guard has taken the drop in
	quiet(t, e.loops...)
	if got := net.grants(); got != 4 || srv.isRevoked() {
		t.Fatalf("%d grants (revoked %t), want the four that were lost and no more: the pass that drops the token sends nothing", got, srv.isRevoked())
	}
	if rt, _ := e.store.LoadRefresh(); rt != "" {
		t.Fatalf("refresh.enc holds %q after the drop", rt)
	}
	if st := g.Status(); st.State != account.StateGrace || st.Reason != account.ReasonUnconfirmed {
		t.Fatalf("Status = %s/%q, want grace/unconfirmed", st.State, st.Reason)
	}

	// The loop goes on: every pass finds no token and records nothing. (While it backs off it
	// keeps the high-water mark current, as it does after any failed attempt: only the mark moves.)
	withoutMark := func(s *account.Session) string {
		c := *s
		c.HW = time.Time{}
		return describe(&c)
	}
	droppedSess := e.session()
	dropped := withoutMark(droppedSess)
	for i := 0; i < 3; i++ {
		e.f.Clock.Advance(6 * time.Minute) // past its backoff
		quiet(t, e.loops...)
	}
	if got := e.session(); withoutMark(got) != dropped || got.HW.Before(droppedSess.HW) {
		t.Fatalf("a pass of the loop changed the session after the drop: %s, was %s", describe(got), describe(droppedSess))
	}
	if st := g.Status(); st.Reason != account.ReasonUnconfirmed {
		t.Fatalf("Status = %s/%q, want the reason to stay unconfirmed", st.State, st.Reason)
	}
	// The end of the grace says why.
	e.f.Clock.Set(start.Add(-2*time.Hour + 24*time.Hour))
	if st := g.Status(); st.State != account.StateLocked || st.Reason != account.ReasonUnconfirmed {
		t.Fatalf("Status once the grace is over = %s/%q, want locked/unconfirmed", st.State, st.Reason)
	}
	// A new sign-in replaces the dropped session.
	e.underLock(func() { e.signIn(10*time.Minute, time.Hour) })
	e.f.Clock.Advance(loopPoll)
	eventually(t, "the sign-in", func() bool { return g.Status().State == account.StateOK })
	if sess := e.session(); sess.LastResult != "ok" || !sess.PendingSince.IsZero() {
		t.Fatalf("stored session = %s, want the login's session", describe(sess))
	}
}

// A stamp that the loop once wrote itself is not remembered for ever: when its marker is gone, a
// marker of another process that happens to carry the very same stamp is a foreign one again, and
// it is not left to the backoff of the loop's own failures. The same stamp takes the same instant:
// the other process's session is written here with it, on a clock that goes on forward (a clock set
// back to that instant would be a step back, which the refresher takes for a clock gone back).
func TestAForeignMarkerWithTheStampOfAnEarlierOwnMarkerIsStillForeign(t *testing.T) {
	ctx := context.Background()
	e, srv, net, g := loopMachine(t, 2*time.Hour) // in grace: the first pass is due
	net.then(lost, lost)
	stamp := e.f.Clock.Now()
	g.StartRefresher(ctx)
	waitForGrants(t, net, 1, "the first attempt") // its answer is lost: the marker, with the stamp of this instant, is the loop's own
	expectGrants(t, e, net, 1, "the first lost answer")
	e.f.Clock.Advance(31 * time.Second)
	waitForGrants(t, net, 2, "the retry")
	expectGrants(t, e, net, 2, "the retry, lost again") // the loop's next attempt is 60 s away, at +91 s

	// Another process retries the marker inside the window and stores the answer: the marker is gone.
	e.underLock(func() {
		ts, err := srv.Refresh(ctx, "rt-1")
		if err != nil {
			t.Fatalf("monoes.me refused the other process: %v", err)
		}
		sess, err := account.NewSession(account.HostURL, ts.AccessToken, &account.User{ID: "user-1"}, e.f.Clock.Now())
		if err != nil {
			t.Fatal(err)
		}
		// Its mark a minute old, so that the loop's next pass writes it: that write shows the
		// pass that read this session, with no marker.
		sess.HW = sess.HW.Add(-time.Minute)
		e.save(sess)
		if err := e.store.SaveRefresh(ts.RefreshToken); err != nil {
			t.Fatal(err)
		}
	})
	e.f.Clock.Advance(time.Second)
	waitForHW(t, e, e.f.Clock.Now(), "the loop's pass over the session that has no marker")
	if net.grants() != 2 || e.rawPending() != "" {
		t.Fatalf("%d grants with pending %q, want two and no marker", net.grants(), e.rawPending())
	}

	// Another process then loses an answer, and its session carries the stamp the loop once wrote.
	e.underLock(func() {
		rt, err := e.store.LoadRefresh()
		if err != nil {
			t.Fatal(err)
		}
		if _, err := srv.Refresh(ctx, rt); err != nil { // monoes.me rotates, the answer is lost
			t.Fatalf("monoes.me refused the other process: %v", err)
		}
		sess := e.session()
		sess.PendingSince, sess.LastAttempt, sess.LastResult = stamp, e.f.Clock.Now(), "unreachable"
		e.save(sess)
	})
	e.f.Clock.Advance(time.Second) // the loop's next poll reads it; its own next attempt is 58 s away
	waitForGrants(t, net, 3, "the retry of the marker the other process left, at once and not after the loop's backoff of 60 s")
	eventually(t, "the retry to be answered and stored", func() bool { return e.rawPending() == "" && e.session().LastResult == "ok" })
	if srv.isRevoked() {
		t.Fatal("the account was revoked")
	}
}

// A marker that another process left during the hold, whose first retry by the loop is lost
// again, stays on the schedule: the hold of the last refresh does not come back because the
// loop now follows that marker, and the next retry, 30 s later, recovers inside the window.
func TestAForeignMarkerWhoseFirstRetryIsLostAgainIsNotLeftToTheHold(t *testing.T) {
	e, srv, net, g := loopMachine(t, 40*time.Minute) // past its half-life: the first pass refreshes
	g.StartRefresher(context.Background())
	waitForGrants(t, net, 1, "the first refresh")
	expectGrants(t, e, net, 1, "the hold")
	waitForHW(t, e, e.f.Clock.Now(), "the first pass that holds to write the mark")

	anotherProcessLosesAnAnswer(t, e, srv)
	net.then(lost)
	e.f.Clock.Advance(30 * time.Second) // the hold has 30 minutes to go
	waitForGrants(t, net, 2, "the first retry of the marker the other process left")
	expectGrants(t, e, net, 2, "the first retry, lost again")
	e.f.Clock.Advance(31 * time.Second) // +61 s from the other process's send
	waitForGrants(t, net, 3, "the second retry, 30 s after the first")
	eventually(t, "the second retry to be answered and stored", func() bool { return e.rawPending() == "" && e.session().LastResult == "ok" })
	if srv.isRevoked() || !reflect.DeepEqual(srv.presented(), []string{"rt-1", "rt-rotated-1", "rt-rotated-1", "rt-rotated-1"}) {
		t.Fatalf("monoes.me was presented %v (revoked %t), want the other process's token twice more, inside the window", srv.presented(), srv.isRevoked())
	}
}

// The same during a long backoff: the loop has backed off to 240 s after four settled failures,
// and its first retry of a marker another process left is lost again. The next retry is 30 s
// away, not the five minutes the backoff would double to, by which time the token is dropped.
func TestAForeignMarkerWhoseFirstRetryIsLostAgainIsNotLeftToALongBackoff(t *testing.T) {
	e, srv, net, g := loopMachine(t, 2*time.Hour)
	net.then(unsent, unsent, unsent, unsent)
	g.StartRefresher(context.Background())
	for i, step := range []time.Duration{0, 31 * time.Second, 61 * time.Second, 121 * time.Second} {
		e.f.Clock.Advance(step)
		waitForGrants(t, net, i+1, "a settled failure")
		expectGrants(t, e, net, i+1, "the backoff after it")
	}

	anotherProcessLosesAnAnswer(t, e, srv)
	net.then(lost)
	e.f.Clock.Advance(30 * time.Second) // 210 s of the backoff to go
	waitForGrants(t, net, 5, "the first retry of the marker the other process left")
	expectGrants(t, e, net, 5, "the first retry, lost again")
	e.f.Clock.Advance(31 * time.Second) // +61 s from the other process's send
	waitForGrants(t, net, 6, "the second retry, 30 s after the first")
	eventually(t, "the second retry to be answered and stored", func() bool { return e.rawPending() == "" && e.session().LastResult == "ok" })
	if srv.isRevoked() || !reflect.DeepEqual(srv.presented(), []string{"rt-1", "rt-1", "rt-1"}) {
		t.Fatalf("monoes.me was presented %v (revoked %t), want the other process's token twice more, inside the window", srv.presented(), srv.isRevoked())
	}
}

// The loop's own first lost answer after a run of settled failures starts the schedule again:
// its retries come at +30, +90 and +210 s from the send that may have rotated the token, not
// at the 240 s that the backoff of the settled failures would double to.
func TestTheRefreshersOwnLostAnswerAfterSettledFailuresIsRetriedFromThirtySeconds(t *testing.T) {
	e, srv, net, g := loopMachine(t, 2*time.Hour)
	net.then(unsent, unsent, unsent, lost)
	g.StartRefresher(context.Background())
	for i, step := range []time.Duration{0, 31 * time.Second, 61 * time.Second, 121 * time.Second} {
		e.f.Clock.Advance(step)
		waitForGrants(t, net, i+1, "an attempt")
		expectGrants(t, e, net, i+1, "the backoff after it")
	}
	if e.rawPending() == "" {
		t.Fatal("the lost answer left no marker")
	}
	e.f.Clock.Advance(31 * time.Second)
	waitForGrants(t, net, 5, "the first retry of the loop's own marker, 30 s after its send")
	eventually(t, "the retry to be answered and stored", func() bool { return e.rawPending() == "" && e.session().LastResult == "ok" })
	if srv.isRevoked() || !reflect.DeepEqual(srv.presented(), []string{"rt-1", "rt-1"}) {
		t.Fatalf("monoes.me was presented %v (revoked %t), want the lost grant and its retry, inside the window", srv.presented(), srv.isRevoked())
	}
}

// After a drop has completed (no refresh token, unconfirmed, no marker) the refresher has
// nothing to try until a sign-in, and it backs off as it does after any failed attempt: a pass
// at 0, 30, 90, 210 and 450 s in ten minutes, not one at every wake-up, each taking the session
// lock and reading the session and the refresh token. Each records nothing.
func TestTheRefresherBacksOffAfterACompletedDrop(t *testing.T) {
	e := newEnv(t)
	sess := e.signIn(2*time.Hour, time.Hour) // in grace: every pass of the refresher is due
	sess.LastResult = "unconfirmed"
	e.save(sess)
	if err := e.store.DeleteRefresh(); err != nil {
		t.Fatal(err)
	}
	cs := newCountingStore(account.OpenStore(e.dir, e.seal))
	var read atomic.Int64 // the refresher's last reading of the clock
	now := func() time.Time {
		at := e.f.Clock.Now()
		read.Store(at.UnixNano())
		return at
	}
	g := account.NewGuard(account.GuardOptions{Store: cs, Refresher: e.ref, Now: now, Poll: loopPoll})
	t.Cleanup(g.Close)
	t0 := e.f.Clock.Now()
	g.StartRefresher(context.Background())
	for i := 0; i < 120; i++ { // ten minutes, five seconds at a time, each instant read by the refresher before the next
		e.f.Clock.Advance(5 * time.Second)
		at := e.f.Clock.Now()
		eventually(t, "the refresher to read the clock at +"+at.Sub(t0).String(), func() bool { return read.Load() == at.UnixNano() })
	}
	if n := cs.calls("LoadRefresh"); n < 4 || n > 6 {
		t.Fatalf("%d passes read the refresh token in ten minutes, want the five of the backoff (30 s doubling to 5 min)", n)
	}
	if n := cs.calls("Lock"); n > 17 {
		t.Fatalf("%d locks in ten minutes, want at most the five passes and one high-water write a minute", n)
	}
	if got := e.session(); got.LastResult != "unconfirmed" || !got.LastAttempt.Equal(sess.LastAttempt) || e.ref.calls.Load() != 0 {
		t.Fatalf("stored session = %s with %d network refreshes, want unconfirmed, the last attempt as it was, and none", describe(got), e.ref.calls.Load())
	}
}

// A running refresher keeps the evidence of a clock set back fresh. Its own lost answers record an
// attempt only at the retries (+31, +92 and +213 s); between them it writes the high-water mark,
// which, while the marker is pending, carries the last attempt up to the clock. A clock then set
// back 110 s at +340 s reads +230 s: the marker looks 230 s old, inside the window, but the clock
// reads before the last attempt the refresher wrote, so the next command drops the token instead of
// presenting it 340 s after monoes.me rotated it. monoes.me keeps its own clock here.
func TestARunningRefresherKeepsTheEvidenceOfAClockSetBackFresh(t *testing.T) {
	f := accounttest.New(t) // f.Clock is monoes.me's clock
	dir := filepath.Join(t.TempDir(), "account")
	seal := account.NewMemorySealer()
	store := account.OpenStore(dir, seal)
	signInInstall(t, f, store, 57*time.Minute, time.Hour, "rt-1") // past its half-life: the refresher is due at once
	srv := newWindowServer(f, 0, "rt-1")
	net := &flakyNet{srv: srv}
	net.then(lost, lost, lost, lost)
	machine := accounttest.NewClock(f.Clock.Now()) // this machine's clock
	m0 := machine.Now()
	both := func(d time.Duration) { f.Clock.Advance(d); machine.Advance(d) }
	mark := func(want time.Duration, what string) {
		t.Helper()
		eventually(t, what, func() bool {
			sess, err := store.Load()
			return err == nil && sess != nil && sess.HW.Equal(m0.Add(want))
		})
	}
	daemon := account.NewGuard(account.GuardOptions{Store: account.OpenStore(dir, seal), Refresher: net, Now: machine.Now, Poll: loopPoll})
	t.Cleanup(daemon.Close)
	daemon.StartRefresher(context.Background())
	waitForGrants(t, net, 1, "the first attempt")
	for _, step := range []struct {
		advance time.Duration
		grants  int
	}{{31 * time.Second, 2}, {61 * time.Second, 3}, {121 * time.Second, 4}} { // +31 s, +92 s, +213 s: all lost
		both(step.advance)
		waitForGrants(t, net, step.grants, "the retry")
	}
	mark(213*time.Second, "the record of the third retry")
	both(61 * time.Second) // +274 s: the refresher waits for +453 s and writes the mark
	mark(274*time.Second, "the high-water write at +274 s")
	both(61 * time.Second) // +335 s
	mark(335*time.Second, "the high-water write at +335 s")
	both(5 * time.Second) // +340 s

	machine.Set(m0.Add(230 * time.Second)) // set back 110 s: the marker looks 230 s old
	cli := account.NewGuard(account.GuardOptions{Store: account.OpenStore(dir, seal), Refresher: srv, Now: machine.Now})
	t.Cleanup(cli.Close)
	if _, err := cli.EnsureFresh(context.Background()); err != nil {
		t.Fatalf("the command: %v", err)
	}
	if got := count(srv.presented(), "rt-1"); got != 4 || srv.isRevoked() {
		t.Fatalf("monoes.me was presented rt-1 %d times (revoked %t), want the refresher's four and none from the command: %v", got, srv.isRevoked(), srv.presented())
	}
	if sess, err := store.Load(); err != nil || sess.LastResult != "unconfirmed" {
		t.Fatalf("stored session = %s (%v), want the token dropped as unconfirmed", describe(sess), err)
	}
	if rt, _ := store.LoadRefresh(); rt != "" {
		t.Fatalf("refresh.enc holds %q, want the dead token gone", rt)
	}
}
