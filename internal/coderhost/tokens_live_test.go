package coderhost

import (
	"errors"
	"fmt"
	"testing"
	"time"
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
