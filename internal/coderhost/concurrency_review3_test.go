package coderhost

import (
	"errors"
	"strings"
	"sync"
	"testing"
	"time"
)

// A stop fails the chat's earlier sends in the database; the pass that did it must not
// then start one of them from its stale copy.
func TestAStopInThePassKeepsTheChatsEarlierHeldSendFromStarting(t *testing.T) {
	h, l, st := newHost(t)
	_, kids := family(t, st, 2)
	setMaxConcurrent(t, st, "m", 1)
	send(t, st, kids[0], "run", "x")
	_ = h.Tick()
	b1 := send(t, st, kids[1], "B1", "x")
	_ = h.Tick() // B held
	l.turn("run").end()
	settled(t, h, st, kids[0]) // the slot is free
	stop := enqueue(t, st, Command{ConversationID: kids[1], Kind: KindStop})
	_ = h.Tick()
	if c, _, _ := st.GetCommand(stop.ID); c.Status != CmdDone {
		t.Fatalf("stop = %+v", c)
	}
	if c, _, _ := st.GetCommand(b1.ID); c.Status != CmdFailed || !strings.Contains(c.Error, "stop") {
		t.Fatalf("the send before the stop = %+v: it must stay failed", c)
	}
	if got := startOrder(l); !sameOrder(got, []string{"run"}) {
		t.Fatalf("started %v: a turn started on a chat the operator just stopped", got)
	}
}

// The same for a send a master queued, when the master itself is stopped in the pass.
func TestAMastersStopInThePassKeepsItsHeldSendToAChildFromStarting(t *testing.T) {
	h, l, st := newHost(t)
	_, kids := family(t, st, 2)
	setMaxConcurrent(t, st, "m", 1)
	send(t, st, kids[0], "run", "x")
	_ = h.Tick()
	ms := enqueue(t, st, Command{ConversationID: kids[1], Kind: KindSend, TurnID: MasterTurnPrefix + "a", Message: "x", Issuer: "m"})
	_ = h.Tick() // held
	l.turn("run").end()
	settled(t, h, st, kids[0])
	enqueue(t, st, Command{ConversationID: "m", Kind: KindStop})
	_ = h.Tick()
	if c, _, _ := st.GetCommand(ms.ID); c.Status != CmdFailed || !strings.Contains(c.Error, "stopped") {
		t.Fatalf("the master's send = %+v", c)
	}
	if got := startOrder(l); !sameOrder(got, []string{"run"}) {
		t.Fatalf("started %v", got)
	}
}

// A note that expires every 15 minutes while the cap holds it is given back only
// MaxExpiredReopens times; after that the question stays parked for a person.
func TestAnAnswerNoteThatKeepsExpiringStopsBeingReopened(t *testing.T) {
	h, _, st := newHost(t)
	clk := &clock{t: t0}
	st.now = clk.now
	_, kids := family(t, st, 2)
	setMaxConcurrent(t, st, "m", 1)
	send(t, st, kids[0], "run", "x")
	_ = h.Tick()
	q, err := st.Park("p", kids[1], "scope_growth", "wider?", nil, "", false)
	if err != nil {
		t.Fatal(err)
	}
	for i := 0; i < MaxExpiredReopens; i++ {
		if _, _, err := st.Answer("p", q.ID, "yes", masterScope{"m"}); err != nil {
			t.Fatalf("answer %d: %v", i, err)
		}
		_ = h.Tick() // held by the cap
		clk.t = clk.t.Add(MaxCheckinAge + time.Minute)
		_ = h.Tick() // expires
		_ = h.Tick() // gives the question back
	}
	if n, _ := st.reopenCount(q.ID); n != 0 {
		t.Fatalf("expiries counted as failures: %d", n)
	}
	if _, _, err := st.Answer("p", q.ID, "yes", masterScope{"m"}); !errors.Is(err, ErrAnswerKeepsFailing) {
		t.Fatalf("answer after %d expiries = %v", MaxExpiredReopens, err)
	}
	// it is parked: still open and listed, for a person to settle (the master cannot answer it any more)
	if got, err := st.GetQuestion("p", q.ID); err != nil || got.Status != questionStatusOpen {
		t.Fatalf("question = %+v, %v", got, err)
	}
	if open, _ := st.ListQuestions("p", kids[1], true); len(open) != 1 {
		t.Fatalf("open questions = %d", len(open))
	}
}

// A read error in a re-check holds the command for the next pass; it does not fail it.
func TestAReadErrorInARecheckHoldsTheSendInsteadOfFailingIt(t *testing.T) {
	h, l, st := newHost(t)
	var mu sync.Mutex
	var lines []string
	h.Logf = func(f string, a ...any) { mu.Lock(); lines = append(lines, f); mu.Unlock() }
	_, kids := family(t, st, 1)
	if _, err := st.db.Exec(`UPDATE coder_sessions SET settings = '{broken' WHERE conversation_id = ?`, kids[0]); err != nil {
		t.Fatal(err)
	}
	note := enqueue(t, st, Command{ConversationID: kids[0], Kind: KindSend, TurnID: answerTurnID(answerFromDefault), Message: "a default"})
	ms := enqueue(t, st, Command{ConversationID: kids[0], Kind: KindSend, TurnID: MasterTurnPrefix + "a", Message: "from the master", Issuer: "m"})
	for i := 0; i < 3; i++ {
		_ = h.Tick()
	}
	for _, c := range []Command{note, ms} {
		if got, _, _ := st.GetCommand(c.ID); got.Status != CmdPending {
			t.Fatalf("%s = %+v: a failed read must not fail the send", c.TurnID, got)
		}
	}
	mu.Lock()
	n := strings.Count(strings.Join(lines, "\n"), "is held because")
	mu.Unlock()
	if len(startedConvs(l)) != 0 || n != 1 {
		t.Fatalf("started %q, logged %d times", startedConvs(l), n)
	}
	if err := st.SaveSettings("p", kids[0], DefaultSettings()); err != nil { // the read works again
		t.Fatal(err)
	}
	_ = h.Tick()
	if got := startOrder(l); len(got) != 1 || !strings.HasPrefix(got[0], AnswerPrefix+"d-") {
		t.Fatalf("started %v after the read worked again", got)
	}
}
