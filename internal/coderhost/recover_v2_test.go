package coderhost

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/monoes/mono-agent/internal/procbind"
)

type fakeStopper struct {
	mu      sync.Mutex
	calls   [][2]any
	results []procbind.StopResult // consumed in order; the last repeats
}

func (f *fakeStopper) StopLeader(pid int, stamp string) procbind.StopResult {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.calls = append(f.calls, [2]any{pid, stamp})
	r := f.results[0]
	if len(f.results) > 1 {
		f.results = f.results[1:]
	}
	return r
}

// v2Turn makes a running session whose current turn is a bound turn: the session row as the host saved it (with the
// pid and stamp it records only after the launch returns, or empty), and the token and binding rows with the leader
// identity committed before delivery.
func v2Turn(t *testing.T, st *Store, conv, turn string, savedPID int, savedStamp string, bindPID int, bindStamp string) string {
	t.Helper()
	x, err := st.EnsureSession("p", conv)
	if err != nil {
		t.Fatal(err)
	}
	x.State, x.TurnID, x.TurnPID, x.TurnStamp = StateRunning, turn, savedPID, savedStamp
	if err := st.SaveSession(x); err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	key, err := procbind.RowKey(ctx, st.db, "p")
	if err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC().Truncate(time.Millisecond)
	id, _ := procbind.NewTokenID()
	_, hash, _ := procbind.NewSecret(procbind.SessionPrefixV2)
	err = procbind.MintTx(ctx, st.db, key, procbind.MintRequest{Profile: "p", WorkerPID: bindPID, WorkerStamp: bindStamp, HostGeneration: "old-gen", Now: now,
		Tokens: []procbind.MintToken{{Kind: procbind.KindSession, ID: id, Hash: hash, Scope: procbind.SessionScope(conv, turn), Expires: now.Add(time.Hour), ConversationID: conv, TurnID: turn}}})
	if err != nil {
		t.Fatal(err)
	}
	return id
}

// A bound turn is never adopted: whatever the launcher could adopt, the previous host's bound turn is stopped, and
// once its leader is gone the session is settled, the launcher told, and the turn's token revoked.
func TestARestartStopsABoundTurnInsteadOfAdoptingIt(t *testing.T) {
	for name, res := range map[string]procbind.StopResult{
		"the leader was stopped": {Outcome: procbind.StopStopped},
		"the leader was absent":  {Outcome: procbind.StopAbsent},
	} {
		h, l, st := newHost(t)
		id := v2Turn(t, st, "c1", "t1", 0, "", 4242, "bind-stamp")
		l.alive[4242] = newFakeTurn(4242) // the launcher WOULD adopt this pid: a bound turn must not be adopted
		fs := &fakeStopper{results: []procbind.StopResult{res}}
		h.Stopper = fs
		if err := h.Recover(); err != nil {
			t.Fatal(err)
		}
		if len(fs.calls) != 1 || fs.calls[0] != [2]any{4242, "bind-stamp"} {
			t.Errorf("%s: the stopper was called with %v, want the binding row's identity", name, fs.calls)
		}
		if h.live[key("p", "c1")] != nil {
			t.Errorf("%s: a bound turn was adopted", name)
		}
		x := state(t, st, "c1")
		if x.TurnID != "" || x.State != StateIdle {
			t.Errorf("%s: the session was not settled: %+v", name, x)
		}
		if len(l.finished) != 1 {
			t.Errorf("%s: the launcher was told %v", name, l.finished)
		}
		if rv, err := procbind.IsRevoked(context.Background(), st.db, procbind.KindSession, id); err != nil || !rv {
			t.Errorf("%s: the turn's token was not revoked: %v %v", name, rv, err)
		}
		if len(h.Held()) != 0 {
			t.Errorf("%s: held: %v", name, h.Held())
		}
	}
}

// A check that cannot be completed holds the session: not settled, the launcher not told, the token not revoked, and
// a send to the chat waits; the check is retried at every pass, and once the leader is confirmed gone the session is
// settled and the waiting send runs.
func TestAHeldBoundTurnBlocksTheChatUntilItsLeaderIsConfirmedGone(t *testing.T) {
	h, l, st := newHost(t)
	id := v2Turn(t, st, "c1", "t1", 0, "", 4242, "bind-stamp")
	fs := &fakeStopper{results: []procbind.StopResult{
		{Outcome: procbind.StopHeld, Cause: "the turn's process could not be opened (too many open files)"},
		{Outcome: procbind.StopHeld, Cause: "the turn's process could not be opened (too many open files)"},
		{Outcome: procbind.StopStopped},
	}}
	h.Stopper = fs
	if err := h.Recover(); err != nil {
		t.Fatal(err)
	}
	x := state(t, st, "c1")
	if x.TurnID != "t1" || x.State != StateRunning || len(l.finished) != 0 {
		t.Fatalf("a held session was settled: %+v finished=%v", x, l.finished)
	}
	if rv, _ := procbind.IsRevoked(context.Background(), st.db, procbind.KindSession, id); !rv {
		t.Error("the token of a turn whose leader may survive was not revoked before the stop")
	}
	if held := h.Held(); len(held) != 1 {
		t.Errorf("held = %v", held)
	}
	send(t, st, "c1", "t2", "next")
	if err := h.Tick(); err != nil { // second Held answer: still held, the send waits, no turn starts
		t.Fatal(err)
	}
	if len(l.started) != 0 {
		t.Fatalf("a turn started on a held chat: %v", l.started)
	}
	if err := h.Tick(); err != nil { // the leader is confirmed gone: the session is settled
		t.Fatal(err)
	}
	if x := state(t, st, "c1"); x.TurnID != "" || x.State != StateIdle {
		t.Fatalf("the session was not settled once the leader was confirmed gone: %+v", x)
	}
	if err := h.Tick(); err != nil { // and the waiting send runs on the next pass
		t.Fatal(err)
	}
	if len(l.started) != 1 {
		t.Fatalf("the waiting send did not run once the hold cleared: %v", l.started)
	}
	if len(h.Held()) != 0 {
		t.Errorf("still held: %v", h.Held())
	}
	if rv, _ := procbind.IsRevoked(context.Background(), st.db, procbind.KindSession, id); !rv {
		t.Error("the token was not revoked when the hold cleared")
	}
	l.turn("t2").end() // the started turn ends, so the host's watcher returns
	h.Wait()
}

// When the binding row cannot be read, the session's own record of the turn identifies the leader.
func TestRecoveryFallsBackToTheSessionRecord(t *testing.T) {
	h, _, st := newHost(t)
	v2Turn(t, st, "c1", "t1", 777, "saved-stamp", 4242, "bind-stamp")
	// the profile's key is gone: the binding row cannot be verified
	if _, err := st.db.Exec(`DELETE FROM vault_keys WHERE profile_id = 'p'`); err != nil {
		t.Fatal(err)
	}
	fs := &fakeStopper{results: []procbind.StopResult{{Outcome: procbind.StopAbsent}}}
	h.Stopper = fs
	if err := h.Recover(); err != nil {
		t.Fatal(err)
	}
	if len(fs.calls) != 1 || fs.calls[0] != [2]any{777, "saved-stamp"} {
		t.Errorf("fallback identity: %v", fs.calls)
	}
}

// A legacy turn is adopted exactly as before.
func TestALegacyTurnIsStillAdopted(t *testing.T) {
	h, l, st := newHost(t)
	x, err := st.EnsureSession("p", "c1")
	if err != nil {
		t.Fatal(err)
	}
	x.State, x.TurnID, x.TurnPID, x.TurnStamp = StateRunning, "t1", 4242, "stamp"
	if err := st.SaveSession(x); err != nil {
		t.Fatal(err)
	}
	l.alive[4242] = newFakeTurn(4242)
	fs := &fakeStopper{results: []procbind.StopResult{{Outcome: procbind.StopStopped}}}
	h.Stopper = fs
	if err := h.Recover(); err != nil {
		t.Fatal(err)
	}
	if len(fs.calls) != 0 || h.live[key("p", "c1")] == nil {
		t.Errorf("a legacy turn: stopper calls %v, adopted=%v", fs.calls, h.live[key("p", "c1")] != nil)
	}
}

// blockingStopper stops nothing until released; it tells the test when it has been called and what the database said
// about the turn's token at that moment.
type blockingStopper struct {
	called  chan struct{}
	release chan struct{}
	atCall  func()
}

func (b *blockingStopper) StopLeader(int, string) procbind.StopResult {
	if b.atCall != nil {
		b.atCall()
	}
	close(b.called)
	<-b.release
	return procbind.StopResult{Outcome: procbind.StopStopped}
}

// While a leader is being stopped (up to two grace periods) the host's lock is free: other callers of the host are not
// blocked, and the session shows as held with the reason. The turn's token is already revoked when the stop starts.
func TestStoppingALeaderDoesNotHoldTheHostLockAndRevokesTheTokenFirst(t *testing.T) {
	h, _, st := newHost(t)
	id := v2Turn(t, st, "c1", "t1", 0, "", 4242, "bind-stamp")
	bs := &blockingStopper{called: make(chan struct{}), release: make(chan struct{})}
	var revokedAtStop bool
	bs.atCall = func() {
		rv, _ := procbind.IsRevoked(context.Background(), st.db, procbind.KindSession, id)
		revokedAtStop = rv
	}
	h.Stopper = bs
	done := make(chan error, 1)
	go func() { done <- h.Recover() }()
	select {
	case <-bs.called:
	case <-time.After(5 * time.Second):
		t.Fatal("the leader was never stopped")
	}
	got := make(chan map[string]string, 1)
	go func() { got <- h.Held() }() // takes the host lock
	select {
	case held := <-got:
		if len(held) != 1 {
			t.Errorf("held while stopping = %v", held)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("the host lock is held while the leader is being stopped")
	}
	close(bs.release)
	if err := <-done; err != nil {
		t.Fatal(err)
	}
	if !revokedAtStop {
		t.Error("the token was not revoked before the leader was stopped")
	}
	if len(h.Held()) != 0 {
		t.Errorf("still held after the stop: %v", h.Held())
	}
}

type failingChildTokens struct{ err error }

func (f failingChildTokens) RevokeChildTokens(string, string) error { return f.err }

// A child's task tokens that cannot be revoked hold the session: it is not settled while a token of the run lives.
func TestAChildWhoseTaskTokensCannotBeRevokedStaysHeld(t *testing.T) {
	h, l, st := newHost(t)
	v2Turn(t, st, "c1", "t1", 0, "", 4242, "bind-stamp")
	if _, err := st.db.Exec(`UPDATE coder_sessions SET role = 'child', parent_conversation_id = 'm' WHERE conversation_id = 'c1'`); err != nil {
		t.Fatal(err)
	}
	h.Stopper = &fakeStopper{results: []procbind.StopResult{{Outcome: procbind.StopStopped}}}
	h.ChildTokens = failingChildTokens{err: errors.New("database is locked")}
	if err := h.Recover(); err != nil {
		t.Fatal(err)
	}
	if x := state(t, st, "c1"); x.TurnID != "t1" || x.State != StateRunning {
		t.Fatalf("the session was settled with a live task token: %+v", x)
	}
	if len(l.finished) != 0 || len(h.Held()) != 1 {
		t.Errorf("finished=%v held=%v", l.finished, h.Held())
	}
	h.ChildTokens = failingChildTokens{} // the revoke works now: the next pass settles it
	if err := h.Tick(); err != nil {
		t.Fatal(err)
	}
	if x := state(t, st, "c1"); x.TurnID != "" {
		t.Errorf("the session was not settled once its tokens could be revoked: %+v", x)
	}
}

// The production stopper reads a leader's stamp with its boot id and compares it the boot-aware way.
func TestTheProductionStopperKnowsTheBootId(t *testing.T) {
	h, _, _ := newHost(t)
	s, ok := h.leaderStopper().(procbind.Stopper)
	if !ok || s.Same == nil || s.StampOf == nil {
		t.Fatalf("the production stopper is %#v", h.leaderStopper())
	}
	if !s.Same("start:5", "start:5@boot") || s.Same("start:5@a", "start:5@b") {
		t.Error("the production stopper does not compare the boot id")
	}
}

// The stop of a mission child must not settle a session whose bound leader is held: the leader may still be running, and
// settling would let a new turn start next to it. Recovery alone ends the hold.
func TestStoppingAChildDoesNotSettleAHeldBoundTurn(t *testing.T) {
	h, l, st := newHost(t)
	v2Turn(t, st, "c1", "t1", 0, "", 4242, "bind-stamp")
	if _, err := st.db.Exec(`UPDATE coder_sessions SET role = 'child', parent_conversation_id = 'm' WHERE conversation_id = 'c1'`); err != nil {
		t.Fatal(err)
	}
	h.Stopper = &fakeStopper{results: []procbind.StopResult{{Outcome: procbind.StopHeld, Cause: "the turn's process did not exit after SIGKILL"}}}
	if err := h.Recover(); err != nil {
		t.Fatal(err)
	}
	if len(h.Held()) != 1 {
		t.Fatalf("held = %v", h.Held())
	}
	h.stopChild("p", "m", "c1")
	if x := state(t, st, "c1"); x.TurnID != "t1" || x.State != StateRunning {
		t.Fatalf("the held bound turn was settled by the child's stop: %+v", x)
	}
	if len(l.finished) != 0 || len(h.Held()) != 1 {
		t.Errorf("finished=%v held=%v", l.finished, h.Held())
	}
}
