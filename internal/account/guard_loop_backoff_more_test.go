package account_test

import (
	"context"
	"testing"
	"time"

	"github.com/monoes/mono-agent/internal/account"
)

// These tests pin the refresher's backoff where guard_loop_test.go leaves it
// open: the very second a retry comes, a call that takes time, the resets, the
// clock that went back, and a pass that found nothing to do. The fixture clock
// moves. Real time is only the refresher's tick (loopPoll), the quiet(t, e.loops...) waits for
// what must not happen and the eventually waits (3 seconds at most) for what must.

// failingLoop is an env whose server cannot be reached, with a guard on it.
func failingLoop(t *testing.T, age time.Duration) (*env, *loopServer, *account.Guard) {
	t.Helper()
	e := newEnv(t)
	srv := newLoopServer(e)
	srv.set(func(s *loopServer) { s.err = transient(account.ReasonUnreachable) })
	srv.signIn(age, time.Hour)
	return e, srv, e.guardOn(srv, loopPoll)
}

func TestARetryComesAtTheSecondTheBackoffEnds(t *testing.T) {
	e, srv, g := failingLoop(t, 2*time.Hour)
	g.StartRefresher(context.Background())
	waitForCalls(t, srv, 1, "the first attempt")
	expectCalls(t, srv, 1, "the failed attempt")
	e.f.Clock.Advance(30*time.Second - time.Second)
	expectCalls(t, srv, 1, "a second before the 30 second backoff ends")
	e.f.Clock.Advance(time.Second) // 30 seconds after the start of the failed attempt, to the second
	waitForCalls(t, srv, 2, "the retry at the very second the backoff ends")
}

func TestTheBackoffIsCountedFromTheStartOfTheFailedAttempt(t *testing.T) {
	// The call takes 20 seconds on the clock. A retry 30 seconds after its
	// start is 10 seconds after its end: the backoff does not depend on how long
	// the call took.
	e, srv, g := failingLoop(t, 2*time.Hour)
	srv.set(func(s *loopServer) { s.takes = 20 * time.Second })
	g.StartRefresher(context.Background())
	waitForCalls(t, srv, 1, "the first attempt")
	expectCalls(t, srv, 1, "the failed attempt")
	e.f.Clock.Advance(9 * time.Second) // 29 seconds after the start
	expectCalls(t, srv, 1, "29 seconds after the start of the failed attempt")
	e.f.Clock.Advance(2 * time.Second) // 31 seconds after the start
	waitForCalls(t, srv, 2, "the retry 30 seconds after the start of the failed attempt")
}

func TestTheBackoffStartsOverAfterASuccess(t *testing.T) {
	e, srv, g := failingLoop(t, 2*time.Hour)
	g.StartRefresher(context.Background())
	waitForCalls(t, srv, 1, "the first attempt") // fails: the next comes in 30 seconds
	expectCalls(t, srv, 1, "the first failure")
	e.f.Clock.Advance(31 * time.Second)
	waitForCalls(t, srv, 2, "the second attempt") // fails: the next comes in 60 seconds
	expectCalls(t, srv, 2, "the second failure")
	srv.set(func(s *loopServer) { s.err = nil }) // monoes.me is back
	e.f.Clock.Advance(61 * time.Second)
	waitForCalls(t, srv, 3, "the attempt that succeeds")
	expectCalls(t, srv, 3, "the success")

	// The new token is due after half its lifetime. When the network is down
	// again the backoff starts over at 30 seconds, not at 120.
	srv.set(func(s *loopServer) { s.err = transient(account.ReasonUnreachable) })
	e.f.Clock.Advance(30 * time.Minute)
	waitForCalls(t, srv, 4, "the refresh at the half-life")
	expectCalls(t, srv, 4, "the failure after the success")
	e.f.Clock.Advance(29 * time.Second)
	expectCalls(t, srv, 4, "29 seconds after that failure")
	e.f.Clock.Advance(2 * time.Second)
	waitForCalls(t, srv, 5, "the retry 30 seconds after the failure that followed the success")
}

func TestTheBackoffStartsOverAfterARefusalAndANewSignIn(t *testing.T) {
	e, srv, g := failingLoop(t, 2*time.Hour)
	g.StartRefresher(context.Background())
	waitForCalls(t, srv, 1, "the first attempt")
	expectCalls(t, srv, 1, "the first failure")
	e.f.Clock.Advance(31 * time.Second)
	waitForCalls(t, srv, 2, "the second attempt") // the next would come in 60 seconds
	expectCalls(t, srv, 2, "the second failure")
	srv.set(func(s *loopServer) { s.err = &account.RefusedError{Description: "revoked"} })
	e.f.Clock.Advance(61 * time.Second)
	waitForCalls(t, srv, 3, "the attempt that is refused")
	expectCalls(t, srv, 3, "the refusal: a refused session is not refreshed again")

	// The user signs in again and the network is down. The refresher is still
	// there, takes the new session at once, and its backoff starts over at 30
	// seconds, not at 120.
	srv.set(func(s *loopServer) { s.err = transient(account.ReasonUnreachable) })
	srv.signIn(2*time.Hour, time.Hour)
	e.f.Clock.Advance(loopPoll)
	waitForCalls(t, srv, 4, "the first attempt on the new session")
	expectCalls(t, srv, 4, "the failure on the new session")
	e.f.Clock.Advance(29 * time.Second)
	expectCalls(t, srv, 4, "29 seconds after that failure")
	e.f.Clock.Advance(2 * time.Second)
	waitForCalls(t, srv, 5, "the retry 30 seconds after the failure on the new session")
}

func TestAClockSetBackMoreThanTheLongestBackoffEndsTheWait(t *testing.T) {
	e, srv, g := failingLoop(t, 2*time.Hour)
	g.StartRefresher(context.Background())
	waitForCalls(t, srv, 1, "the first attempt") // fails at T: no retry before T+30s
	expectCalls(t, srv, 1, "the failed attempt")

	// The clock goes back to 4 minutes 30 seconds before T: the retry is exactly 5
	// minutes away, which is as long as a backoff gets, so it is a wait.
	e.f.Clock.Advance(-(4*time.Minute + 30*time.Second))
	expectCalls(t, srv, 1, "a retry exactly the longest backoff away")
	// One more second back and it is 5 minutes 1 second away: no backoff is that
	// long, so the clock went back and the wait is over.
	e.f.Clock.Advance(-time.Second)
	waitForCalls(t, srv, 2, "a retry further away than the longest backoff")
}

func TestASuccessEndsTheWaitItFollowedSoAClockSetBackCanBeRepaired(t *testing.T) {
	// A failure at T waits until T+30s. The attempt after it succeeds, so the
	// wait is over for good: when the clock then goes back, the refresher does
	// not wait for the clock to come round to T+30s again.
	e, srv, g := failingLoop(t, 2*time.Hour)
	srv.set(func(s *loopServer) { s.lag = 61 * time.Minute }) // every token it issues is due again at once by this clock
	g.StartRefresher(context.Background())
	waitForCalls(t, srv, 1, "the first attempt")
	expectCalls(t, srv, 1, "the failed attempt")
	srv.set(func(s *loopServer) { s.err = nil })
	e.f.Clock.Advance(31 * time.Second)
	waitForCalls(t, srv, 2, "the attempt that succeeds")
	expectCalls(t, srv, 2, "the success")

	e.f.Clock.Advance(-3 * time.Minute) // the old retry time, T+30s, is 3 minutes away
	waitForCalls(t, srv, 3, "the refresh after the clock was set back")
}

func TestARefusalEndsTheWaitItFollowedSoANewSignInIsTakenAfterAClockSetBack(t *testing.T) {
	e, srv, g := failingLoop(t, 2*time.Hour)
	g.StartRefresher(context.Background())
	waitForCalls(t, srv, 1, "the first attempt")
	expectCalls(t, srv, 1, "the failed attempt") // no retry before T+30s
	srv.set(func(s *loopServer) { s.err = &account.RefusedError{Description: "revoked"} })
	e.f.Clock.Advance(31 * time.Second)
	waitForCalls(t, srv, 2, "the attempt that is refused")
	expectCalls(t, srv, 2, "the refusal")

	e.f.Clock.Advance(-3 * time.Minute) // the old retry time, T+30s, is 3 minutes away
	srv.set(func(s *loopServer) { s.err = nil })
	srv.signIn(2*time.Hour, time.Hour) // the new session is due
	e.f.Clock.Advance(loopPoll)
	waitForCalls(t, srv, 3, "the refresh of the new session")
}

func TestAPassThatFindsNothingToDoIsNotAFailure(t *testing.T) {
	e := newEnv(t)
	srv := newLoopServer(e)
	srv.signIn(30*time.Minute-10*time.Second, time.Hour) // half-life in ten seconds
	g := e.guardOn(srv, loopPoll)
	g.StartRefresher(context.Background())
	expectCalls(t, srv, 0, "before the half-life") // the first pass finds nothing due
	e.f.Clock.Advance(10 * time.Second)            // the half-life: at once, not after a backoff
	waitForCalls(t, srv, 1, "the refresh at the half-life")
}

func TestTheRefresherStillFollowsTheFileWhileItWaitsToRetry(t *testing.T) {
	e, srv, g := failingLoop(t, 2*time.Hour)
	got := make(chan account.Status, 4)
	g.OnRefused(func(st account.Status) { got <- st })
	g.StartRefresher(context.Background())
	waitForCalls(t, srv, 1, "the first attempt") // fails: the refresher waits 30 seconds
	expectCalls(t, srv, 1, "the failed attempt")

	e.storeRefusal() // another process was refused
	e.f.Clock.Advance(loopPoll)
	select {
	case st := <-got:
		if st.Reason != account.ReasonRefused {
			t.Fatalf("callback got %s/%q", st.State, st.Reason)
		}
	case <-time.After(testWait):
		t.Fatal("a refresher that waits to retry did not notice another process's refusal")
	}
}
