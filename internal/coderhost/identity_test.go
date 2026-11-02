package coderhost

import (
	"errors"
	"os"
	"testing"

	"github.com/monoes/mono-agent/internal/procbind"
)

// RecordTurnProcess writes the identity of the CURRENT turn's process once; it never touches a newer turn's row, and repeating the
// same record is a no-op.
func TestRecordTurnProcessWritesOnlyTheCurrentTurnOnce(t *testing.T) {
	_, _, st := newHost(t)
	x, err := st.EnsureSession("p", "c1")
	if err != nil {
		t.Fatal(err)
	}
	x.State, x.TurnID = StateRunning, "t1"
	if err := st.SaveSession(x); err != nil {
		t.Fatal(err)
	}
	if err := st.RecordTurnProcess("p", "c1", "t1", 777, "stamp-a", false); err != nil {
		t.Fatal(err)
	}
	if got := state(t, st, "c1"); got.TurnPID != 777 || got.TurnStamp != "stamp-a" {
		t.Fatalf("not recorded: %+v", got)
	}
	if err := st.RecordTurnProcess("p", "c1", "t1", 777, "stamp-a", false); err != nil {
		t.Errorf("repeating the same record: %v", err)
	}
	if err := st.RecordTurnProcess("p", "c1", "t1", 888, "stamp-b", false); err == nil {
		t.Error("a second, different process was recorded over the first")
	}
	if err := st.RecordTurnProcess("p", "c1", "t0", 999, "stamp-c", false); err == nil { // an older turn's process
		t.Error("an older turn's process was recorded on the session")
	}
	if got := state(t, st, "c1"); got.TurnPID != 777 || got.TurnStamp != "stamp-a" || got.TurnID != "t1" {
		t.Errorf("the row was changed: %+v", got)
	}
	if err := st.RecordTurnProcess("p", "c1", "t1", 1, "x", false); err == nil {
		t.Error("pid 1 was accepted")
	}
	// a process of a turn that is not the session's current one is refused even while the row has no process yet
	if _, err := st.EnsureSession("p", "c2"); err != nil {
		t.Fatal(err)
	}
	y := state(t, st, "c2")
	y.State, y.TurnID = StateRunning, "t9"
	if err := st.SaveSession(y); err != nil {
		t.Fatal(err)
	}
	if err := st.RecordTurnProcess("p", "c2", "t8", 555, "stamp-d", false); err == nil {
		t.Error("the process of another turn was recorded on a session that has no process yet")
	}
	if got := state(t, st, "c2"); got.TurnPID != 0 {
		t.Errorf("the row was changed: %+v", got)
	}
}

// The process of a start that then failed (before the host saved it itself) was recorded by the launcher: a host that restarts during
// the hold ADOPTS it and starts no queued turn beside it; it settles only when the process actually ends.
func TestARestartDuringAHoldAdoptsTheProcessTheLauncherRecorded(t *testing.T) {
	h, l, st := newHost(t)
	if _, err := st.EnsureSession("p", "c1"); err != nil {
		t.Fatal(err)
	}
	if _, err := st.IssueSessionToken("p", "c1", "t1"); err != nil {
		t.Fatal(err)
	}
	l.onStart = func() { // what the launcher does the moment its process exists, before the start fails
		if err := st.RecordTurnProcess("p", "c1", "t1", 777, "stamp", false); err != nil {
			t.Error(err)
		}
	}
	l.startErr = &TokensLiveError{Err: errors.New("the turn had not ended"), Ended: make(chan struct{})}
	send(t, st, "c1", "t1", "first")
	if err := h.Tick(); err != nil {
		t.Fatal(err)
	}
	if len(h.Held()) != 1 {
		t.Fatalf("held = %v", h.Held())
	}
	// the host restarts: a new Host over the same database, the old process still alive
	l2 := newFakeLauncher()
	old := newFakeTurn(777)
	l2.alive[777] = old
	h2 := &Host{Store: st, L: l2}
	if err := h2.Recover(); err != nil {
		t.Fatal(err)
	}
	if h2.live[key("p", "c1")] == nil {
		t.Fatal("the restarted host did not adopt the old process")
	}
	if got := state(t, st, "c1"); got.TurnID != "t1" || got.State != StateRunning {
		t.Fatalf("the restarted host settled the session beside the old process: %+v", got)
	}
	if legacyTokens(t, st, "c1") != 1 {
		t.Error("the old turn's token was revoked while its process still runs")
	}
	send(t, st, "c1", "t2", "queued")
	if err := h2.Tick(); err != nil {
		t.Fatal(err)
	}
	if len(l2.started) != 0 {
		t.Fatalf("a queued turn started beside the old process: %v", l2.started)
	}
	old.end() // the old process ends
	h2.Wait()
	if err := h2.Tick(); err != nil {
		t.Fatal(err)
	}
	if len(l2.started) != 1 {
		t.Errorf("the queued turn did not start after the old process ended: %v", l2.started)
	}
	l2.turn("t2").end()
	h2.Wait()
}

// pid 0 and negative pids, and a session that has no row, are refused: nothing is written.
func TestRecordTurnProcessRefusesBadInputAndEmptyRows(t *testing.T) {
	_, _, st := newHost(t)
	if _, err := st.EnsureSession("p", "c0"); err != nil { // a row WITH a current turn and no process: only the pid guard can reject these
		t.Fatal(err)
	}
	z := state(t, st, "c0")
	z.State, z.TurnID = StateRunning, "t1"
	if err := st.SaveSession(z); err != nil {
		t.Fatal(err)
	}
	for _, pid := range []int{0, -1, 1} {
		if err := st.RecordTurnProcess("p", "c0", "t1", pid, "stamp", false); err == nil {
			t.Errorf("pid %d was accepted", pid)
		}
	}
	if got := state(t, st, "c0"); got.TurnPID != 0 || got.TurnStamp != "" {
		t.Errorf("the row was changed by a refused record: %+v", got)
	}
	if err := st.RecordTurnProcess("p", "no-such-conversation", "t1", 777, "stamp", false); err == nil {
		t.Error("a session with no row accepted a process")
	}
	if _, err := st.EnsureSession("p", "c1"); err != nil { // a row with no current turn
		t.Fatal(err)
	}
	if err := st.RecordTurnProcess("p", "c1", "t1", 777, "stamp", false); err == nil {
		t.Error("a session with no current turn accepted a process")
	}
	if got := state(t, st, "c1"); got.TurnPID != 0 || got.TurnStamp != "" {
		t.Errorf("the row was changed: %+v", got)
	}
}

// A bound launch's process was recorded before its token existed and the host died: recovery STOPS it (by its identity, as for any
// bound turn) and settles; it does not adopt it as a legacy turn.
func TestRecoveryStopsABoundLaunchRecordedBeforeItsTokenExisted(t *testing.T) {
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
	l.alive[4242] = newFakeTurn(4242) // the legacy path WOULD adopt this
	fs := &fakeStopper{results: []procbind.StopResult{{Outcome: procbind.StopStopped}}}
	h.Stopper = fs
	if err := h.Recover(); err != nil {
		t.Fatal(err)
	}
	if len(fs.calls) != 1 || fs.calls[0] != [2]any{4242, "start:99"} {
		t.Errorf("the stopper was called with %v, want the recorded pid and the plain stamp", fs.calls)
	}
	if h.live[key("p", "c1")] != nil {
		t.Error("a bound launch was adopted as a legacy turn")
	}
	if got := state(t, st, "c1"); got.TurnID != "" || got.State != StateIdle {
		t.Errorf("not settled after the stop: %+v", got)
	}
}

// A bound launch whose process cannot be confirmed gone is held, not settled.
func TestRecoveryHoldsABoundLaunchItCannotConfirmGone(t *testing.T) {
	h, _, st := newHost(t)
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
	h.Stopper = &fakeStopper{results: []procbind.StopResult{{Outcome: procbind.StopHeld, Cause: "it did not exit after SIGKILL"}}}
	if err := h.Recover(); err != nil {
		t.Fatal(err)
	}
	if got := state(t, st, "c1"); got.TurnID != "t1" || len(h.Held()) != 1 {
		t.Errorf("the session was settled with a bound leader that may still run: %+v held=%v", got, h.Held())
	}
}

// The bound flag is stored with the identity and is not part of the stamp; a repeat with the other flag is a different record.
func TestTheBoundFlagIsRecordedNextToTheIdentityNotInTheStamp(t *testing.T) {
	_, _, st := newHost(t)
	x, err := st.EnsureSession("p", "c1")
	if err != nil {
		t.Fatal(err)
	}
	x.State, x.TurnID = StateRunning, "t1"
	if err := st.SaveSession(x); err != nil {
		t.Fatal(err)
	}
	if err := st.RecordTurnProcess("p", "c1", "t1", 777, "start:5", true); err != nil {
		t.Fatal(err)
	}
	got := state(t, st, "c1")
	if got.TurnStamp != "start:5" || !got.TurnBound || got.TurnPID != 777 {
		t.Fatalf("recorded %+v", got)
	}
	if err := st.RecordTurnProcess("p", "c1", "t1", 777, "start:5", false); err == nil {
		t.Error("the same process was recorded again with the other flag")
	}
	// the host's own save after a successful start clears the flag (the v2 rows say what the turn is from then on)
	got.TurnBound = false
	if err := st.SaveSession(got); err != nil {
		t.Fatal(err)
	}
	if again := state(t, st, "c1"); again.TurnBound {
		t.Error("the flag survived a save that cleared it")
	}
}

// A pre-token bound hold is NOT released at the next tick: with no v2 row to find, the session stays held, and the leader is stopped
// again, until it is confirmed gone. A queued send does not start beside it meanwhile.
func TestAPreTokenBoundHoldSurvivesTheNextTickAndBlocksTheQueue(t *testing.T) {
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
	fs := &fakeStopper{results: []procbind.StopResult{
		{Outcome: procbind.StopHeld, Cause: "it did not exit"}, // at recovery
		{Outcome: procbind.StopHeld, Cause: "it did not exit"}, // at the next tick
		{Outcome: procbind.StopStopped},                        // later
	}}
	h.Stopper = fs
	if err := h.Recover(); err != nil {
		t.Fatal(err)
	}
	send(t, st, "c1", "t2", "queued")
	if err := h.Tick(); err != nil { // the retry: still held
		t.Fatal(err)
	}
	h.WaitRetries() // the leader retry runs off the pass
	if len(h.Held()) != 1 || len(l.started) != 0 {
		t.Fatalf("after the next tick: held=%v started=%v", h.Held(), l.started)
	}
	if got := state(t, st, "c1"); got.TurnID != "t1" {
		t.Fatalf("the session was settled while the leader may be running: %+v", got)
	}
	for _, c := range fs.calls {
		if c != [2]any{4242, "start:99"} {
			t.Errorf("the stopper was called with %v, want the recorded pid and the plain stamp", c)
		}
	}
	if err := h.Tick(); err != nil { // the leader is confirmed gone: settled
		t.Fatal(err)
	}
	h.WaitRetries()                  // the leader retry runs off the pass
	if err := h.Tick(); err != nil { // and the queued send runs
		t.Fatal(err)
	}
	if len(h.Held()) != 0 || len(l.started) != 1 {
		t.Errorf("after the leader was confirmed gone: held=%v started=%v", h.Held(), l.started)
	}
	l.turn("t2").end()
	h.Wait()
}

// The recorded identity of a bound turn is the plain stamp, so the ancestry check that decides whether a caller descends from the turn
// (operator-only task actions are refused inside a turn) still sees it.
func TestADescendantOfARecordedBoundTurnIsInATurn(t *testing.T) {
	_, _, st := newHost(t)
	x, err := st.EnsureSession("p", "c1")
	if err != nil {
		t.Fatal(err)
	}
	x.State, x.TurnID = StateRunning, "t1"
	if err := st.SaveSession(x); err != nil {
		t.Fatal(err)
	}
	pp := os.Getppid()
	if err := st.RecordTurnProcess("p", "c1", "t1", pp, ProcStamp(pp), true); err != nil {
		t.Fatal(err)
	}
	orig := callerChain
	t.Cleanup(func() { callerChain = orig })
	callerChain = func() ([]int, bool) { return []int{os.Getpid(), 7, pp, 3}, true } // env cleared: only the process tree is left to go by
	in, err := st.CallerInATurn()
	if err != nil || !in {
		t.Fatalf("a descendant of a recorded bound turn: in=%v err=%v", in, err)
	}
	callerChain = func() ([]int, bool) { return []int{os.Getpid(), 7, 3}, true }
	if in, err := st.CallerInATurn(); err != nil || in {
		t.Fatalf("a caller outside every turn: in=%v err=%v", in, err)
	}
}
