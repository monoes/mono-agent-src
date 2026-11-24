package coderhost

import (
	"context"
	"testing"

	"github.com/monoes/mono-agent/internal/procbind"
)

// #1743: a restart must ADOPT a running bound turn, as it does with binding off. Killing the leader's group left the
// agent (its own process groups) running while the next send started a second one for the same conversation.
func TestARestartAdoptsARunningBoundTurnAndTheNextSendWaits(t *testing.T) {
	readableStamps(t)
	h, l, st := newHost(t)
	id := v2Turn(t, st, "c1", "t1", 4242, "stamp", 4242, "bind-stamp") // the session recorded the launcher's identity
	live := newFakeTurn(4242)
	l.alive[4242] = live
	fs := &fakeStopper{results: []procbind.StopResult{{Outcome: procbind.StopStopped}}}
	h.Stopper = fs
	if err := h.Recover(); err != nil {
		t.Fatal(err)
	}
	if len(fs.calls) != 0 {
		t.Fatalf("a running bound turn was stopped: %v", fs.calls)
	}
	if h.live[key("p", "c1")] == nil {
		t.Fatal("the bound turn was not adopted")
	}
	if x := state(t, st, "c1"); x.State != StateRunning || x.TurnID != "t1" {
		t.Fatalf("session = %+v", x)
	}
	if len(l.finished) != 0 {
		t.Fatalf("the adopted turn was finished: %v", l.finished)
	}
	// the old host's generation is gone: the adopted turn holds no usable credential
	if rv, err := procbind.IsRevoked(context.Background(), st.db, procbind.KindSession, id); err != nil || !rv {
		t.Fatalf("the adopted turn's token was left live: %v %v", rv, err)
	}
	send(t, st, "c1", "t2", "say hello")
	_ = h.Tick()
	if len(l.started) != 0 {
		t.Fatalf("a second agent was started for the conversation: %v", l.started)
	}
	live.end()
	h.Wait()
	_ = h.Tick()
	if len(l.started) != 1 || len(h.Held()) != 0 {
		t.Fatalf("started = %v held = %v, want the queued turn once the adopted one ended", l.started, h.Held())
	}
}

// A bound turn whose process is gone (the launcher cannot adopt it) is still settled through the stop path.
func TestARestartStillSettlesABoundTurnWhoseProcessIsGone(t *testing.T) {
	readableStamps(t)
	h, l, st := newHost(t)
	v2Turn(t, st, "c1", "t1", 4242, "stamp", 4242, "bind-stamp") // nothing alive
	fs := &fakeStopper{results: []procbind.StopResult{{Outcome: procbind.StopAbsent}}}
	h.Stopper = fs
	if err := h.Recover(); err != nil {
		t.Fatal(err)
	}
	if len(fs.calls) != 1 || h.live[key("p", "c1")] != nil {
		t.Fatalf("stopper calls %v, adopted %v", fs.calls, h.live[key("p", "c1")] != nil)
	}
	if x := state(t, st, "c1"); x.TurnID != "" || len(l.finished) != 1 {
		t.Fatalf("session = %+v finished = %v", x, l.finished)
	}
}

// readableStamps makes every pid's start stamp readable for the test.
func readableStamps(t *testing.T) {
	t.Helper()
	prev := boundStamp
	boundStamp = func(int) string { return "stamp" }
	t.Cleanup(func() { boundStamp = prev })
}

// A process whose start time cannot be read cannot be told from a later one that reused its pid: not adopted, stopped as before.
func TestARestartDoesNotAdoptAProcessWhoseStampCannotBeRead(t *testing.T) {
	prev := boundStamp
	boundStamp = func(int) string { return "" }
	t.Cleanup(func() { boundStamp = prev })
	h, l, st := newHost(t)
	v2Turn(t, st, "c1", "t1", 4242, "stamp", 4242, "bind-stamp")
	l.alive[4242] = newFakeTurn(4242)
	fs := &fakeStopper{results: []procbind.StopResult{{Outcome: procbind.StopStopped}}}
	h.Stopper = fs
	if err := h.Recover(); err != nil {
		t.Fatal(err)
	}
	if h.live[key("p", "c1")] != nil || len(fs.calls) == 0 {
		t.Fatalf("adopted %v, stopper calls %v: want the old stop path", h.live[key("p", "c1")] != nil, fs.calls)
	}
}

// An adopted mission child keeps no task token, as when its turn is stopped; a token that cannot be ended means no adoption.
func TestARestartRevokesTheTaskTokensOfAnAdoptedChild(t *testing.T) {
	readableStamps(t)
	h, l, st := newHost(t)
	v2Turn(t, st, "c1", "t1", 4242, "stamp", 4242, "bind-stamp")
	if _, err := st.EnsureSession("p", "m1"); err != nil {
		t.Fatal(err)
	}
	if err := st.SetRole("p", "m1", "master", ""); err != nil {
		t.Fatal(err)
	}
	if err := st.SetRole("p", "c1", "child", "m1"); err != nil {
		t.Fatal(err)
	}
	l.alive[4242] = newFakeTurn(4242)
	rv := &fakeRevoker{}
	h.ChildTokens = rv
	h.Stopper = &fakeStopper{results: []procbind.StopResult{{Outcome: procbind.StopStopped}}}
	if err := h.Recover(); err != nil {
		t.Fatal(err)
	}
	if h.live[key("p", "c1")] == nil || len(rv.calls) == 0 {
		t.Fatalf("adopted %v, child token revocations %v", h.live[key("p", "c1")] != nil, rv.calls)
	}
}
