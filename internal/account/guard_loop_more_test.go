package account_test

import (
	"context"
	"sync/atomic"
	"testing"
	"time"

	"github.com/monoes/mono-agent/internal/account"
)

// These tests pin the refresher's properties that guard_loop_test.go leaves
// open: its lifecycle (start, start again, close, cancel), what it does with no
// Refresher, with several guards and with a callback that closes it, and how
// often it wakes. Most came from a mutant of guard_loop.go that the brief's tests
// let through. They wait on real time in three ways: quiet(t, e.loops...) (150 ms, that
// something does NOT happen), the 3 second limits of eventually and of the
// channel receives (that something MUST happen), and the one 500 ms sleep of
// TestTheRefresherWakesOncePerPoll.

func TestCloseStopsTheRefresherAfterSeveralStartRefresherCalls(t *testing.T) {
	e := newEnv(t)
	e.signIn(20*time.Minute, time.Hour) // half-life in ten minutes
	g := e.newGuard(loopPoll)
	for i := 0; i < 3; i++ {
		g.StartRefresher(context.Background())
	}
	g.Close()
	e.f.Clock.Advance(10 * time.Minute)
	quiet(t, e.loops...)
	if n := e.ref.calls.Load(); n != 0 {
		t.Fatalf("%d refreshes after Close: a repeated StartRefresher left a loop that Close does not stop", n)
	}
}

func TestAGuardClosedBeforeItStartedStartsNoRefresher(t *testing.T) {
	e := newEnv(t)
	e.signIn(2*time.Hour, time.Hour)
	g := e.newGuard(loopPoll)
	g.Close()
	g.StartRefresher(context.Background())
	quiet(t, e.loops...)
	if n := e.ref.calls.Load(); n != 0 {
		t.Fatalf("%d refreshes: a guard that was closed before it started a refresher started one", n)
	}
}

func TestStartRefresherWhileDormantIsNotRemembered(t *testing.T) {
	e := newEnv(t)
	e.signIn(2*time.Hour, time.Hour)
	g := e.newGuard(loopPoll)
	account.SetEnforceFromForTest(t, time.Time{}) // dormant
	g.StartRefresher(context.Background())
	quiet(t, e.loops...)
	if n := e.ref.calls.Load(); n != 0 {
		t.Fatalf("%d refreshes while dormant", n)
	}
	account.SetEnforceFromForTest(t, e.f.Clock.Now().Add(-24*time.Hour)) // the date has come
	g.StartRefresher(context.Background())
	eventually(t, "the refresher a later start made", func() bool { return e.ref.calls.Load() == 1 })
}

func TestCloseDoesNotWaitForTheNextTick(t *testing.T) {
	e := newEnv(t)
	e.signIn(20*time.Minute, time.Hour)
	g := e.guardOn(e.ref, time.Hour) // the next tick is an hour away
	g.StartRefresher(context.Background())
	waitForFirstPass(t, g) // the first pass is over and the loop waits for its tick
	done := make(chan struct{})
	go func() {
		defer close(done)
		g.Close()
	}()
	select {
	case <-done:
	case <-time.After(testWait):
		t.Fatal("Close waited for the refresher's next tick")
	}
}

// The context of the refresher ends the loop, never the grant it has sent (A20):
// the loop ends once the call in flight is answered and stored.
func TestCancellingTheContextWaitsForAGrantInFlightAndStoresItsAnswer(t *testing.T) {
	e := newEnv(t)
	e.signIn(2*time.Hour, time.Hour)
	release := make(chan struct{})
	e.ref.set(func(r *fakeRefresher) { r.hold = release })
	g := e.newGuard(loopPoll)
	ctx, cancel := context.WithCancel(context.Background())
	g.StartRefresher(ctx)
	eventually(t, "the attempt to start", func() bool { return e.ref.calls.Load() == 1 })

	cancel()
	done := make(chan struct{})
	go func() {
		defer close(done)
		g.Close() // returns only once the loop has ended
	}()
	select {
	case <-done:
		t.Fatal("the loop ended while a refresh grant was in flight: the server may have rotated the token and its answer would be lost")
	case <-time.After(300 * time.Millisecond):
	}
	close(release)
	select {
	case <-done:
	case <-time.After(testWait):
		t.Fatal("the refresher did not end once the grant was answered")
	}
	if rt, err := e.store.LoadRefresh(); err != nil || rt != "rt-2" {
		t.Fatalf("stored refresh token = %q (%v), want rt-2: the answer of a grant in flight must be stored", rt, err)
	}
	if got := e.session().LastResult; got != "ok" {
		t.Fatalf("LastResult = %q, want ok", got)
	}
	settle(t, e.loops...)
	if n := e.ref.calls.Load(); n != 1 {
		t.Fatalf("%d calls: the loop went on after its context ended", n)
	}
}

func TestTheFirstPassRunsAtOnce(t *testing.T) {
	e := newEnv(t)
	e.signIn(2*time.Hour, time.Hour)
	g := e.guardOn(e.ref, time.Hour) // the first tick is an hour away
	g.StartRefresher(context.Background())
	eventually(t, "the first attempt, an hour before the first tick", func() bool { return e.ref.calls.Load() == 1 })
}

func TestARefresherWithoutARefresherOnlyFollowsTheFile(t *testing.T) {
	e := newEnv(t)
	before := e.signIn(2*time.Hour, time.Hour) // expired: a refresher would try at once
	g := account.NewGuard(account.GuardOptions{Store: account.OpenStore(e.dir, e.seal), Now: e.f.Clock.Now, Poll: loopPoll})
	t.Cleanup(g.Close)
	e.loops = append(e.loops, g)
	got := make(chan account.Status, 4)
	g.OnRefused(func(st account.Status) { got <- st })
	g.StartRefresher(context.Background())
	waitForHW(t, e, e.f.Clock.Now(), "the first pass to write the stale mark") // what the loop does, and all it does
	quiet(t, e.loops...)
	if after := e.session(); after.LastResult != "ok" || !after.LastAttempt.Equal(before.LastAttempt) {
		t.Fatalf("LastResult = %q, LastAttempt = %v: a guard with no Refresher attempted something", after.LastResult, after.LastAttempt)
	}
	e.storeRefusal()
	e.f.Clock.Advance(loopPoll)
	select {
	case <-got:
	case <-time.After(testWait):
		t.Fatal("a refresher with nothing to refresh with did not notice another process's refusal")
	}
}

func TestTheRefresherRefreshesASessionAnotherProcessSignedIn(t *testing.T) {
	// The test never calls Status: if the session is picked up, the refresher
	// did it, and it shows by the refresh it makes.
	e := newEnv(t)
	srv := newLoopServer(e)
	g := e.guardOn(srv, loopPoll)
	g.StartRefresher(context.Background())
	expectCalls(t, srv, 0, "with no session") // the refresher has nothing to do and goes on waiting
	srv.signIn(2*time.Hour, time.Hour)        // the CLI signs in, with a token that is due
	e.f.Clock.Advance(loopPoll)
	waitForCalls(t, srv, 1, "the refresh of the session another process signed in")
}

func TestSeveralRefreshersOverOneSessionMakeOneNetworkCall(t *testing.T) {
	e := newEnv(t)
	e.signIn(2*time.Hour, time.Hour)                                      // expired: the first pass of every loop is due
	e.ref.set(func(r *fakeRefresher) { r.delay = 20 * time.Millisecond }) // the calls would overlap
	guards := make([]*account.Guard, 5)
	for i := range guards {
		guards[i] = e.newGuard(loopPoll)
	}
	for _, g := range guards {
		g.StartRefresher(context.Background())
	}
	eventually(t, "the refresh", func() bool { return e.ref.calls.Load() >= 1 })
	quiet(t, e.loops...)
	if n := e.ref.calls.Load(); n != 1 {
		t.Fatalf("%d network calls from five refreshers, want 1: the server rotates the refresh token, so a second call is a lockout", n)
	}
	e.f.Clock.Advance(loopPoll) // every guard looks at the file again
	eventually(t, "every guard to see the new token and none to be locked", func() bool {
		for _, g := range guards {
			if g.Status().State != account.StateOK {
				return false
			}
		}
		return true
	})
}

func TestAnOnRefusedCallbackMayCloseTheGuard(t *testing.T) {
	e := newEnv(t)
	e.signIn(10*time.Minute, time.Hour)
	g := e.newGuard(loopPoll)
	closed := make(chan struct{}, 4)
	g.OnRefused(func(account.Status) {
		g.Close() // waits for the loop: the loop must not be waiting for this callback
		closed <- struct{}{}
	})
	g.StartRefresher(context.Background())
	waitForHW(t, e, e.f.Clock.Now(), "the first pass to write the stale mark") // a write of the loop must not race the test's
	e.storeRefusal()
	e.f.Clock.Advance(loopPoll)
	select {
	case <-closed:
	case <-time.After(testWait):
		t.Fatal("a callback that closes the guard did not return: the refresher and the callback wait for each other")
	}
}

func TestTheRefresherWakesOncePerPoll(t *testing.T) {
	e := newEnv(t) // no session: every pass only looks at the file
	store := newCountingStore(account.OpenStore(e.dir, e.seal))
	var readings atomic.Int64
	// A clock that moves on every reading, so that every Status looks at the
	// file again: the number of looks is then the number of passes, three times
	// (the loop's own Status, and the two of the pass that finds nothing to do).
	now := func() time.Time { return e.f.Clock.Now().Add(time.Duration(readings.Add(1)) * time.Second) }
	const poll = 50 * time.Millisecond
	g := account.NewGuard(account.GuardOptions{Store: store, Now: now, Poll: poll})
	t.Cleanup(g.Close)
	start := time.Now()
	g.StartRefresher(context.Background())
	// It wakes: two passes, with three looks each, however slow the machine is.
	eventually(t, "six looks at session.json", func() bool { return store.calls("Mtime") >= 6 })
	// And it sleeps between passes: one that did not would make thousands of looks
	// in the time this waits.
	time.Sleep(10 * poll)
	passes := int32(time.Since(start)/poll) + 1 // a pass at once, then one per tick
	if n, most := int32(store.calls("Mtime")), 6*passes; n > most {
		t.Fatalf("%d looks at session.json in %d polls: three per pass is %d and no more than %d is allowed; a refresher that does not sleep makes far more", n, passes, 3*passes, most)
	}
}
