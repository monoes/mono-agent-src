package coderhost

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/monoes/mono-agent/internal/procbind"
)

func TestTokensMayBeLiveSeesThroughWrapping(t *testing.T) {
	base := &TokensLiveError{Err: errors.New("database is locked")}
	if !TokensMayBeLive(base) || !TokensMayBeLive(fmt.Errorf("wrapped: %w", base)) || !TokensMayBeLive(errors.Join(errors.New("exit status 1"), base)) {
		t.Fatal("a token-live failure was not recognised")
	}
	if TokensMayBeLive(errors.New("exit status 1")) || TokensMayBeLive(nil) {
		t.Fatal("an ordinary failure was taken for a token-live one")
	}
}

// A turn that ends with a failure to end its tokens does not settle the session: the session stays running and held, no new
// turn starts next to the live token, and once the revocation succeeds at a later pass the session is settled.
func TestAnExitThatCouldNotEndTheTokensHoldsTheSessionUntilItCan(t *testing.T) {
	h, _, st := newHost(t)
	x, err := st.EnsureSession("p", "c1")
	if err != nil {
		t.Fatal(err)
	}
	x.State, x.TurnID = StateRunning, "t1"
	if err := st.SaveSession(x); err != nil {
		t.Fatal(err)
	}
	turn := newFakeTurn(4242)
	turn.waitErr = &TokensLiveError{Err: errors.New("could not be ended")}
	h.live = map[string]Turn{key("p", "c1"): turn}
	h.watch(x, turn)
	turn.end()
	deadline := time.Now().Add(3 * time.Second)
	for len(h.Held()) == 0 {
		if time.Now().After(deadline) {
			t.Fatal("the session was not held")
		}
		time.Sleep(5 * time.Millisecond)
	}
	if got := state(t, st, "c1"); got.TurnID != "t1" || got.State != StateRunning {
		t.Fatalf("the session was settled with a token that may be live: %+v", got)
	}
	send(t, st, "c1", "t2", "next")
	if err := h.Tick(); err != nil { // the real retry (RevokeTurnSessionTokensErr) succeeds at this pass: nothing to revoke
		t.Fatal(err)
	}
	h.WaitRetries() // the retry runs off the lock: wait for it
	if got := state(t, st, "c1"); got.TurnID == "t1" {
		t.Fatalf("the session was never settled once the tokens could be ended: %+v", got)
	}
}

// A launch that fails with tokens possibly live is not settled either; the hold's retry has to succeed first, and the dead
// token (a real one here) is gone once it did.
func TestALaunchFailureWithLiveTokensHoldsTheSessionAndTheRetryEndsThem(t *testing.T) {
	h, l, st := newHost(t)
	if _, err := st.EnsureSession("p", "c1"); err != nil { // idle: the send below starts turn t1
		t.Fatal(err)
	}
	if _, err := st.IssueSessionToken("p", "c1", "t1"); err != nil {
		t.Fatal(err)
	}
	if got := legacyTokens(t, st, "c1"); got != 1 {
		t.Fatalf("setup: %d tokens", got)
	}
	l.startErr = &TokensLiveError{Err: errors.New("the refused turn's token could not be ended")}
	send(t, st, "c1", "t1", "hello")
	if err := h.Tick(); err != nil {
		t.Fatal(err)
	}
	if len(h.Held()) != 1 {
		t.Fatalf("held = %v, want the refused session held", h.Held())
	}
	if got := state(t, st, "c1"); got.TurnID != "t1" || got.State != StateRunning {
		t.Fatalf("the session was settled with a token that may be live: %+v", got)
	}
	l.startErr = nil
	if err := h.Tick(); err != nil { // the retry ends the token and settles the session
		t.Fatal(err)
	}
	h.WaitRetries() // the retry runs off the lock: wait for it
	if got := legacyTokens(t, st, "c1"); got != 0 {
		t.Errorf("the refused turn's token is still live after the retry: %d", got)
	}
	if len(h.Held()) != 0 {
		t.Errorf("still held: %v", h.Held())
	}
	if got := state(t, st, "c1"); got.TurnID != "" {
		t.Errorf("the session was not settled after the retry: %+v", got)
	}
}

// A child whose task tokens cannot be revoked at settle is held instead of cleared, and settled once the revoke works.
func TestSettleDoesNotClearAChildWhoseTaskTokensCannotBeRevoked(t *testing.T) {
	h, _, st := newHost(t)
	x, err := st.EnsureSession("p", "c1")
	if err != nil {
		t.Fatal(err)
	}
	x.State, x.TurnID = StateRunning, "t1"
	if err := st.SaveSession(x); err != nil {
		t.Fatal(err)
	}
	if _, err := st.db.Exec(`UPDATE coder_sessions SET role = 'child', parent_conversation_id = 'm' WHERE conversation_id = 'c1'`); err != nil {
		t.Fatal(err)
	}
	x = state(t, st, "c1")
	h.ChildTokens = failingChildTokens{err: errors.New("database is locked")}
	h.mu.Lock()
	h.settle(x)
	h.mu.Unlock()
	if got := state(t, st, "c1"); got.TurnID != "t1" || got.State != StateRunning {
		t.Fatalf("the child was cleared with a task token that may be live: %+v", got)
	}
	if len(h.Held()) != 1 {
		t.Fatalf("held = %v", h.Held())
	}
	h.ChildTokens = failingChildTokens{}
	if err := h.Tick(); err != nil {
		t.Fatal(err)
	}
	h.WaitRetries()
	if got := state(t, st, "c1"); got.TurnID != "" || len(h.Held()) != 0 {
		t.Errorf("not settled once the revoke worked: %+v held=%v", got, h.Held())
	}
}

// After a host restart a turn whose process is gone has its OWN tokens ended before the session is settled (the old host never
// ran its exit's revocation): the legacy token of the turn is gone once Recover returns.
func TestRecoveryEndsTheTokensOfATurnWhoseProcessIsGone(t *testing.T) {
	h, l, st := newHost(t)
	x, err := st.EnsureSession("p", "c1")
	if err != nil {
		t.Fatal(err)
	}
	x.State, x.TurnID, x.TurnPID, x.TurnStamp = StateRunning, "t1", 4242, "gone-stamp" // no live process behind it
	if err := st.SaveSession(x); err != nil {
		t.Fatal(err)
	}
	if _, err := st.IssueSessionToken("p", "c1", "t1"); err != nil {
		t.Fatal(err)
	}
	if err := h.Recover(); err != nil {
		t.Fatal(err)
	}
	if got := legacyTokens(t, st, "c1"); got != 0 {
		t.Errorf("the restarted host left the dead turn's token live: %d", got)
	}
	if got := state(t, st, "c1"); got.TurnID != "" || got.State != StateIdle {
		t.Errorf("the session was not settled: %+v", got)
	}
	if len(l.finished) != 1 {
		t.Errorf("finished = %v", l.finished)
	}
}

// A child whose task tokens cannot be ended at recovery is held with its turn id kept (a later restart finds it again), and
// settled once they can.
func TestRecoveryHoldsAChildWhoseTaskTokensCannotBeEnded(t *testing.T) {
	old := holdBackoff
	holdBackoff = time.Millisecond
	t.Cleanup(func() { holdBackoff = old })
	h, _, st := newHost(t)
	x, err := st.EnsureSession("p", "c1")
	if err != nil {
		t.Fatal(err)
	}
	x.State, x.TurnID, x.TurnPID, x.TurnStamp = StateRunning, "t1", 4242, "gone-stamp"
	if err := st.SaveSession(x); err != nil {
		t.Fatal(err)
	}
	if _, err := st.db.Exec(`UPDATE coder_sessions SET role = 'child', parent_conversation_id = 'm' WHERE conversation_id = 'c1'`); err != nil {
		t.Fatal(err)
	}
	h.ChildTokens = failingChildTokens{err: errors.New("database is locked")}
	if err := h.Recover(); err != nil {
		t.Fatal(err)
	}
	if got := state(t, st, "c1"); got.TurnID != "t1" || len(h.Held()) != 1 {
		t.Fatalf("the child was settled with a task token that may be live: %+v held=%v", got, h.Held())
	}
	h.ChildTokens = failingChildTokens{}
	time.Sleep(20 * time.Millisecond)
	if err := h.Tick(); err != nil {
		t.Fatal(err)
	}
	h.WaitRetries()
	if got := state(t, st, "c1"); got.TurnID != "" || len(h.Held()) != 0 {
		t.Errorf("not settled once the tokens could be ended: %+v held=%v", got, h.Held())
	}
}

type countingChildTokens struct {
	err   error
	calls *int
}

func (c countingChildTokens) RevokeChildTokens(string, string) error { *c.calls++; return c.err }

// A hold for tokens is retried with a growing wait, not at every pass: while the wait runs, a pass does not call the revoke.
func TestAHoldForTokensIsRetriedWithABackoffNotAtEveryPass(t *testing.T) {
	old := holdBackoff
	holdBackoff = time.Hour
	t.Cleanup(func() { holdBackoff = old })
	h, _, st := newHost(t)
	x, err := st.EnsureSession("p", "c1")
	if err != nil {
		t.Fatal(err)
	}
	x.State, x.TurnID = StateRunning, "t1"
	if err := st.SaveSession(x); err != nil {
		t.Fatal(err)
	}
	if _, err := st.db.Exec(`UPDATE coder_sessions SET role = 'child', parent_conversation_id = 'm' WHERE conversation_id = 'c1'`); err != nil {
		t.Fatal(err)
	}
	calls := 0
	h.ChildTokens = countingChildTokens{err: errors.New("database is locked"), calls: &calls}
	x = state(t, st, "c1")
	h.mu.Lock()
	h.settle(x) // the first failure: held, and one call
	h.mu.Unlock()
	if calls != 1 || len(h.Held()) != 1 {
		t.Fatalf("calls %d held %v", calls, h.Held())
	}
	if err := h.Tick(); err != nil { // the first retry is due at once (no failure of the retry yet)
		t.Fatal(err)
	}
	h.WaitRetries() // the retry runs off the lock: wait for it
	after := calls
	for i := 0; i < 5; i++ {
		if err := h.Tick(); err != nil {
			t.Fatal(err)
		}
	}
	if calls != after {
		t.Errorf("the revoke was called %d more times inside the backoff", calls-after)
	}
}

type blockingChildTokens struct {
	started chan struct{}
	release chan struct{}
}

func (b blockingChildTokens) RevokeChildTokens(string, string) error {
	select {
	case b.started <- struct{}{}:
	default:
	}
	<-b.release
	return nil
}

// The retry of a hold runs off the host's lock: while a revocation is stuck, the host still answers (Held takes the lock) and
// a second pass does not start a second retry for the same hold.
func TestTheRetryOfAHoldRunsOutsideTheHostLock(t *testing.T) {
	h, _, st := newHost(t)
	x, err := st.EnsureSession("p", "c1")
	if err != nil {
		t.Fatal(err)
	}
	x.State, x.TurnID = StateRunning, "t1"
	if err := st.SaveSession(x); err != nil {
		t.Fatal(err)
	}
	if _, err := st.db.Exec(`UPDATE coder_sessions SET role = 'child', parent_conversation_id = 'm' WHERE conversation_id = 'c1'`); err != nil {
		t.Fatal(err)
	}
	x = state(t, st, "c1")
	h.ChildTokens = failingChildTokens{err: errors.New("database is locked")}
	h.mu.Lock()
	h.settle(x) // held
	h.mu.Unlock()
	b := blockingChildTokens{started: make(chan struct{}, 4), release: make(chan struct{})}
	h.ChildTokens = b
	if err := h.Tick(); err != nil { // starts the retry, which blocks in the revocation
		t.Fatal(err)
	}
	select {
	case <-b.started:
	case <-time.After(3 * time.Second):
		t.Fatal("the retry never started")
	}
	got := make(chan int, 1)
	go func() { got <- len(h.Held()) }() // takes the host lock
	select {
	case n := <-got:
		if n != 1 {
			t.Errorf("held = %d", n)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("the host lock is held while a retry runs")
	}
	if err := h.Tick(); err != nil { // a second pass: no second retry for the hold
		t.Fatal(err)
	}
	select {
	case <-b.started:
		t.Error("a second retry started for the same hold")
	case <-time.After(150 * time.Millisecond):
	}
	close(b.release)
	h.WaitRetries()
	if got := state(t, st, "c1"); got.TurnID != "" || len(h.Held()) != 0 {
		t.Errorf("not settled after the retry finished: %+v held=%v", got, h.Held())
	}
}

type sequenceChildTokens struct {
	errs  []error // the n-th call returns errs[n]; later calls succeed
	calls *int
}

func (c sequenceChildTokens) RevokeChildTokens(string, string) error {
	n := *c.calls
	*c.calls++
	if n < len(c.errs) {
		return c.errs[n]
	}
	return nil
}

// After a successful off-lock retry the session is cleared without revoking the child's tokens a second time under the lock
// (a second revoke that is slow or fails must not stall the host or put the session back on hold).
func TestASuccessfulRetryDoesNotRevokeTheChildTokensAgainUnderTheLock(t *testing.T) {
	h, _, st := newHost(t)
	x, err := st.EnsureSession("p", "c1")
	if err != nil {
		t.Fatal(err)
	}
	x.State, x.TurnID = StateRunning, "t1"
	if err := st.SaveSession(x); err != nil {
		t.Fatal(err)
	}
	if _, err := st.db.Exec(`UPDATE coder_sessions SET role = 'child', parent_conversation_id = 'm' WHERE conversation_id = 'c1'`); err != nil {
		t.Fatal(err)
	}
	x = state(t, st, "c1")
	calls := 0
	// call 0: the first settle fails (the hold); call 1: the retry succeeds; call 2 would be settle's own second revoke, which fails
	h.ChildTokens = sequenceChildTokens{errs: []error{errors.New("locked"), nil, errors.New("locked again")}, calls: &calls}
	h.mu.Lock()
	h.settle(x)
	h.mu.Unlock()
	if err := h.Tick(); err != nil {
		t.Fatal(err)
	}
	h.WaitRetries()
	if calls != 2 {
		t.Errorf("the child's tokens were revoked %d times, want 2 (the failing settle and the retry)", calls)
	}
	if got := state(t, st, "c1"); got.TurnID != "" || len(h.Held()) != 0 {
		t.Errorf("the session was not settled by the successful retry: %+v held=%v", got, h.Held())
	}
}

// A hold about a turn that may still be running (its exit cleanup unfinished) is not retried, settled, or released to a queued
// turn until that turn has finished, even though the revocation itself would succeed.
func TestAHoldWaitsForTheOldTurnToFinishBeforeAQueuedTurnStarts(t *testing.T) {
	h, l, st := newHost(t)
	if _, err := st.EnsureSession("p", "c1"); err != nil {
		t.Fatal(err)
	}
	ended := make(chan struct{})
	l.startErr = &TokensLiveError{Err: errors.New("the turn had not ended"), Ended: ended}
	send(t, st, "c1", "t1", "first")
	if err := h.Tick(); err != nil {
		t.Fatal(err)
	}
	if len(h.Held()) != 1 {
		t.Fatalf("held = %v", h.Held())
	}
	l.startErr = nil
	send(t, st, "c1", "t2", "queued behind the old turn")
	for i := 0; i < 3; i++ { // the revocation would succeed, but the old turn has not finished
		if err := h.Tick(); err != nil {
			t.Fatal(err)
		}
		h.WaitRetries()
	}
	if got := state(t, st, "c1"); got.TurnID != "t1" || len(h.Held()) != 1 || len(l.started) != 0 {
		t.Fatalf("the session was released while the old turn had not finished: %+v held=%v started=%v", got, h.Held(), l.started)
	}
	close(ended) // the old turn finished
	for i := 0; i < 3; i++ {
		if err := h.Tick(); err != nil {
			t.Fatal(err)
		}
		h.WaitRetries()
	}
	if len(h.Held()) != 0 || len(l.started) != 1 {
		t.Errorf("the queued turn did not start after the old one finished: held=%v started=%v", h.Held(), l.started)
	}
	l.turn("t2").end()
	h.Wait()
}

// switchChildTokens fails until told to block, then blocks every call until released.
type switchChildTokens struct {
	mu      sync.Mutex
	block   bool
	started chan struct{}
	release chan struct{}
}

func (c *switchChildTokens) setBlock() { c.mu.Lock(); c.block = true; c.mu.Unlock() }

func (c *switchChildTokens) RevokeChildTokens(string, string) error {
	c.mu.Lock()
	block := c.block
	c.mu.Unlock()
	if !block {
		return errors.New("database is locked")
	}
	select {
	case c.started <- struct{}{}:
	default:
	}
	<-c.release
	return nil
}

// Run does not return while the retry of a hold is still running: the registry and the database are closed right after it.
func TestRunWaitsForARetryInFlightBeforeItReturns(t *testing.T) {
	h, _, st := newHost(t)
	h.Poll = 10 * time.Millisecond
	x, err := st.EnsureSession("p", "c1") // idle while Run recovers: the hold is made after Run is up
	if err != nil {
		t.Fatal(err)
	}
	if _, err := st.db.Exec(`UPDATE coder_sessions SET role = 'child', parent_conversation_id = 'm' WHERE conversation_id = 'c1'`); err != nil {
		t.Fatal(err)
	}
	sw := &switchChildTokens{started: make(chan struct{}, 4), release: make(chan struct{})}
	h.ChildTokens = sw
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	returned := make(chan error, 1)
	go func() { returned <- h.Run(ctx) }()
	time.Sleep(100 * time.Millisecond) // Recover is done and the loop is ticking
	x.State, x.TurnID = StateRunning, "t1"
	if err := st.SaveSession(x); err != nil {
		t.Fatal(err)
	}
	x = state(t, st, "c1")
	h.mu.Lock()
	h.settle(x) // the child's task tokens cannot be revoked: held
	h.mu.Unlock()
	sw.setBlock()
	select {
	case <-sw.started: // a pass started the retry, which is stuck in the revocation
	case <-time.After(5 * time.Second):
		t.Fatal("the retry never started")
	}
	cancel()
	select {
	case <-returned:
		t.Fatal("Run returned while a retry was still running")
	case <-time.After(200 * time.Millisecond):
	}
	close(sw.release)
	select {
	case <-returned:
	case <-time.After(5 * time.Second):
		t.Fatal("Run did not return after the retry finished")
	}
}

// A failure without a channel does not hide a later one that has it, and several pending turns are all waited for.
func TestTurnStartedSeesThroughWrapping(t *testing.T) {
	base := &StartedError{Err: errors.New("failed after the process was created")}
	if !TurnStarted(base) || !TurnStarted(fmt.Errorf("w: %w", base)) || !TurnStarted(errors.Join(errors.New("x"), base)) {
		t.Fatal("a failure after the process was created was not recognised")
	}
	if TurnStarted(errors.New("could not start")) || TurnStarted(nil) {
		t.Fatal("a failure before any process existed was taken for a started turn")
	}
}

func TestTurnPendingSeesEveryChannelInAJoinedError(t *testing.T) {
	if TurnPending(errors.New("plain")) != nil || TurnPending(&TokensLiveError{Err: errors.New("no channel")}) != nil {
		t.Fatal("a failure without a pending turn reported one")
	}
	first, second := make(chan struct{}), make(chan struct{})
	err := errors.Join(
		&TokensLiveError{Err: errors.New("the first failure carries no channel")},
		fmt.Errorf("wrapped: %w", errors.Join(errors.New("x"), &TokensLiveError{Err: errors.New("pending"), Ended: first})),
		&TokensLiveError{Err: errors.New("another"), Ended: second})
	pending := TurnPending(err)
	if pending == nil {
		t.Fatal("the pending channel was hidden behind the first failure")
	}
	close(first)
	select {
	case <-pending:
		t.Fatal("reported finished while one pending turn was still running")
	case <-time.After(100 * time.Millisecond):
	}
	close(second)
	select {
	case <-pending:
	case <-time.After(3 * time.Second):
		t.Fatal("never reported finished")
	}
}

func turnCapRows(t *testing.T, st *Store, turn string) (rows, started int) {
	t.Helper()
	if err := st.db.QueryRow(`SELECT COUNT(*), COALESCE(SUM(started), 0) FROM coder_session_turn_caps WHERE conversation_id = 'c1' AND turn_id = ?`, turn).Scan(&rows, &started); err != nil {
		t.Fatal(err)
	}
	return
}

// A start that failed with its process possibly still running keeps the turn's spend cap (marked started, as a running turn's);
// a start that failed before any process existed drops it.
func TestAStartErrorKeepsTheCapOfATurnThatMayStillBeRunning(t *testing.T) {
	for name, c := range map[string]struct {
		err     error
		rows    int
		started int
	}{
		"the old turn may still be running": {&TokensLiveError{Err: errors.New("not ended"), Ended: make(chan struct{})}, 1, 1},
		"it started and already ended":      {&StartedError{Err: errors.New("the refusal came after the process exited")}, 1, 1},
		"no process existed":                {errors.New("could not start"), 0, 0},
	} {
		h, l, st := newHost(t)
		if _, err := st.EnsureSession("p", "c1"); err != nil {
			t.Fatal(err)
		}
		if err := st.SetBudget("p", "c1", 10); err != nil {
			t.Fatal(err)
		}
		l.startErr = c.err
		send(t, st, "c1", "t1", "hello")
		if err := h.Tick(); err != nil {
			t.Fatal(err)
		}
		if rows, started := turnCapRows(t, st, "t1"); rows != c.rows || started != c.started {
			t.Errorf("%s: cap rows %d started %d, want %d and %d", name, rows, started, c.rows, c.started)
		}
	}
}

// Several old turns named by one failure are ALL waited for, with no goroutine: the host looks at the channels, and the session
// is released only when the last one has finished.
func TestAHoldWaitsForEveryOldTurnTheFailureNames(t *testing.T) {
	h, l, st := newHost(t)
	if _, err := st.EnsureSession("p", "c1"); err != nil {
		t.Fatal(err)
	}
	a, b := make(chan struct{}), make(chan struct{})
	l.startErr = errors.Join(&TokensLiveError{Err: errors.New("first"), Ended: a}, &TokensLiveError{Err: errors.New("second"), Ended: b})
	send(t, st, "c1", "t1", "first")
	if err := h.Tick(); err != nil {
		t.Fatal(err)
	}
	if len(h.Held()) != 1 {
		t.Fatalf("held = %v", h.Held())
	}
	l.startErr = nil
	close(a)
	for i := 0; i < 2; i++ {
		if err := h.Tick(); err != nil {
			t.Fatal(err)
		}
		h.WaitRetries()
	}
	if len(h.Held()) != 1 {
		t.Fatalf("released while the second old turn had not finished: %v", h.Held())
	}
	close(b)
	for i := 0; i < 2; i++ {
		if err := h.Tick(); err != nil {
			t.Fatal(err)
		}
		h.WaitRetries()
	}
	if len(h.Held()) != 0 {
		t.Errorf("still held after both finished: %v", h.Held())
	}
	if got := state(t, st, "c1"); got.TurnID != "" {
		t.Errorf("not settled: %+v", got)
	}
}

func TestTurnsPendingReturnsEveryChannelWithoutAGoroutine(t *testing.T) {
	a, b := make(chan struct{}), make(chan struct{})
	chans := TurnsPending(errors.Join(&TokensLiveError{Err: errors.New("x")}, &TokensLiveError{Err: errors.New("y"), Ended: a}, &TokensLiveError{Err: errors.New("z"), Ended: b}))
	if len(chans) != 2 || allEnded(chans) {
		t.Fatalf("channels %d, ended %v", len(chans), allEnded(chans))
	}
	close(a)
	if allEnded(chans) {
		t.Fatal("reported ended with one turn still running")
	}
	close(b)
	if !allEnded(chans) {
		t.Fatal("not reported ended after both finished")
	}
	if TurnsPending(errors.New("plain")) != nil {
		t.Error("a plain error named a pending turn")
	}
}

// A held bound session whose lookup keeps failing is not skipped silently: the failures are counted, shown with the hold, and logged
// (at powers of two, not at every pass), and the session stays held.
func TestAHeldSessionWhoseLookupKeepsFailingIsCountedAndLogged(t *testing.T) {
	h, l, st := newHost(t)
	x, err := st.EnsureSession("p", "c1")
	if err != nil {
		t.Fatal(err)
	}
	x.State, x.TurnID = StateRunning, "t1"
	if err := st.SaveSession(x); err != nil {
		t.Fatal(err)
	}
	if err := st.RecordTurnProcess("p", "c1", "t1", 4242, "start:99", true); err != nil {
		t.Fatal(err)
	}
	h.Stopper = &fakeStopper{results: []procbind.StopResult{{Outcome: procbind.StopHeld, Cause: "it did not exit"}}}
	var logs []string
	h.Logf = func(f string, a ...any) { logs = append(logs, fmt.Sprintf(f, a...)) }
	if err := h.Recover(); err != nil {
		t.Fatal(err)
	}
	if _, err := st.db.Exec(`ALTER TABLE coder_session_tokens_v2 RENAME TO coder_session_tokens_v2_gone`); err != nil { // the lookup now fails
		t.Fatal(err)
	}
	for i := 0; i < 9; i++ {
		if err := h.Tick(); err != nil {
			t.Fatal(err)
		}
	}
	held := h.Held()
	shown := false
	for _, cause := range held { // the map is keyed by the session's host key
		shown = strings.Contains(cause, "failed attempts")
	}
	if len(held) != 1 || !shown {
		t.Fatalf("the hold does not show the failing lookups: %v", held)
	}
	n := 0
	for _, l := range logs {
		if strings.Contains(l, "cannot be looked up") {
			n++
		}
	}
	if n < 1 || n > 4 { // attempts 1, 2, 4, 8 of 9: logged at powers of two, not at every pass
		t.Errorf("logged %d times for 9 failing passes: %v", n, logs)
	}
	if got := state(t, st, "c1"); got.TurnID != "t1" || len(l.started) != 0 {
		t.Errorf("the session was released while its lookups fail: %+v started=%v", got, l.started)
	}
}
