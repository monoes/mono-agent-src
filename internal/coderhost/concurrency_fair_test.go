package coderhost

import (
	"database/sql"
	"errors"
	"strings"
	"sync"
	"testing"
	"time"
)

func startOrder(l *fakeLauncher) []string {
	var out []string
	for _, s := range startedConvs(l) {
		out = append(out, turnOf(s))
	}
	return out
}

func sameOrder(got, want []string) bool { return strings.Join(got, ",") == strings.Join(want, ",") }

// A held chat keeps its place: the freed slot goes to the oldest waiting send of the
// master, not to the newer sends the pass reaches first.
func TestAFreedSlotGoesToTheOldestWaitingSendOfTheMaster(t *testing.T) {
	h, l, st := newHost(t)
	_, kids := family(t, st, 4) // k1 A, k2 B, k3 C, k4 runs
	setMaxConcurrent(t, st, "m", 1)
	send(t, st, kids[3], "run", "x")
	_ = h.Tick()
	send(t, st, kids[0], "A1", "x")
	send(t, st, kids[0], "A2", "x") // the same chat: after A1
	_ = h.Tick()                    // A is held, and remembered as held
	send(t, st, kids[1], "B1", "x")
	send(t, st, kids[2], "C1", "x")
	l.turn("run").end()
	settled(t, h, st, kids[3])
	_ = h.Tick()
	if got := startOrder(l); !sameOrder(got, []string{"run", "A1"}) {
		t.Fatalf("started %v: A, held first, takes the freed slot", got)
	}
	l.turn("A1").end()
	settled(t, h, st, kids[0])
	_ = h.Tick()
	if got := startOrder(l); !sameOrder(got, []string{"run", "A1", "A2"}) {
		t.Fatalf("started %v: A's second send is the oldest waiting now", got)
	}
	l.turn("A2").end()
	settled(t, h, st, kids[0])
	_ = h.Tick()
	l.turn("B1").end()
	settled(t, h, st, kids[1])
	_ = h.Tick()
	if got := startOrder(l); !sameOrder(got, []string{"run", "A1", "A2", "B1", "C1"}) {
		t.Fatalf("started %v: B then C, by id", got)
	}
}

// A master's send that its own paused master holds is not waiting for a slot, so it does
// not keep one from a person's send to a sibling.
func TestASendHeldByAPausedMasterDoesNotReserveASlot(t *testing.T) {
	h, l, st := newHost(t)
	_, kids := family(t, st, 3)
	setMaxConcurrent(t, st, "m", 1)
	send(t, st, kids[2], "run", "x")
	_ = h.Tick()
	enqueue(t, st, Command{ConversationID: kids[0], Kind: KindSend, TurnID: MasterTurnPrefix + "a", Message: "x", Issuer: "m"})
	send(t, st, kids[1], "person-b", "x")
	m := state(t, st, "m")
	m.State = StatePaused
	if err := st.SaveSession(m); err != nil {
		t.Fatal(err)
	}
	l.turn("run").end()
	settled(t, h, st, kids[2])
	_ = h.Tick()
	if got := startOrder(l); !sameOrder(got, []string{"run", "person-b"}) {
		t.Fatalf("started %v: the paused master's send must not hold the slot", got)
	}
}

// A pause keeps its place among the held chat's own sends; a stop cancels the sends before it
// and leaves the one after it.
func TestControlsOnAHeldChatKeepTheirPlaceAmongItsSends(t *testing.T) {
	h, l, st := newHost(t)
	_, kids := family(t, st, 3)
	setMaxConcurrent(t, st, "m", 1)
	send(t, st, kids[2], "run", "x")
	_ = h.Tick()
	send(t, st, kids[0], "A5", "x")
	_ = h.Tick() // A held
	pause := enqueue(t, st, Command{ConversationID: kids[0], Kind: KindPause})
	l.turn("run").end()
	settled(t, h, st, kids[2])
	_ = h.Tick()
	if got := startOrder(l); !sameOrder(got, []string{"run", "A5"}) {
		t.Fatalf("started %v: the pause (queued after the send) overtook it", got)
	}
	if c, _, _ := st.GetCommand(pause.ID); c.Status != CmdDone || state(t, st, kids[0]).State != StatePaused {
		t.Fatalf("pause = %+v, state %s", c, state(t, st, kids[0]).State)
	}

	// B: held send, a stop, a send after it, then resume: the stop cancels only the first.
	send(t, st, kids[1], "B1", "x")
	_ = h.Tick() // B held (A's turn still runs)
	before := send(t, st, kids[1], "B2", "x")
	stop := enqueue(t, st, Command{ConversationID: kids[1], Kind: KindStop})
	after := send(t, st, kids[1], "B3", "x")
	_ = h.Tick()
	if c, _, _ := st.GetCommand(stop.ID); c.Status != CmdDone {
		t.Fatalf("stop = %+v", c)
	}
	if c, _, _ := st.GetCommand(before.ID); c.Status != CmdFailed || !strings.Contains(c.Error, "stop") {
		t.Fatalf("a send before the stop = %+v", c)
	}
	if c, _, _ := st.GetCommand(after.ID); c.Status != CmdPending {
		t.Fatalf("a send after the stop = %+v: it must survive", c)
	}
}

func TestGetSettingsReturnsADatabaseErrorAndDefaultsOnlyForNoRow(t *testing.T) {
	_, _, st := newHost(t)
	if s, err := st.GetSettings("p", "nobody"); err != nil || s.MaxConcurrent != DefaultSettings().MaxConcurrent {
		t.Fatalf("no row = %+v, %v", s, err)
	}
	closed, _ := sql.Open("sqlite", ":memory:")
	closed.Close()
	if _, err := NewStore(closed).GetSettings("p", "m"); err == nil || errors.Is(err, sql.ErrNoRows) {
		t.Fatalf("a closed database gave %v", err)
	}
}

func TestAnUnreadableCapHoldsAndIsLoggedOncePerMasterPerHour(t *testing.T) {
	h, l, st := newHost(t)
	clk := &clock{t: t0}
	st.now = clk.now
	var mu sync.Mutex
	var lines []string
	h.Logf = func(f string, a ...any) { mu.Lock(); lines = append(lines, f); mu.Unlock() }
	_, kids := family(t, st, 1)
	if _, err := st.db.Exec(`UPDATE coder_sessions SET settings = '{broken' WHERE conversation_id = 'm'`); err != nil {
		t.Fatal(err)
	}
	send(t, st, kids[0], "c1", "x")
	for i := 0; i < 3; i++ {
		_ = h.Tick()
	}
	mu.Lock()
	n := strings.Count(strings.Join(lines, "\n"), "max_concurrent cannot be read")
	mu.Unlock()
	if len(startedConvs(l)) != 0 || n != 1 {
		t.Fatalf("started %q, logged %d times", startedConvs(l), n)
	}
	clk.t = clk.t.Add(time.Hour + time.Minute)
	_ = h.Tick()
	mu.Lock()
	n = strings.Count(strings.Join(lines, "\n"), "max_concurrent cannot be read")
	mu.Unlock()
	if n != 2 {
		t.Fatalf("logged %d times after an hour", n)
	}
	// the running-children read failing is logged and holds as well
	h.logCapUnreadable("running-children", errors.New("disk"))
	if !h.childSlotFull(Session{ProfileID: "p", Role: "child", Parent: "m"}, "k", &childRunning{err: errors.New("disk")}) {
		t.Fatal("not held")
	}
}
