package coderhost

import (
	"errors"
	"fmt"
	"os"
	"os/exec"
	"strings"
	"sync"
	"testing"
	"time"
)

// event journals an event of a turn; payload is the JSON text of the event.
func event(t *testing.T, st *Store, conv, turn string, seq int, typ, payload string) {
	t.Helper()
	if _, err := st.db.Exec(`INSERT INTO ai_chat_events (profile_id, conversation_id, turn_id, seq, created_at, version, type, payload) VALUES ('p', ?, ?, ?, 'now', 1, ?, ?)`,
		conv, turn, seq, typ, payload); err != nil {
		t.Fatal(err)
	}
}

func TestSpendIsTheLargestReportedTotalOfEachTurnAndIgnoresNonCosts(t *testing.T) {
	st := newStoreT(t)
	m, _ := master(t, st, "coding")
	c := m.ConversationID
	base := time.Now().Add(-time.Hour)
	addTurn(t, st, c, "t1", "completed", "x", base)
	event(t, st, c, "t1", 1, "usage.updated", `{"agentId":"","costUsd":3.0}`)
	event(t, st, c, "t1", 2, "usage.updated", `{"agentId":"","costUsd":1.0}`) // a lower final snapshot
	event(t, st, c, "t1", 3, "usage.updated", `{"agentId":"","costUsd":-50}`)
	event(t, st, c, "t1", 4, "usage.updated", `{"agentId":"","costUsd":"lots"}`)
	event(t, st, c, "t1", 5, "usage.updated", `{"agentId":"","costUsd":null}`)
	// a worker finished twice (re-emitted) counts once
	event(t, st, c, "t1", 6, "agent.finished", `{"agentId":"w1","costUsd":0.5}`)
	event(t, st, c, "t1", 7, "agent.finished", `{"agentId":"w1","costUsd":0.5}`)
	event(t, st, c, "t1", 8, "agent.finished", `{"agentId":"w2","costUsd":0.25}`)
	event(t, st, c, "t1", 9, "agent.finished", `{"agentId":"w2","costUsd":0.1}`) // re-emitted lower: the largest stands
	addTurn(t, st, c, "t3", "completed", "x", base.Add(2*time.Minute))
	event(t, st, c, "t3", 1, "usage.updated", `{"agentId":"","costUsd":-5}`) // only a negative: not a cost
	addTurn(t, st, c, "t2", "completed", "x", base.Add(time.Minute))
	event(t, st, c, "t2", 1, "usage.updated", `{"agentId":"","costUsd":2.0}`)
	if spent, err := st.Spent("p", c); err != nil || spent != 5.75 {
		t.Fatalf("spent = %v %v, want 3 + 2 + 0.5 + 0.25", spent, err)
	}
}

func TestARuntimeThatReportsNoCostCannotSpendMoreThanTheBudget(t *testing.T) {
	h, l, st := newHost(t)
	m, _ := master(t, st, "coding")
	c := m.ConversationID
	st.SetBudget("p", c, 2)
	started := 0
	for i := 0; i < 5; i++ { // turns that journal no cost at all
		cmd := send(t, st, c, fmt.Sprint("t-", i), "go")
		_ = h.Tick()
		if got, _, _ := st.GetCommand(cmd.ID); got.Status != CmdDone {
			break
		}
		started++
		l.turn(fmt.Sprint("t-", i)).end()
		waitFor(t, "the turn to settle", func() bool { return state(t, st, c).State == StateIdle })
		addTurn(t, st, c, fmt.Sprint("t-", i), "completed", "x", time.Now())
	}
	if started != 1 || len(l.caps) != 1 || l.caps[0] != 2 {
		t.Fatalf("%d turns ran under caps %v, want one turn under the whole $2", started, l.caps)
	}
	if spent, _ := st.Spent("p", c); spent != 2 {
		t.Fatalf("a turn with no cost report counts as its cap: spent = %v", spent)
	}
}

func TestAMasterCannotGiveAwayWhatItHasSpent(t *testing.T) {
	st := newStoreT(t)
	m, _ := master(t, st, "coding")
	c := m.ConversationID
	st.SetBudget("p", c, 10)
	addTurn(t, st, c, "t1", "completed", "x", time.Now().Add(-time.Hour))
	event(t, st, c, "t1", 1, "usage.updated", `{"agentId":"","costUsd":9}`)
	if _, err := st.Spawn(m, SpawnRequest{BudgetUSD: 10}); !errors.Is(err, ErrLimit) {
		t.Fatalf("10 of the 1 left: %v", err)
	}
	if _, err := st.Spawn(m, SpawnRequest{BudgetUSD: 1}); err != nil {
		t.Fatalf("the last 1: %v", err)
	}
	// the statement guards it too, whatever the early check said (a spend that landed in between)
	k := spawnUncappedOf(t, st, m)
	if err := st.carveBudget("p", c, k, 0.5); err == nil {
		t.Fatal("carveBudget gave away what the master does not have")
	}
}

func spawnUncappedOf(t *testing.T, st *Store, m Caller) string {
	t.Helper()
	s, _ := st.GetSettings("p", m.ConversationID)
	s.MaxChildren = 10
	st.SaveSettings("p", m.ConversationID, s)
	// an uncapped sibling would be refused under a capped master, so create the session by hand
	a := spawnRaw(t, st, m)
	return a
}

func spawnRaw(t *testing.T, st *Store, m Caller) string {
	t.Helper()
	id := fmt.Sprint("raw-", time.Now().UnixNano())
	st.EnsureSession("p", id)
	st.db.Exec(`UPDATE coder_sessions SET role = 'child', parent_conversation_id = ? WHERE conversation_id = ?`, m.ConversationID, id)
	return id
}

func TestASmallRemainderIsACleanBudgetRefusal(t *testing.T) {
	st := newStoreT(t)
	m, _ := master(t, st, "coding")
	c := m.ConversationID
	st.SetBudget("p", c, 1)
	addTurn(t, st, c, "t1", "completed", "x", time.Now().Add(-time.Hour))
	event(t, st, c, "t1", 1, "usage.updated", `{"agentId":"","costUsd":0.9999995}`)
	if _, _, err := st.TurnCap("p", c); !errors.Is(err, errBudgetUsedUp) {
		t.Fatalf("TurnCap = %v", err)
	}
}

func TestATokenPresentedOutsideTheTurnTreeSaysSoAndBindingNeedsAStamp(t *testing.T) {
	if !ancestrySupported {
		t.Skip("ancestry is not read here")
	}
	st := newStoreT(t)
	m, _ := master(t, st, "coding")
	running(t, st, m.ConversationID, "t1")
	tok, _ := st.IssueSessionToken("p", m.ConversationID, "t1")
	if err := st.BindSessionToken("p", m.ConversationID, "t1", os.Getpid(), ""); err == nil {
		t.Fatal("a token was bound without a start stamp")
	}
	sib := exec.Command("sleep", "30")
	if err := sib.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { sib.Process.Kill(); sib.Wait() })
	st.BindSessionToken("p", m.ConversationID, "t1", sib.Process.Pid, ProcStamp(sib.Process.Pid))
	_, err := st.VerifySessionToken(tok)
	if !errors.Is(err, ErrNotInTurnTree) || !errors.Is(err, ErrSessionToken) || !strings.Contains(err.Error(), "process tree") {
		t.Fatalf("err = %v", err)
	}
}

func TestAHostInsideASessionTurnDoesNotBindTokens(t *testing.T) {
	st := newStoreT(t)
	m, _ := master(t, st, "coding")
	running(t, st, m.ConversationID, "t1")
	st.IssueSessionToken("p", m.ConversationID, "t1")
	pp := os.Getppid()
	st.BindSessionToken("p", m.ConversationID, "t1", pp, ProcStamp(pp))
	orig := callerChain
	t.Cleanup(func() { callerChain = orig })
	callerChain = func() ([]int, bool) { return []int{os.Getpid(), 7, pp, 3}, true }
	if !st.HostInsideTurn() {
		t.Fatal("a host below a bound turn was not noticed")
	}
	callerChain = func() ([]int, bool) { return []int{os.Getpid(), 7, 3}, true }
	if st.HostInsideTurn() {
		t.Fatal("a host outside every turn was refused")
	}
}

func TestMasterCommandsShareOnePendingCapEvenWhenQueuedTogether(t *testing.T) {
	db1 := newStoreT(t)
	m, _ := master(t, db1, "coding")
	a := spawn(t, db1, m)
	other := NewStore(db1.db) // a second store over the same database, as a second process would be
	var wg sync.WaitGroup
	for i := 0; i < 24; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			st := db1
			if i%2 == 1 {
				st = other
			}
			if i%3 == 0 {
				st.ControlChild(m, a, KindPause)
			} else {
				st.SendToChild(m, a, "x")
			}
		}(i)
	}
	wg.Wait()
	var n int
	db1.db.QueryRow(`SELECT COUNT(*) FROM coder_session_commands WHERE conversation_id = ? AND issuer != ''`, a).Scan(&n)
	if n != MaxPendingMaster {
		t.Fatalf("%d master commands wait for one child, want %d", n, MaxPendingMaster)
	}
	if _, err := db1.Enqueue(Command{ProfileID: "p", ConversationID: a, Kind: KindSend, TurnID: "person-1", Message: "hi"}); err != nil {
		t.Fatalf("a person's send has no room: %v", err)
	}
	if _, err := db1.Enqueue(Command{ProfileID: "p", ConversationID: a, Kind: KindStop}); err != nil {
		t.Fatalf("the operator's stop has no room: %v", err)
	}
}

func TestAMasterCommandForADescendantRunsAndOneForAReleasedSessionDoesNot(t *testing.T) {
	h, l, st := newHost(t)
	m, _ := master(t, st, "coding")
	a := spawn(t, st, m)
	g := "grandchild"
	st.EnsureSession("p", g)
	st.db.Exec(`UPDATE coder_sessions SET role = 'child', parent_conversation_id = ? WHERE conversation_id = ?`, a, g)
	ok, err := st.Enqueue(Command{ProfileID: "p", ConversationID: g, Kind: KindPause, Issuer: m.ConversationID})
	if err != nil {
		t.Fatal(err)
	}
	_ = h.Tick()
	if got, _, _ := st.GetCommand(ok.ID); got.Status != CmdDone {
		t.Fatalf("a descendant's command: %+v", got)
	}
	st.db.Exec(`UPDATE coder_sessions SET parent_conversation_id = '', role = 'solo' WHERE conversation_id = ?`, g)
	bad, _ := st.Enqueue(Command{ProfileID: "p", ConversationID: g, Kind: KindResume, Issuer: m.ConversationID})
	_ = h.Tick()
	if got, _, _ := st.GetCommand(bad.ID); got.Status != CmdFailed || len(l.started) != 0 {
		t.Fatalf("a released session's command: %+v", got)
	}
}
