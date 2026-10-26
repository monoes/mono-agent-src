package coderhost

import (
	"strings"
	"sync"
	"testing"
)

func logged(h *Host) (func() string, func(string) int) {
	var mu sync.Mutex
	var lines []string
	h.Logf = func(f string, a ...any) {
		mu.Lock()
		defer mu.Unlock()
		lines = append(lines, f+strings.Repeat(" ", 0))
		for _, x := range a {
			if e, ok := x.(error); ok {
				lines = append(lines, e.Error())
			}
			if s, ok := x.(string); ok {
				lines = append(lines, s)
			}
		}
	}
	all := func() string { mu.Lock(); defer mu.Unlock(); return strings.Join(lines, "\n") }
	return all, func(sub string) int { return strings.Count(all(), sub) }
}

func breakSettings(t *testing.T, st *Store, conv string) {
	t.Helper()
	if _, err := st.db.Exec(`UPDATE coder_sessions SET settings = '{broken' WHERE conversation_id = ?`, conv); err != nil {
		t.Fatal(err)
	}
}

// (a) a master's send alone: its own re-check fails to read.
func TestAMasterSendIsHeldNotFailedWhenItsRecheckCannotRead(t *testing.T) {
	h, l, st := newHost(t)
	_, count := logged(h)
	_, kids := family(t, st, 1)
	breakSettings(t, st, kids[0])
	ms := enqueue(t, st, Command{ConversationID: kids[0], Kind: KindSend, TurnID: MasterTurnPrefix + "a", Message: "x", Issuer: "m"})
	for i := 0; i < 2; i++ {
		_ = h.Tick()
	}
	if c, _, _ := st.GetCommand(ms.ID); c.Status != CmdPending {
		t.Fatalf("master send = %+v", c)
	}
	if count("is held because") != 1 {
		t.Fatalf("logged %d times", count("is held because"))
	}
	if err := st.SaveSettings("p", kids[0], DefaultSettings()); err != nil {
		t.Fatal(err)
	}
	_ = h.Tick()
	if got := startOrder(l); len(got) != 1 || got[0] != MasterTurnPrefix+"a" {
		t.Fatalf("started %v", got)
	}
}

// (b) a check-in alone.
func TestACheckinIsHeldNotFailedWhenItsRecheckCannotRead(t *testing.T) {
	h, l, st := newHost(t)
	family(t, st, 1)
	breakSettings(t, st, "m")
	ci := enqueue(t, st, Command{ConversationID: "m", Kind: KindSend, TurnID: CheckinTurnPrefix + "a", Message: "[CHECK-IN] x"})
	for i := 0; i < 2; i++ {
		_ = h.Tick()
	}
	if c, _, _ := st.GetCommand(ci.ID); c.Status != CmdPending {
		t.Fatalf("check-in = %+v", c)
	}
	if err := st.SaveSettings("p", "m", DefaultSettings()); err != nil {
		t.Fatal(err)
	}
	_ = h.Tick()
	if got := startOrder(l); len(got) != 1 || got[0] != CheckinTurnPrefix+"a" {
		t.Fatalf("started %v", got)
	}
}

// (c) the issuer check cannot read the master's session.
func TestAnIssuerCheckThatCannotReadHoldsTheCommand(t *testing.T) {
	h, l, st := newHost(t)
	_, count := logged(h)
	_, kids := family(t, st, 1)
	ms := enqueue(t, st, Command{ConversationID: kids[0], Kind: KindSend, TurnID: MasterTurnPrefix + "a", Message: "x", Issuer: "m"})
	// a text where the session row wants a number: reading the master's session fails
	if _, err := st.db.Exec(`UPDATE coder_sessions SET turn_pid = 'abc' WHERE conversation_id = 'm'`); err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 2; i++ {
		_ = h.Tick()
	}
	if c, _, _ := st.GetCommand(ms.ID); c.Status != CmdPending {
		t.Fatalf("command = %+v: an unreadable master is not a gone master", c)
	}
	if count("master does not read") != 1 {
		t.Fatalf("logged %d times", count("master does not read"))
	}
	if _, err := st.db.Exec(`UPDATE coder_sessions SET turn_pid = 0 WHERE conversation_id = 'm'`); err != nil {
		t.Fatal(err)
	}
	_ = h.Tick()
	if got := startOrder(l); len(got) != 1 {
		t.Fatalf("started %v", got)
	}
}

// A fault that does not pass (settings that are not valid) is shown after MaxReadHolds passes and a
// minute, with the real reason; a busy database does not get that far.
func TestAPersistentReadFaultFailsTheCommandWithTheRealReason(t *testing.T) {
	h, _, st := newHost(t)
	clk := &clock{t: t0}
	st.now = clk.now
	_, kids := family(t, st, 1)
	breakSettings(t, st, kids[0])
	ms := enqueue(t, st, Command{ConversationID: kids[0], Kind: KindSend, TurnID: MasterTurnPrefix + "a", Message: "x", Issuer: "m"})
	for i := 0; i < 2*MaxReadHolds; i++ {
		_ = h.Tick() // many passes, but within the minute
	}
	if c, _, _ := st.GetCommand(ms.ID); c.Status != CmdPending {
		t.Fatalf("failed within the minute: %+v", c)
	}
	clk.t = clk.t.Add(2 * MinReadHold)
	_ = h.Tick()
	c, _, _ := st.GetCommand(ms.ID)
	if c.Status != CmdFailed || !strings.Contains(c.Error, "are not valid") || strings.Contains(c.Error, "\x00") {
		t.Fatalf("command = %+v", c)
	}
}

// runningChildren does not count a send that was ended after the pass loaded it.
func TestTheWaitingListSkipsASendThatIsNoLongerPending(t *testing.T) {
	h, _, st := newHost(t)
	_, kids := family(t, st, 2)
	live := send(t, st, kids[0], "w1", "x")
	gone := send(t, st, kids[1], "w2", "x")
	if err := st.SetCommand(gone.ID, CmdFailed, "stopped"); err != nil {
		t.Fatal(err)
	}
	r := h.runningChildren([]Command{live, gone})
	if w := r.waiting[key("p", "m")]; len(w) != 1 || w[0] != key("p", kids[0]) {
		t.Fatalf("waiting = %v", w)
	}
}

// A command another process failed between the re-read and the end keeps its status.
func TestACommandEndedElsewhereIsNotOverwrittenByTheStart(t *testing.T) {
	h, l, st := newHost(t)
	addConversation(t, st, "solo", "/work/solo")
	c := send(t, st, "solo", "t1", "x")
	l.onStart = func() { _ = st.SetCommand(c.ID, CmdFailed, "stopped by the operator elsewhere") }
	_ = h.Tick()
	got, _, _ := st.GetCommand(c.ID)
	if got.Status != CmdFailed || got.Error != "stopped by the operator elsewhere" {
		t.Fatalf("command = %+v", got)
	}
}
