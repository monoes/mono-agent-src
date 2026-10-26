package coderhost

import (
	"errors"
	"strings"
	"testing"
	"time"
)

func settled(t *testing.T, h *Host, st *Store, convs ...string) {
	t.Helper()
	waitFor(t, "turns to settle", func() bool {
		h.mu.Lock()
		defer h.mu.Unlock()
		for _, c := range convs {
			if h.live[key("p", c)] != nil {
				return false
			}
		}
		return true
	})
}

func pendingIDs(t *testing.T, st *Store, conv string) []string {
	t.Helper()
	cmds, _ := st.Pending()
	var out []string
	for _, c := range cmds {
		if c.ConversationID == conv {
			out = append(out, c.TurnID)
		}
	}
	return out
}

func enqueue(t *testing.T, st *Store, c Command) Command {
	t.Helper()
	c.ProfileID = "p"
	got, err := st.Enqueue(c)
	if err != nil {
		t.Fatal(err)
	}
	return got
}

func TestMaxConcurrentCountsTurnsAdoptedAfterRecover(t *testing.T) {
	h, l, st := newHost(t)
	_, kids := family(t, st, 2)
	setMaxConcurrent(t, st, "m", 1)
	x, _ := st.EnsureSession("p", kids[0])
	x.State, x.TurnID, x.TurnPID, x.TurnStamp = StateRunning, "old", 500, "stamp"
	if err := st.SaveSession(x); err != nil {
		t.Fatal(err)
	}
	l.alive[500] = newFakeTurn(500)
	if err := h.Recover(); err != nil {
		t.Fatal(err)
	}
	send(t, st, kids[1], "n1", "x")
	_ = h.Tick()
	if len(startedConvs(l)) != 0 {
		t.Fatalf("started %q beside an adopted turn at the cap", startedConvs(l))
	}
	l.alive[500].end()
	settled(t, h, st, kids[0])
	_ = h.Tick()
	if len(startedConvs(l)) != 1 {
		t.Fatalf("started %q after the adopted turn ended", startedConvs(l))
	}
}

func TestMaxConcurrentHoldsMasterAndAnswerSendsAndNeverAMastersCheckin(t *testing.T) {
	h, l, st := newHost(t)
	_, kids := family(t, st, 3)
	setMaxConcurrent(t, st, "m", 1)
	send(t, st, kids[0], "run1", "x")
	enqueue(t, st, Command{ConversationID: kids[1], Kind: KindSend, TurnID: MasterTurnPrefix + "a", Message: "from the master", Issuer: "m"})
	enqueue(t, st, Command{ConversationID: kids[2], Kind: KindSend, TurnID: answerTurnID(answerFromDefault), Message: "a default"})
	_ = h.Tick()
	if got := startedConvs(l); len(got) != 1 || !strings.HasPrefix(got[0], "run1:") {
		t.Fatalf("started %q", got)
	}
	if len(pendingIDs(t, st, kids[1])) != 1 || len(pendingIDs(t, st, kids[2])) != 1 {
		t.Fatal("a held master- or answer- send is not pending")
	}
	l.turn("run1").end()
	settled(t, h, st, kids[0])
	_ = h.Tick()
	if got := startedConvs(l); len(got) != 2 || !strings.HasPrefix(got[1], MasterTurnPrefix+"a:") {
		t.Fatalf("started %q: the master's send (queued first) takes the slot", got)
	}
	// A master is not a child: neither the cap nor an unreadable count holds it.
	if h.childSlotFull(Session{Role: "master"}, "", &childRunning{err: errors.New("x")}) {
		t.Fatal("a master was held")
	}
}

func TestStopAndPauseOnAHeldChatStillRun(t *testing.T) {
	h, l, st := newHost(t)
	_, kids := family(t, st, 3)
	setMaxConcurrent(t, st, "m", 1)
	send(t, st, kids[0], "run1", "x")
	send(t, st, kids[1], "held1", "x")
	send(t, st, kids[2], "held2", "x")
	_ = h.Tick()
	stop := enqueue(t, st, Command{ConversationID: kids[1], Kind: KindStop})
	pause := enqueue(t, st, Command{ConversationID: kids[2], Kind: KindPause})
	_ = h.Tick()
	if c, _, _ := st.GetCommand(stop.ID); c.Status != CmdDone {
		t.Fatalf("stop = %+v", c)
	}
	if c, _, _ := st.GetCommand(pause.ID); c.Status != CmdDone || state(t, st, kids[2]).State != StatePaused {
		t.Fatalf("pause = %+v, state %s", c, state(t, st, kids[2]).State)
	}
	if len(startedConvs(l)) != 1 {
		t.Fatalf("started %q", startedConvs(l))
	}
	if len(pendingIDs(t, st, kids[1])) != 0 {
		t.Fatal("the stopped chat still has its held send")
	}
}

func TestMaxConcurrentGivesTheSlotToTheEarlierSendWhoeverQueuedIt(t *testing.T) {
	h, l, st := newHost(t)
	_, kids := family(t, st, 2)
	setMaxConcurrent(t, st, "m", 1)
	send(t, st, kids[1], "person-b", "typed first")                                                                                  // B, by a person
	enqueue(t, st, Command{ConversationID: kids[0], Kind: KindSend, TurnID: MasterTurnPrefix + "a", Message: "second", Issuer: "m"}) // A, by the master
	_ = h.Tick()
	if got := startedConvs(l); len(got) != 1 || !strings.HasPrefix(got[0], "person-b:") {
		t.Fatalf("started %q: the earlier send takes the only slot", got)
	}
}

func TestAHeldSendExpiresAsDocumentedAndAnExpiredAnswerNoteIsNotAFailure(t *testing.T) {
	h, _, st := newHost(t)
	clk := &clock{t: t0}
	st.now = clk.now
	_, kids := family(t, st, 2)
	setMaxConcurrent(t, st, "m", 1)
	send(t, st, kids[0], "run1", "x")
	held := enqueue(t, st, Command{ConversationID: kids[1], Kind: KindSend, TurnID: MasterTurnPrefix + "a", Message: "held", Issuer: "m"})
	person := send(t, st, kids[1], "person-held", "typed")
	// A question the master answers: its note is held by the cap too.
	q, err := st.Park("p", kids[1], "scope_growth", "wider?", nil, "", false)
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err := st.Answer("p", q.ID, "yes", masterScope{"m"}); err != nil {
		t.Fatal(err)
	}
	_ = h.Tick()
	clk.t = clk.t.Add(MaxCheckinAge + time.Minute)
	_ = h.Tick() // expires what the cap held past 15 minutes
	_ = h.Tick() // gives the question back
	if c, _, _ := st.GetCommand(held.ID); c.Status != CmdFailed || !strings.HasPrefix(c.Error, expiredMarker) {
		t.Fatalf("a held master send after 15 minutes = %+v", c)
	}
	if c, _, _ := st.GetCommand(person.ID); c.Status != CmdPending {
		t.Fatalf("a held person's send after 15 minutes = %+v", c)
	}
	got, _ := st.GetQuestion("p", q.ID)
	if n, _ := st.reopenCount(q.ID); got.Status != questionStatusOpen || n != 0 {
		t.Fatalf("the expired answer note: status %s, reopen count %d (want open, 0)", got.Status, n)
	}
	clk.t = clk.t.Add(MaxSendAge)
	_ = h.Tick()
	if c, _, _ := st.GetCommand(person.ID); c.Status != CmdFailed {
		t.Fatalf("a held person's send after 6 hours = %+v", c)
	}
}

func TestMaxConcurrentFailsClosedWhenTheMastersSettingsDoNotRead(t *testing.T) {
	h, l, st := newHost(t)
	_, kids := family(t, st, 1)
	if _, err := st.db.Exec(`UPDATE coder_sessions SET settings = '{broken' WHERE conversation_id = 'm'`); err != nil {
		t.Fatal(err)
	}
	send(t, st, kids[0], "c1", "x")
	_ = h.Tick()
	if len(startedConvs(l)) != 0 {
		t.Fatalf("started %q with unreadable settings", startedConvs(l))
	}
	if err := st.SaveSettings("p", "m", DefaultSettings()); err != nil {
		t.Fatal(err)
	}
	_ = h.Tick()
	if len(startedConvs(l)) != 1 {
		t.Fatalf("started %q once the settings read again", startedConvs(l))
	}
	// an unreadable count holds a child too, and only a child
	if !h.childSlotFull(Session{ProfileID: "p", Role: "child", Parent: "m"}, "k", &childRunning{err: errors.New("x")}) {
		t.Fatal("a child was not held when the count could not be read")
	}
}

func TestHeldSendsDoNotHideAStopBehindThemInTheWindow(t *testing.T) {
	h, l, st := newHost(t)
	addConversation(t, st, "m", "/work/m")
	cfg := DefaultSettings()
	cfg.MaxConcurrent, cfg.MaxChildren = 1, 10
	if err := st.SaveSettings("p", "m", cfg); err != nil {
		t.Fatal(err)
	}
	if err := st.SetRole("p", "m", "master", ""); err != nil {
		t.Fatal(err)
	}
	var kids []string
	for _, k := range []string{"k1", "k2", "k3", "k4", "k5", "k6"} {
		addConversation(t, st, k, "/work/"+k)
		if err := st.SetRole("p", k, "child", "m"); err != nil {
			t.Fatal(err)
		}
		kids = append(kids, k)
	}
	addConversation(t, st, "solo", "/work/solo")
	send(t, st, kids[0], "run1", "x")
	send(t, st, "solo", "solo1", "x")
	_ = h.Tick()
	for _, k := range kids[1:] {
		for i := 0; i < MaxPendingPerChat; i++ {
			send(t, st, k, k+"-"+string(rune('a'+i%26))+string(rune('a'+i/26)), "x")
		}
	}
	_ = h.Tick() // all held: 250 sends, more than the window
	stop := enqueue(t, st, Command{ConversationID: "solo", Kind: KindStop})
	_ = h.Tick()
	if c, _, _ := st.GetCommand(stop.ID); c.Status != CmdDone {
		t.Fatalf("a stop queued behind %d held sends = %+v", (len(kids)-1)*MaxPendingPerChat, c)
	}
	if len(startedConvs(l)) != 2 {
		t.Fatalf("started %d turns", len(startedConvs(l)))
	}
}
