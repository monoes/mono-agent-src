package account_test

import (
	"context"
	"testing"
	"time"

	"github.com/monoes/mono-agent/internal/account"
)

// These tests are about the refresher's hold: monoes.me rotates the refresh
// token on every use, so the refresher must never refresh more than once per
// half-life of the token it has just obtained, even when the local clock runs so
// far ahead of monoes.me that the new token already looks past its half-life.
// The tests move the fixture clock. Real time is only the refresher's tick
// (loopPoll), the quiet(t, e.loops...) waits for what must not happen and the eventually waits
// (3 seconds at most) for what must.

func TestTheRefresherRefreshesOncePerHalfLifeWhenTheLocalClockRunsAhead(t *testing.T) {
	// monoes.me issues a token that lives an hour. By a clock that is lag ahead of
	// monoes.me the token it has just issued is lag old: past its half-life
	// (30 minutes) from the start, and from 60 minutes on already expired.
	for _, lag := range []time.Duration{31 * time.Minute, 61 * time.Minute} {
		t.Run(lag.String(), func(t *testing.T) {
			e := newEnv(t)
			srv := newLoopServer(e)
			srv.set(func(s *loopServer) { s.lag = lag })
			srv.signIn(40*time.Minute, time.Hour) // past its half-life: the first pass refreshes
			g := e.guardOn(srv, loopPoll)
			g.StartRefresher(context.Background())

			waitForCalls(t, srv, 1, "the first refresh")
			expectCalls(t, srv, 1, "the passes after a refresh must not refresh again: a token that looks old to this clock is not a reason")
			for cycle := int32(2); cycle <= 3; cycle++ {
				e.f.Clock.Advance(30*time.Minute - time.Second)
				expectCalls(t, srv, cycle-1, "a second before the half-life is over")
				e.f.Clock.Advance(2 * time.Second)
				waitForCalls(t, srv, cycle, "the refresh after the half-life")
				expectCalls(t, srv, cycle, "once per half-life")
			}
		})
	}
}

func TestTheRefresherKeepsItsScheduleOnACorrectClock(t *testing.T) {
	e := newEnv(t)
	srv := newLoopServer(e) // no lag: monoes.me and the local clock agree
	srv.signIn(40*time.Minute, time.Hour)
	g := e.guardOn(srv, loopPoll)
	g.StartRefresher(context.Background())

	waitForCalls(t, srv, 1, "the first refresh")
	expectCalls(t, srv, 1, "the first refresh, and no more")
	for n := int32(2); n <= 4; n++ {
		e.f.Clock.Advance(30*time.Minute - time.Second)
		expectCalls(t, srv, n-1, "a second before the half-life of the last token")
		e.f.Clock.Advance(time.Second) // the half-life, to the second
		waitForCalls(t, srv, n, "the refresh at the half-life")
		expectCalls(t, srv, n, "once per half-life")
	}
}

func TestAClockSetBackEndsTheHoldSoTheRefresherCanRepairIt(t *testing.T) {
	// The server's clock is 61 minutes behind this one, so every token it issues is
	// due for a refresh by this clock at once. A clock that is set back after the
	// refresh must not leave the refresher waiting for the clock to come round
	// to the hold's end again: a clock that went back is what a refresh repairs.
	for _, setBack := range []time.Duration{time.Second, 10 * time.Minute, 30 * time.Minute} {
		t.Run(setBack.String(), func(t *testing.T) {
			e := newEnv(t)
			srv := newLoopServer(e)
			srv.set(func(s *loopServer) { s.lag = 61 * time.Minute })
			srv.signIn(40*time.Minute, time.Hour)
			g := e.guardOn(srv, loopPoll)
			g.StartRefresher(context.Background())
			waitForCalls(t, srv, 1, "the first refresh")
			expectCalls(t, srv, 1, "the hold")

			e.f.Clock.Advance(-setBack)
			waitForCalls(t, srv, 2, "a refresh after the clock was set back")
			expectCalls(t, srv, 2, "the hold the second refresh began")
		})
	}
}

func TestAClockSetBackThatLocksTheGuardIsRepairedByTheRefresherDespiteItsHold(t *testing.T) {
	// The refresh stores hw at the new token's iat, T, and the refresher then holds
	// for half an hour without doing anything that moves hw. A clock set back 10
	// minutes puts the guard before hw minus 5 minutes: locked(clock_rollback).
	// Only a refresh repairs that, and the hold must not stand in its way. The
	// clock does not move again here, so a refresher that waited out the hold
	// would leave the guard locked for good.
	e := newEnv(t)
	srv := newLoopServer(e) // no lag
	srv.signIn(40*time.Minute, time.Hour)
	g := e.guardOn(srv, loopPoll)
	g.StartRefresher(context.Background())
	waitForCalls(t, srv, 1, "the first refresh")
	expectCalls(t, srv, 1, "the hold")
	waitForHW(t, e, e.f.Clock.Now(), "the refresh to store the new token, whose iat is the mark")

	back := e.f.Clock.Now().Add(-10 * time.Minute)
	if st := account.Evaluate(e.session(), back); st.State != account.StateLocked || st.Reason != account.ReasonClockRollback {
		t.Fatalf("the stored session at the set-back time is %s/%q, want locked/clock_rollback: the test sets nothing up", st.State, st.Reason)
	}
	e.f.Clock.Set(back)
	eventually(t, "the refresh that repairs the clock rollback", func() bool { return g.Status().State == account.StateOK })
}

func TestTheHoldIsCountedFromTheStartOfTheRefresh(t *testing.T) {
	// Like the backoff, the hold does not depend on how long the call took. The
	// call takes 20 seconds; the half-life is counted from where it began.
	e := newEnv(t)
	srv := newLoopServer(e)
	srv.set(func(s *loopServer) { s.lag, s.takes = 61*time.Minute, 20*time.Second })
	srv.signIn(40*time.Minute, time.Hour)
	g := e.guardOn(srv, loopPoll)
	g.StartRefresher(context.Background())
	waitForCalls(t, srv, 1, "the first refresh")
	expectCalls(t, srv, 1, "the hold")

	// The clock stands 20 seconds after the start of the pass: 30 minutes after the
	// start is 30 minutes minus 20 seconds from now.
	e.f.Clock.Advance(30*time.Minute - 21*time.Second)
	expectCalls(t, srv, 1, "a second before the half-life, counted from the start")
	e.f.Clock.Advance(2 * time.Second)
	waitForCalls(t, srv, 2, "the refresh at the half-life, counted from the start")
}

func TestTheRefresherStillFollowsTheFileWhileItHolds(t *testing.T) {
	e := newEnv(t)
	srv := newLoopServer(e)
	srv.set(func(s *loopServer) { s.lag = 31 * time.Minute })
	srv.signIn(40*time.Minute, time.Hour)
	g := e.guardOn(srv, loopPoll)
	got := make(chan account.Status, 4)
	g.OnRefused(func(st account.Status) { got <- st })
	g.StartRefresher(context.Background())
	waitForCalls(t, srv, 1, "the first refresh")
	expectCalls(t, srv, 1, "the hold")
	// The first pass that holds writes the stale mark. Wait for it: a write of the
	// loop that was still going on would race the test's own write of the session.
	waitForHW(t, e, e.f.Clock.Now(), "the first pass that holds to write the mark")

	e.storeRefusal() // another process was refused
	e.f.Clock.Advance(loopPoll)
	select {
	case st := <-got:
		if st.Reason != account.ReasonRefused {
			t.Fatalf("callback got %s/%q", st.State, st.Reason)
		}
	case <-time.After(testWait):
		t.Fatal("a refresher that holds did not notice another process's refusal")
	}
	if n := srv.calls.Load(); n != 1 {
		t.Fatalf("%d calls: a refused session is not refreshed", n)
	}
}

func TestARefusalStartsNoHold(t *testing.T) {
	e := newEnv(t)
	srv := newLoopServer(e)
	srv.set(func(s *loopServer) { s.err = &account.RefusedError{Description: "blocked"} })
	srv.signIn(2*time.Hour, time.Hour) // expired: every pass is due
	g := e.guardOn(srv, loopPoll)
	g.StartRefresher(context.Background())
	waitForCalls(t, srv, 1, "the refused refresh")
	expectCalls(t, srv, 1, "a refused session is not refreshed again")

	// The user signs in again, and the session the login stored is already due.
	// A refusal holds nothing back, so the refresher takes it at once.
	srv.set(func(s *loopServer) { s.err = nil })
	srv.signIn(2*time.Hour, time.Hour)
	e.f.Clock.Advance(loopPoll)
	waitForCalls(t, srv, 2, "the refresh of the session a new sign-in stored")
}
