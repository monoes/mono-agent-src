package account

import (
	"context"
	"time"
)

// StartRefresher starts the background refresher: it follows session.json,
// tells OnRefused callbacks about a refusal, and refreshes at half the token's
// lifetime, retrying a failure with a backoff of 30 seconds doubling to 5
// minutes. It is idempotent, does nothing while the package is dormant (spec
// D22) and ends when ctx ends or the guard is closed, after a grant in flight is
// answered and stored: a grant once sent is never abandoned (A20), so the end of
// ctx and Close wait for it, for at most the grant's timeout and the key store
// write that follows. The daemon is to start it at once and any other process after
// LateRefresher (B3a and B2 write those calls).
//
// A guard starts one loop in its life. A loop that has ended with its ctx leaves
// loopCancel set, so every later call is a no-op and the ctx of the first call
// decides how long the refresher runs: callers must pass a ctx that lives as long
// as the process.
func (g *Guard) StartRefresher(ctx context.Context) {
	if dormant() {
		return
	}
	g.mu.Lock()
	if g.closed || g.loopCancel != nil {
		g.mu.Unlock()
		return
	}
	lctx, cancel := context.WithCancel(ctx)
	done := make(chan struct{})
	g.loopCancel, g.loopDone = cancel, done
	g.mu.Unlock()
	go func() {
		defer close(done)
		g.runLoop(lctx)
	}()
}

// runLoop wakes every poll and judges everything by the guard's clock, so a
// test that moves the clock controls it exactly. It keeps running with no
// session, so a sign-in from another process is picked up by itself. A clock
// that reads more than clockBackTolerance before the previous pass, while a marker
// is pending, is taken for a clock set back: the evidence of the marker is raised
// to the previous reading and the pass is made at once, so that a token whose age
// can no longer be told is dropped, not presented after its window (A24).
func (g *Guard) runLoop(ctx context.Context) {
	ticker := time.NewTicker(g.poll)
	defer ticker.Stop()
	var backoff time.Duration
	var notBefore time.Time // no attempt before this, by the guard's clock; counted from the start of the failed attempt
	// After a refresh, no attempt before holdUntil: half the new token's lifetime
	// after heldAt, the start of the pass that refreshed. monoes.me rotates the
	// refresh token on every use, and a local clock half a lifetime or more ahead
	// of monoes.me sees each token it has just been given as past its half-life,
	// so without the hold every pass would refresh again.
	var heldAt, holdUntil time.Time
	// The stamp of the marker that this loop's own last unsettled attempt left, which it
	// follows on the schedule below (A24). A marker with any other stamp was left by
	// another process.
	var ownPending time.Time
	var prevNow time.Time // the clock of the previous pass
	for ctx.Err() == nil {
		st := g.Status() // follows the file and fires OnRefused, whatever the pass does next
		// The time of the pass is read after the verdict: a clock set back between the
		// two readings then leaves now on the later side of the step, where a hold is
		// counted from; a reading from before it would put the start of a hold after
		// the clock, and the next pass would refresh again.
		now := g.now()
		// A hold made after now means the clock went back, and ends it. A stored time
		// must never keep a refresh off, or a clock set back could not be repaired by
		// the refresh that resets it (see elapsed). So does the verdict of a clock that
		// went back: the loop that holds keeps the high-water mark current, so a clock
		// corrected after that lies before the mark and is locked, and one refresh,
		// which the corrected clock and monoes.me agree on, repairs it; nothing in
		// this process would before the clock came round to the mark again.
		//
		// A marker is on a clock: monoes.me repeats its answer for 300 s and the guard drops
		// the token after pendingRetryWindow, so waiting out a hold of half an hour or a
		// backoff of five minutes would lose a token that a retry still recovers. No hold
		// keeps a pass off while there is one. One that another process left (a CLI whose
		// answer was lost while this loop held after a refresh or waited out a backoff) also
		// caps the wait at backoffMin. A marker that the loop follows, its own or one it has
		// retried, keeps the backoff, which starts again at backoffMin for each new marker,
		// so that its retries at +30, +90 and +210 s are inside the window.
		pending := g.pendingStamp()
		if pending.IsZero() {
			ownPending = time.Time{}
		}
		// The backoff runs on this clock, so after a step back the next retry of a marker
		// comes as much later as the step, and no pass looks at the marker meanwhile. A
		// clock that reads more than clockBackTolerance before the previous pass, with a
		// marker pending, went back: the session's last attempt is raised to the previous
		// reading, the evidence pendingExpired reads, and the pass is made at once, so that
		// it drops the token while the clock is still behind that evidence.
		if !pending.IsZero() && !prevNow.IsZero() && now.Before(prevNow.Add(-clockBackTolerance)) {
			g.keepLastAttempt(prevNow)
			notBefore = time.Time{}
		}
		prevNow = now
		foreign := !pending.IsZero() && !pending.Equal(ownPending)
		held := holdUntil.After(now) && !heldAt.After(now) && st.Reason != ReasonClockRollback && pending.IsZero()
		wait := notBefore.Sub(now)
		// A notBefore further away than the longest backoff means the clock went back.
		if !held && (!notBefore.After(now) || wait > backoffMax || (foreign && wait > backoffMin)) {
			_, oc, _ := g.refreshIfDue(ctx, modeBackground)
			switch oc {
			case outcomeFailed:
				stamp := g.pendingStamp()
				if !stamp.IsZero() && !stamp.Equal(ownPending) {
					backoff = 0 // a marker that the loop has not followed before: its schedule starts at backoffMin
				}
				backoff = min(max(backoff*2, backoffMin), backoffMax)
				notBefore = now.Add(backoff)
				ownPending = stamp // an attempt that failed with its marker kept is the loop's own
			case outcomeRefreshed:
				backoff, notBefore = 0, time.Time{}
				_, rcpt := g.cached()
				heldAt, holdUntil = now, now.Add(holdFor(rcpt))
			case outcomeRefused:
				backoff, notBefore = 0, time.Time{}
			}
		} else {
			g.touchHW(now) // as a pass that finds nothing due does, keeps the high-water mark current
		}
		g.passes.Add(1)
		select {
		case <-ctx.Done():
		case <-ticker.C:
		}
	}
}

// holdFor is how long the refresher leaves alone a token it has just obtained:
// half its lifetime, the half-life that makes a token due. With no receipt (the
// cache could not read the token back) it is the shortest backoff.
func holdFor(rcpt *Receipt) time.Duration {
	if rcpt == nil {
		return backoffMin
	}
	return rcpt.ExpiresAt.Sub(rcpt.IssuedAt) / 2
}
