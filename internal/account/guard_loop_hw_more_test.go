package account_test

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/monoes/mono-agent/internal/account"
)

// These tests are about the high-water mark (spec §4.5) in a refresher that
// waits. While it holds after a refresh or waits to retry a failure, the loop
// does not call refreshIfDue, which is what keeps the mark current for a process
// that stays up, so the waiting branch of the loop does it itself, through
// touchHW, at most once a minute. touchHW's own rules (a mark that is a minute
// stale, nothing while dormant, nothing for a refused or a missing session) are
// pinned in guard_refresh_hw_more_test.go; the tests here show that the loop
// keeps them.

// hwOnDisk is the mark in session.json, the zero time when it cannot be read.
func hwOnDisk(e *env) time.Time {
	sess, err := e.store.Load()
	if err != nil || sess == nil {
		return time.Time{}
	}
	return sess.HW
}

// waitForHW waits, in real time, until session.json holds the mark want.
func waitForHW(t *testing.T, e *env, want time.Time, what string) {
	t.Helper()
	deadline := time.Now().Add(testWait)
	for !hwOnDisk(e).Equal(want) {
		if time.Now().After(deadline) {
			t.Fatalf("timed out waiting for %s: the mark is %v, want %v", what, hwOnDisk(e), want)
		}
		time.Sleep(2 * time.Millisecond)
	}
}

// expectHW waits quietFor and then fails unless session.json holds the mark want.
func expectHW(t *testing.T, e *env, want time.Time, what string) {
	t.Helper()
	quiet(t, e.loops...)
	if got := hwOnDisk(e); !got.Equal(want) {
		t.Fatalf("%s: the mark is %v, want %v", what, got, want)
	}
}

func TestTheRefresherKeepsTheHighWaterMarkCurrentWhileItHolds(t *testing.T) {
	e := newEnv(t)
	srv := newLoopServer(e) // no lag
	srv.signIn(40*time.Minute, time.Hour)
	g := e.guardOn(srv, loopPoll)
	g.StartRefresher(context.Background())
	waitForCalls(t, srv, 1, "the first refresh")
	expectCalls(t, srv, 1, "the hold")
	start := e.f.Clock.Now()
	waitForHW(t, e, start, "the refresh to store the new token: the mark is its iat")

	// The refresher holds for half an hour. Two minutes on, the mark is two
	// minutes stale, and the loop that holds writes it.
	e.f.Clock.Advance(2 * time.Minute)
	waitForHW(t, e, start.Add(2*time.Minute), "the mark to be written while the refresher holds")
	// Not again before it is a minute stale.
	e.f.Clock.Advance(59 * time.Second)
	expectHW(t, e, start.Add(2*time.Minute), "59 seconds after the write")
	e.f.Clock.Advance(2 * time.Second)
	waitForHW(t, e, start.Add(3*time.Minute+time.Second), "the next write, a minute after the last")
	expectCalls(t, srv, 1, "the hold goes on")
}

func TestTheRefresherKeepsTheHighWaterMarkCurrentWhileItWaitsToRetry(t *testing.T) {
	e, srv, g := failingLoop(t, 2*time.Hour)
	g.StartRefresher(context.Background())
	start := e.f.Clock.Now()
	waitForCalls(t, srv, 1, "the first attempt") // fails at T and waits 30 seconds
	expectCalls(t, srv, 1, "the first failure")
	e.f.Clock.Advance(31 * time.Second)
	waitForCalls(t, srv, 2, "the second attempt") // fails at T+31s and waits 60 seconds
	expectCalls(t, srv, 2, "the second failure")
	e.f.Clock.Advance(61 * time.Second)
	waitForCalls(t, srv, 3, "the third attempt") // fails at T+92s and waits 120 seconds, until T+212s
	expectCalls(t, srv, 3, "the third failure")
	waitForHW(t, e, start.Add(92*time.Second), "the record of the third failure: an attempt that fails writes the mark itself")

	// 61 seconds into the wait the mark is a minute stale, and the loop that waits writes it.
	e.f.Clock.Advance(61 * time.Second)
	waitForHW(t, e, start.Add(153*time.Second), "the mark to be written while the refresher waits to retry")
	// Not again: the retry is 59 seconds away.
	e.f.Clock.Advance(58 * time.Second)
	expectHW(t, e, start.Add(153*time.Second), "58 seconds after the write, a second before the retry")
	expectCalls(t, srv, 3, "the wait goes on")
}

// holdingRefresher is an env whose refresher has just refreshed and now holds for half an hour.
func holdingRefresher(t *testing.T) (*env, *account.Guard) {
	t.Helper()
	e := newEnv(t)
	srv := newLoopServer(e) // no lag
	srv.signIn(40*time.Minute, time.Hour)
	g := e.guardOn(srv, loopPoll)
	g.StartRefresher(context.Background())
	waitForCalls(t, srv, 1, "the first refresh")
	expectCalls(t, srv, 1, "the hold")
	return e, g
}

// fiveMinutesLater is when the mark is five minutes stale: a loop that wrote it whatever the
// session says would write it now.
func fiveMinutesLater(t *testing.T, e *env) {
	t.Helper()
	e.f.Clock.Advance(5 * time.Minute)
	quiet(t, e.loops...)
}

func TestAHoldingRefresherWritesNoMarkWhileDormantOrForARefusedSession(t *testing.T) {
	t.Run("the package is dormant", func(t *testing.T) {
		e, _ := holdingRefresher(t)
		account.SetEnforceFromForTest(t, time.Time{})
		var old time.Time
		e.underLock(func() { old = e.pinSession() })
		fiveMinutesLater(t, e)
		if !mustMtime(t, e.store).Equal(old) {
			t.Fatal("the loop wrote the high-water mark while the package is dormant")
		}
	})
	t.Run("another process was refused", func(t *testing.T) {
		e, _ := holdingRefresher(t)
		var old time.Time
		e.underLock(func() {
			e.save(refusedSession())
			old = e.pinSession()
		})
		fiveMinutesLater(t, e)
		if !mustMtime(t, e.store).Equal(old) {
			t.Fatal("the loop wrote the high-water mark of a refused session")
		}
	})
}

// A session that is gone is a machine with no session (A25): the loop that holds takes the
// loss in, and from the enforcement date on its next pass keeps the clock-guard record, so that
// the date is still judged by a mark when the clock is set back. It does not bring back a token.
func TestAHoldingRefresherThatFindsTheSessionGoneKeepsTheRecord(t *testing.T) {
	e, g := holdingRefresher(t)
	e.underLock(func() {
		if err := os.Remove(filepath.Join(e.dir, "session.json")); err != nil {
			t.Fatal(err)
		}
	})
	// Let the loop take the loss in before the clock makes the record due. A pass that has read
	// the session but not yet the time may find the file gone only after the clock has moved: it
	// then keeps nothing, because the session it had was cached, and counts its attempt at the
	// new time, so that the next pass at the same time writes nothing either. A real clock moves
	// on; a fixture clock that stands still does not. The loop is not wrong, but the test must
	// not depend on which pass came first: one poll of clock makes the loop re-read the file, and
	// three passes later it has taken the loss in.
	e.f.Clock.Advance(2 * loopPoll)
	settle(t, g)
	e.f.Clock.Advance(5 * time.Minute)
	waitForHW(t, e, e.f.Clock.Now(), "the record the loop keeps")
	theRecord(t, e, e.f.Clock.Now())
}
