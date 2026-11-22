package account_test

import (
	"context"
	"testing"
	"time"

	"github.com/monoes/mono-agent/internal/account"
)

// loopPoll is the refresher's tick in these tests. Everything else, the
// half-life and the backoff, is judged by the fixture clock, which the test moves.
const loopPoll = 5 * time.Millisecond

func TestTheRefresherRefreshesAtHalfTheTokenLifetime(t *testing.T) {
	e := newEnv(t)
	e.signIn(20*time.Minute, time.Hour) // half-life is ten minutes from now
	g := e.newGuard(loopPoll)
	g.StartRefresher(context.Background())
	settle(t, e.loops...)
	if n := e.ref.calls.Load(); n != 0 {
		t.Fatalf("%d refreshes before the half-life", n)
	}
	e.f.Clock.Advance(10 * time.Minute)
	eventually(t, "the refresh at half-life", func() bool { return e.ref.calls.Load() == 1 })
	eventually(t, "the new token", func() bool { return g.Status().ValidUntil.Equal(e.f.Clock.Now().Add(time.Hour)) })
	settle(t, e.loops...)
	if n := e.ref.calls.Load(); n != 1 {
		t.Fatalf("%d refreshes, want 1: a fresh token is not due again", n)
	}
}

func TestTheRefresherBacksOffFrom30SecondsToFiveMinutes(t *testing.T) {
	e := newEnv(t)
	e.signIn(2*time.Hour, time.Hour) // expired: every pass is due
	e.ref.set(func(r *fakeRefresher) { r.err = transient(account.ReasonUnreachable) })
	g := e.newGuard(loopPoll)
	g.StartRefresher(context.Background())
	eventually(t, "the first attempt", func() bool { return e.ref.calls.Load() == 1 })
	settle(t, e.loops...) // let the loop record the failure and schedule the retry before the clock moves

	calls := int32(1)
	for _, gap := range []time.Duration{30 * time.Second, time.Minute, 2 * time.Minute, 4 * time.Minute, 5 * time.Minute, 5 * time.Minute} {
		e.f.Clock.Advance(gap - time.Second)
		settle(t, e.loops...)
		if n := e.ref.calls.Load(); n != calls {
			t.Fatalf("a retry came before the %v backoff ended: %d calls, want %d", gap, n, calls)
		}
		e.f.Clock.Advance(2 * time.Second)
		calls++
		want := calls
		eventually(t, "the retry after "+gap.String(), func() bool { return e.ref.calls.Load() == want })
		settle(t, e.loops...)
	}
	if st := g.Status(); st.State != account.StateGrace || st.Reason != account.ReasonUnreachable {
		t.Fatalf("Status = %s/%q, want grace/unreachable", st.State, st.Reason)
	}
}

func TestTheRefresherPicksUpASignInFromAnotherProcess(t *testing.T) {
	e := newEnv(t)
	g := e.newGuard(loopPoll)
	g.StartRefresher(context.Background())
	// The first pass keeps the record of a machine with no session (A25), and not again for a
	// minute: wait for it, so that it cannot write over the sign-in below.
	waitForHW(t, e, e.f.Clock.Now(), "the record of a machine with no session")
	if st := g.Status(); st.Reason != account.ReasonNotLoggedIn {
		t.Fatalf("Status = %s/%q", st.State, st.Reason)
	}
	e.signIn(10*time.Minute, time.Hour) // the CLI signs in
	settle(t, e.loops...)               // passes that began before the sign-in have ended, so the advance is seen by one that began after
	e.f.Clock.Advance(loopPoll)
	eventually(t, "the sign-in", func() bool { return g.Status().State == account.StateOK })
}

func TestStartRefresherIsIdempotent(t *testing.T) {
	e := newEnv(t)
	e.signIn(2*time.Hour, time.Hour)
	e.ref.set(func(r *fakeRefresher) { r.err = transient(account.ReasonUnreachable) })
	g := e.newGuard(loopPoll)
	for i := 0; i < 3; i++ {
		g.StartRefresher(context.Background())
	}
	eventually(t, "the first attempt", func() bool { return e.ref.calls.Load() >= 1 })
	settle(t, e.loops...)
	if n := e.ref.calls.Load(); n != 1 {
		t.Fatalf("%d attempts, want 1: three StartRefresher calls must make one loop", n)
	}
}

func TestTheRefresherDoesNothingWhileDormant(t *testing.T) {
	e := newEnv(t)
	e.signIn(2*time.Hour, time.Hour)
	account.SetEnforceFromForTest(t, time.Time{})
	g := e.newGuard(loopPoll)
	g.StartRefresher(context.Background())
	settle(t, e.loops...)
	if n := e.ref.calls.Load(); n != 0 {
		t.Fatalf("%d refreshes while dormant: no background refresher may run", n)
	}
}

func TestTheRefresherEndsWithItsContext(t *testing.T) {
	e := newEnv(t)
	e.signIn(20*time.Minute, time.Hour)
	g := e.newGuard(loopPoll)
	ctx, cancel := context.WithCancel(context.Background())
	g.StartRefresher(ctx)
	cancel()
	settle(t, e.loops...)
	e.f.Clock.Advance(time.Hour)
	settle(t, e.loops...)
	if n := e.ref.calls.Load(); n != 0 {
		t.Fatalf("%d refreshes after the context ended", n)
	}
}

// A grant the server may already have rotated is never abandoned (A20): Close
// waits for the call in flight and the answer is stored, so the token the server
// now expects is on disk and no later attempt presents the dead one.
func TestCloseWaitsForAGrantInFlightAndStoresItsAnswer(t *testing.T) {
	e := newEnv(t)
	e.signIn(2*time.Hour, time.Hour)
	release := make(chan struct{})
	e.ref.set(func(r *fakeRefresher) { r.hold = release })
	g := e.newGuard(loopPoll)
	g.StartRefresher(context.Background())
	eventually(t, "the attempt to start", func() bool { return e.ref.calls.Load() == 1 })

	done := make(chan struct{})
	go func() {
		defer close(done)
		g.Close()
		g.Close() // safe twice
	}()
	select {
	case <-done:
		t.Fatal("Close returned while a refresh grant was in flight: the server may have rotated the token and its answer would be lost")
	case <-time.After(300 * time.Millisecond):
	}
	close(release)
	select {
	case <-done:
	case <-time.After(testWait):
		t.Fatal("Close did not return once the grant was answered")
	}
	if rt, err := e.store.LoadRefresh(); err != nil || rt != "rt-2" {
		t.Fatalf("stored refresh token = %q (%v), want rt-2: the answer of a grant in flight must be stored", rt, err)
	}
	if got := e.session().LastResult; got != "ok" {
		t.Fatalf("LastResult = %q, want ok", got)
	}
	g.StartRefresher(context.Background()) // a closed guard starts nothing
	settle(t, e.loops...)
	if n := e.ref.calls.Load(); n != 1 {
		t.Fatalf("%d calls: a closed guard started a loop", n)
	}
	if st := g.Status(); st.State != account.StateOK {
		t.Fatalf("a closed guard still answers Status, and the stored answer makes it ok: %s", st.State)
	}
}

func TestTheRefresherTellsOnRefusedWhenItsOwnRefreshIsRefused(t *testing.T) {
	e := newEnv(t)
	e.signIn(2*time.Hour, time.Hour)
	e.ref.set(func(r *fakeRefresher) { r.err = &account.RefusedError{Description: "blocked"} })
	g := e.newGuard(loopPoll)
	got := make(chan account.Status, 4)
	g.OnRefused(func(st account.Status) { got <- st })
	g.StartRefresher(context.Background())
	select {
	case st := <-got:
		if st.Reason != account.ReasonRefused {
			t.Fatalf("callback got %s/%q", st.State, st.Reason)
		}
	case <-time.After(testWait):
		t.Fatal("OnRefused did not fire")
	}
	e.f.Clock.Advance(time.Hour)
	settle(t, e.loops...)
	if n := e.ref.calls.Load(); n != 1 || len(got) != 0 {
		t.Fatalf("%d calls and %d extra callbacks after a refusal, want 1 and 0", n, len(got))
	}
}

func TestTheRefresherNoticesARefusalFromAnotherProcess(t *testing.T) {
	e := newEnv(t)
	e.signIn(10*time.Minute, time.Hour)
	g := e.newGuard(loopPoll)
	got := make(chan account.Status, 4)
	g.OnRefused(func(st account.Status) { got <- st })
	g.StartRefresher(context.Background())
	settle(t, e.loops...)
	e.storeRefusal() // under the lock, as another process must: the loop's first pass writes the stale mark under it
	e.f.Clock.Advance(loopPoll)
	select {
	case <-got:
	case <-time.After(testWait):
		t.Fatal("the refresher did not notice another process's refusal")
	}
}
