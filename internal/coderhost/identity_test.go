package coderhost

import (
	"errors"
	"testing"
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
	if err := st.RecordTurnProcess("p", "c1", "t1", 777, "stamp-a"); err != nil {
		t.Fatal(err)
	}
	if got := state(t, st, "c1"); got.TurnPID != 777 || got.TurnStamp != "stamp-a" {
		t.Fatalf("not recorded: %+v", got)
	}
	if err := st.RecordTurnProcess("p", "c1", "t1", 777, "stamp-a"); err != nil {
		t.Errorf("repeating the same record: %v", err)
	}
	if err := st.RecordTurnProcess("p", "c1", "t1", 888, "stamp-b"); err == nil {
		t.Error("a second, different process was recorded over the first")
	}
	if err := st.RecordTurnProcess("p", "c1", "t0", 999, "stamp-c"); err == nil { // an older turn's process
		t.Error("an older turn's process was recorded on the session")
	}
	if got := state(t, st, "c1"); got.TurnPID != 777 || got.TurnStamp != "stamp-a" || got.TurnID != "t1" {
		t.Errorf("the row was changed: %+v", got)
	}
	if err := st.RecordTurnProcess("p", "c1", "t1", 1, "x"); err == nil {
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
	if err := st.RecordTurnProcess("p", "c2", "t8", 555, "stamp-d"); err == nil {
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
		if err := st.RecordTurnProcess("p", "c1", "t1", 777, "stamp"); err != nil {
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
