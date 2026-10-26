package coderhost

import (
	"errors"
	"fmt"
	"os"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/monoes/mono-agent/internal/ai"
)

func capRows(t *testing.T, st *Store, conv string) int {
	t.Helper()
	var n int
	st.db.QueryRow(`SELECT COUNT(*) FROM coder_session_turn_caps WHERE conversation_id = ?`, conv).Scan(&n)
	return n
}

func TestATurnsCapIsOnRecordBeforeItStartsAndDroppedIfItDoesNotStart(t *testing.T) {
	h, l, st := newHost(t)
	m, _ := master(t, st, "coding")
	c := m.ConversationID
	st.SetBudget("p", c, 4)
	seen := -1
	l.onStart = func() { seen = capRows(t, st, c) }
	send(t, st, c, "t-1", "go")
	_ = h.Tick()
	if seen != 1 {
		t.Fatalf("cap rows when the turn started = %d, want 1", seen)
	}
	l.turn("t-1").end()
	waitFor(t, "settle", func() bool { return state(t, st, c).State == StateIdle })
	st.db.Exec(`UPDATE coder_session_turn_caps SET cap_usd = 0`) // forget what turn 1 held
	l.startErr = errors.New("no runtime")
	cmd := send(t, st, c, "t-2", "again")
	_ = h.Tick()
	if got, _, _ := st.GetCommand(cmd.ID); got.Status != CmdFailed {
		t.Fatalf("command = %+v", got)
	}
	if capRows(t, st, c) != 1 {
		t.Fatalf("a turn that did not start left its cap on record: %d rows", capRows(t, st, c))
	}
	// a cap that cannot be recorded stops the start
	l.startErr = nil
	st.db.Exec(`ALTER TABLE coder_session_turn_caps RENAME TO caps_x`)
	st.db.Exec(`UPDATE coder_session_commands SET status = 'done'`)
	n := len(l.started)
	cmd = send(t, st, c, "t-3", "third")
	_ = h.Tick()
	if got, _, _ := st.GetCommand(cmd.ID); got.Status != CmdFailed || len(l.started) != n {
		t.Fatalf("a turn started with no cap on record: %+v", got)
	}
}

func startedCap(t *testing.T, st *Store, conv, turn string, capUSD float64) {
	t.Helper()
	if err := st.RecordTurnCap("p", conv, turn, capUSD); err != nil {
		t.Fatal(err)
	}
	st.MarkTurnCapStarted("p", conv, turn)
}

func TestAReportedZeroStaysZeroAndOnlyASilentTurnCountsAsItsCap(t *testing.T) {
	st := newStoreT(t)
	m, _ := master(t, st, "coding")
	c := m.ConversationID
	st.SetBudget("p", c, 5)
	base := time.Now().Add(-time.Hour)
	addTurn(t, st, c, "t1", "completed", "x", base)
	startedCap(t, st, c, "t1", 5)
	event(t, st, c, "t1", 1, "usage.updated", `{"agentId":"","costUsd":0}`)
	if spent, _ := st.Spent("p", c); spent != 0 {
		t.Fatalf("a reported 0 is 0: spent = %v", spent)
	}
	addTurn(t, st, c, "t2", "completed", "x", base.Add(time.Minute))
	startedCap(t, st, c, "t2", 4)
	if spent, _ := st.Spent("p", c); spent != 4 {
		t.Fatalf("a started turn with no usage event counts as its whole cap: spent = %v", spent)
	}
	// its workers cost more than the cap: the larger stands, not the two added
	event(t, st, c, "t2", 1, "agent.finished", `{"agentId":"w1","costUsd":6}`)
	if spent, _ := st.Spent("p", c); spent != 6 {
		t.Fatalf("max(cap, workers) = 6, spent = %v", spent)
	}
	event(t, st, c, "t2", 2, "agent.finished", `{"agentId":"w2","costUsd":-1}`)
	addTurn(t, st, c, "t3", "completed", "x", base.Add(2*time.Minute))
	startedCap(t, st, c, "t3", 3)
	event(t, st, c, "t3", 1, "agent.finished", `{"agentId":"w1","costUsd":1}`)
	if spent, _ := st.Spent("p", c); spent != 6+3 {
		t.Fatalf("workers cheaper than the cap add nothing: spent = %v", spent)
	}
	// a reported turn adds its workers on top
	event(t, st, c, "t3", 2, "usage.updated", `{"agentId":"","costUsd":0.5}`)
	if spent, _ := st.Spent("p", c); spent != 6+0.5+1 {
		t.Fatalf("a reported turn plus its workers: spent = %v", spent)
	}
}

func TestACapOfATurnThatNeverStartedChargesNothingAndIsDropped(t *testing.T) {
	st := newStoreT(t)
	now := time.Date(2026, 10, 26, 10, 0, 0, 0, time.UTC)
	st.now = func() time.Time { return now }
	m, _ := master(t, st, "coding")
	c := m.ConversationID
	st.SetBudget("p", c, 5)
	st.RecordTurnCap("p", c, "t-crashed", 5) // the host died between the record and the start
	if spent, _ := st.Spent("p", c); spent != 0 {
		t.Fatalf("an unstarted cap was charged: %v", spent)
	}
	if capUSD, _, err := st.TurnCap("p", c); err != nil || capUSD != 5 {
		t.Fatalf("the next turn's cap = %v %v", capUSD, err)
	}
	st.DropUnstartedCaps()
	if capRows(t, st, c) != 1 {
		t.Fatal("a fresh unstarted row (its turn may be starting) was dropped")
	}
	now = now.Add(2 * time.Minute)
	st.DropUnstartedCaps()
	if capRows(t, st, c) != 0 {
		t.Fatal("an old unstarted row survived")
	}
	// binding the token marks the turn started
	st.RecordTurnCap("p", c, "t-ok", 5)
	running(t, st, c, "t-ok")
	st.IssueSessionToken("p", c, "t-ok")
	st.BindSessionToken("p", c, "t-ok", os.Getpid(), ProcStamp(os.Getpid()))
	x := state(t, st, c)
	x.State, x.TurnID = StateIdle, ""
	st.SaveSession(x)
	if spent, _ := st.Spent("p", c); spent != 5 {
		t.Fatalf("a started, ended, cost-less turn counts as its cap: %v", spent)
	}
}

// The real path: the host starts the master's turn under TurnCap, the turn spawns children during itself,
// and the next turn's cap reflects what was carved.
func TestACappedMasterCanCarveDuringItsOwnTurnAndTheNextTurnSeesIt(t *testing.T) {
	h, l, st := newHost(t)
	m, _ := master(t, st, "coding")
	c := m.ConversationID
	st.SetBudget("p", c, 10)
	send(t, st, c, "t-1", "plan the work")
	_ = h.Tick()
	if len(l.caps) != 1 || l.caps[0] != 10 {
		t.Fatalf("caps = %v, want the whole 10 for the first turn", l.caps)
	}
	// the running turn spawns: carve up to budget - spent - carved
	for _, usd := range []float64{4, 3} {
		if _, err := st.Spawn(m, SpawnRequest{BudgetUSD: usd}); err != nil {
			t.Fatalf("carve %v during the master's own turn: %v", usd, err)
		}
	}
	if _, err := st.Spawn(m, SpawnRequest{BudgetUSD: 3.5}); !errors.Is(err, ErrLimit) {
		t.Fatalf("3.5 of the 3 left: %v", err)
	}
	l.turn("t-1").end()
	waitFor(t, "settle", func() bool { return state(t, st, c).State == StateIdle })
	addTurn(t, st, c, "t-1", "completed", "x", time.Now())
	event(t, st, c, "t-1", 1, "usage.updated", `{"agentId":"","costUsd":2}`)
	send(t, st, c, "t-2", "next")
	_ = h.Tick()
	if len(l.caps) != 2 || l.caps[1] != 1 { // 10 - 7 carved - 2 spent
		t.Fatalf("caps = %v, want 1 left for the next turn", l.caps)
	}
}

func TestDeletingAConversationTakesItsBudgetTokenAndSpawnRowsAndSweepsOrphans(t *testing.T) {
	st := newStoreT(t)
	m, _ := master(t, st, "coding")
	a := spawn(t, st, m)
	running(t, st, a, "t1")
	mint(t, st, a, "t1")
	st.RecordTurnCap("p", a, "t1", 1)
	count := func(q string, args ...any) int {
		var n int
		st.db.QueryRow(q, args...).Scan(&n)
		return n
	}
	store, _ := ai.NewAIStore(st.db)
	x := state2(t, st, a)
	x.State, x.TurnID = StateIdle, ""
	st.SaveSession(x)
	if err := store.DeleteConversation(a, "p"); err != nil {
		t.Fatal(err)
	}
	if count(`SELECT COUNT(*) FROM coder_session_turn_caps WHERE conversation_id = ?`, a)+count(`SELECT COUNT(*) FROM coder_session_tokens WHERE conversation_id = ?`, a) != 0 {
		t.Fatal("a deleted conversation left cap or token rows")
	}
	if count(`SELECT COUNT(*) FROM coder_session_spawns WHERE parent = ?`, m.ConversationID) != 1 {
		t.Fatal("a deleted child must keep its master's spawn row, or spawn-and-delete outruns the rate limit")
	}
	if err := store.DeleteConversation(m.ConversationID, "p"); err != nil {
		t.Fatal(err)
	}
	if count(`SELECT COUNT(*) FROM coder_session_spawns WHERE parent = ?`, m.ConversationID) != 0 {
		t.Fatal("a deleted master left its spawn rows")
	}
	// rows of conversations that are gone, left by a racing writer, are swept
	st.db.Exec(`INSERT INTO coder_session_turn_caps VALUES ('p', 'ghost', 't', 1)`)
	st.db.Exec(`INSERT INTO coder_session_tokens (token_hash, profile_id, conversation_id, turn_id, created_at, expires_at) VALUES ('h', 'p', 'ghost', 't', 'x', 'y')`)
	st.db.Exec(`INSERT INTO coder_session_spawns (profile_id, parent, child, created_at) VALUES ('p', 'ghost', '', 'x')`)
	if _, err := st.SweepOrphans(); err != nil {
		t.Fatal(err)
	}
	if count(`SELECT (SELECT COUNT(*) FROM coder_session_turn_caps) + (SELECT COUNT(*) FROM coder_session_tokens) + (SELECT COUNT(*) FROM coder_session_spawns WHERE parent = 'ghost')`) != 0 {
		t.Fatal("orphan rows survived the sweep")
	}
}

func state2(t *testing.T, st *Store, conv string) Session {
	t.Helper()
	x, _, _ := st.GetSession("p", conv)
	return x
}

func TestHostInsideTurnIgnoresExpiredRowsAndAStaleStamp(t *testing.T) {
	st := newStoreT(t)
	now := time.Date(2026, 10, 26, 10, 0, 0, 0, time.UTC)
	st.now = func() time.Time { return now }
	m, _ := master(t, st, "coding")
	running(t, st, m.ConversationID, "t1")
	st.IssueSessionToken("p", m.ConversationID, "t1")
	pid := os.Getppid()
	st.BindSessionToken("p", m.ConversationID, "t1", pid, ProcStamp(pid))
	if !st.HostInsideTurn() {
		t.Fatal("a host below a live bound turn was not noticed")
	}
	if ancestrySupported {
		st.db.Exec(`UPDATE coder_session_tokens SET stamp = 'start:1'`)
		if st.HostInsideTurn() {
			t.Fatal("a pid that now belongs to another process counts")
		}
		st.db.Exec(`UPDATE coder_session_tokens SET stamp = ?`, ProcStamp(pid))
	}
	now = now.Add(SessionTokenTTL + time.Minute)
	if st.HostInsideTurn() {
		t.Fatal("an expired token row counts")
	}
}

func TestAHeldMasterMessageDoesNotHoldUpAPersonsMessage(t *testing.T) {
	h, l, st := newHost(t)
	m, _ := master(t, st, "coding")
	a := spawn(t, st, m)
	x := state(t, st, m.ConversationID)
	x.State = StatePaused
	st.SaveSession(x)
	held, _ := st.SendToChild(m, a, "from the paused master")
	person := send(t, st, a, "person-1", "from the user")
	_ = h.Tick()
	if got, _, _ := st.GetCommand(held.ID); got.Status != CmdPending {
		t.Fatalf("the master's message = %+v, want held", got)
	}
	if got, _, _ := st.GetCommand(person.ID); got.Status != CmdDone || len(l.started) != 1 || !strings.HasSuffix(l.started[0], "from the user") {
		t.Fatalf("the person's message = %+v started = %v", got, l.started)
	}
}

func TestAMasterAlwaysHasRoomToStopAChild(t *testing.T) {
	st := newStoreT(t)
	m, _ := master(t, st, "coding")
	a := spawn(t, st, m)
	for i := 0; i < MaxPendingMaster; i++ {
		if _, err := st.SendToChild(m, a, "x"); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := st.ControlChild(m, a, KindPause); !errors.Is(err, ErrQueueFull) {
		t.Fatalf("a pause behind a full master queue: %v", err)
	}
	for i := 0; i < 3; i++ {
		if _, err := st.ControlChild(m, a, KindStop); err != nil {
			t.Fatalf("stop %d: %v", i, err)
		}
	}
	if _, err := st.SendToChild(m, a, "one too many"); !errors.Is(err, ErrQueueFull) {
		t.Fatal("stops must not free or take master send slots")
	}
}

// answer is an issuer-tagged send whose turn id is not master-prefixed (another path's, e.g. an answer
// note a master gives a child): the shared issuer rules apply to it as to a master- send.
func answer(t *testing.T, st *Store, master, child, turn string) Command {
	t.Helper()
	c, err := st.Enqueue(Command{ProfileID: "p", ConversationID: child, Kind: KindSend, TurnID: turn, Message: "note", Issuer: master})
	if err != nil {
		t.Fatal(err)
	}
	return c
}

func TestAnyIssuerTaggedSendFollowsTheMasterRules(t *testing.T) {
	h, l, st := newHost(t)
	m, _ := master(t, st, "coding")
	a := spawn(t, st, m)
	cmd := answer(t, st, m.ConversationID, a, "answer-m-1")
	// the helper's verdicts
	x := state(t, st, a)
	if ok, hold, reason := h.issuerStillAllowed(cmd, x); !ok || hold || reason != "" {
		t.Fatalf("live master: %v %v %q", ok, hold, reason)
	}
	if ok, _, _ := h.issuerStillAllowed(Command{ProfileID: "p", ConversationID: a}, x); !ok {
		t.Fatal("a command with no issuer is always allowed")
	}
	mx := state(t, st, m.ConversationID)
	mx.State = StatePaused
	st.SaveSession(mx)
	if ok, hold, _ := h.issuerStillAllowed(cmd, x); ok || !hold {
		t.Fatalf("paused master: ok=%v hold=%v", ok, hold)
	}
	if ok, hold, _ := h.issuerStillAllowed(Command{ProfileID: "p", ConversationID: a, Kind: KindStop, Issuer: m.ConversationID}, x); !ok || hold {
		t.Fatalf("a paused master can still stop its child: ok=%v hold=%v", ok, hold)
	}
	_ = h.Tick()
	if got, _, _ := st.GetCommand(cmd.ID); got.Status != CmdPending || len(l.started) != 0 {
		t.Fatalf("held send = %+v", got)
	}
	// a stop of the master cancels it
	mx.State = StateIdle
	st.SaveSession(mx)
	ca := state(t, st, a)
	ca.State = StatePaused
	st.SaveSession(ca)
	if _, err := st.Enqueue(Command{ProfileID: "p", ConversationID: m.ConversationID, Kind: KindStop}); err != nil {
		t.Fatal(err)
	}
	_ = h.Tick()
	if got, _, _ := st.GetCommand(cmd.ID); got.Status != CmdFailed || got.Error != "the master was stopped" {
		t.Fatalf("after the master's stop: %+v", got)
	}
	// a gone master
	mx2, _ := master(t, st, "coding")
	b := spawn(t, st, mx2)
	gone := answer(t, st, mx2.ConversationID, b, "answer-m-2")
	st.db.Exec(`UPDATE coder_sessions SET state = 'stopped' WHERE conversation_id = ?`, mx2.ConversationID)
	_ = h.Tick()
	if got, _, _ := st.GetCommand(gone.ID); got.Status != CmdFailed || !strings.Contains(got.Error, "gone or stopped") {
		t.Fatalf("stopped master: %+v", got)
	}
}

func TestAnyIssuerTaggedSendExpiresLikeACheckin(t *testing.T) {
	st := newStoreT(t)
	now := time.Date(2026, 10, 26, 10, 0, 0, 0, time.UTC)
	st.now = func() time.Time { return now }
	m, _ := master(t, st, "coding")
	a := spawn(t, st, m)
	answer(t, st, m.ConversationID, a, "answer-m-1")
	st.Enqueue(Command{ProfileID: "p", ConversationID: a, Kind: KindSend, TurnID: "person-1", Message: "hi"})
	now = now.Add(MaxCheckinAge + time.Minute)
	st.ExpireSends()
	pend, _ := st.Pending()
	if len(pend) != 1 || pend[0].TurnID != "person-1" {
		t.Fatalf("pending = %+v", pend)
	}
}

func TestPendingMasterStopsAreCappedAtTwoPerChatAndAtomically(t *testing.T) {
	st := newStoreT(t)
	m, _ := master(t, st, "coding")
	a := spawn(t, st, m)
	first, err := st.ControlChild(m, a, KindStop)
	if err != nil {
		t.Fatal(err)
	}
	for i := 0; i < MaxPendingPerChat+1; i++ { // one issuer: a repeat is a no-op
		got, err := st.ControlChild(m, a, KindStop)
		if err != nil || got.ID != first.ID {
			t.Fatalf("stop %d: %+v %v, want the stop already waiting", i, got, err)
		}
	}
	// exactly two issuers fit: the second is accepted, the third is not
	if _, err := st.Enqueue(Command{ProfileID: "p", ConversationID: a, Kind: KindStop, Issuer: "other-1"}); err != nil {
		t.Fatalf("a second issuer's stop: %v", err)
	}
	if _, err := st.Enqueue(Command{ProfileID: "p", ConversationID: a, Kind: KindStop, Issuer: "other-2"}); !errors.Is(err, ErrQueueFull) {
		t.Fatalf("a third issuer's stop: %v", err)
	}
	var n int
	st.db.QueryRow(`SELECT COUNT(*) FROM coder_session_commands WHERE conversation_id = ? AND kind = 'stop'`, a).Scan(&n)
	if n != MaxPendingMasterStops || MaxPendingMasterStops != 2 {
		t.Fatalf("%d stops wait (limit %d), want 2", n, MaxPendingMasterStops)
	}
	if _, err := st.Enqueue(Command{ProfileID: "p", ConversationID: a, Kind: KindStop}); err != nil {
		t.Fatalf("a person's stop has no room: %v", err)
	}
}

func TestConcurrentMasterStopsFromTwoStoresStayWithinTheCap(t *testing.T) {
	st := newStoreT(t)
	m, _ := master(t, st, "coding")
	a := spawn(t, st, m)
	other := NewStore(st.db)
	var wg sync.WaitGroup
	for i := 0; i < 30; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			s := st
			if i%2 == 1 {
				s = other
			}
			s.Enqueue(Command{ProfileID: "p", ConversationID: a, Kind: KindStop, Issuer: fmt.Sprint("issuer-", i%6)})
		}(i)
	}
	wg.Wait()
	var n, distinct int
	st.db.QueryRow(`SELECT COUNT(*), COUNT(DISTINCT issuer) FROM coder_session_commands WHERE conversation_id = ? AND kind = 'stop'`, a).Scan(&n, &distinct)
	if n != 2 || distinct != 2 {
		t.Fatalf("%d pending stops from %d issuers, want 2 from 2", n, distinct)
	}
	// the same issuer from both stores: one
	st2 := newStoreT(t)
	m2, _ := master(t, st2, "coding")
	b := spawn(t, st2, m2)
	o2 := NewStore(st2.db)
	for i := 0; i < 20; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			s := st2
			if i%2 == 1 {
				s = o2
			}
			s.ControlChild(m2, b, KindStop)
		}(i)
	}
	wg.Wait()
	st2.db.QueryRow(`SELECT COUNT(*) FROM coder_session_commands WHERE conversation_id = ? AND kind = 'stop'`, b).Scan(&n)
	if n != 1 {
		t.Fatalf("%d stops from one issuer wait", n)
	}
}

func TestDropUnstartedCapsKeepsTheSessionsCurrentTurn(t *testing.T) {
	st := newStoreT(t)
	m, _ := master(t, st, "coding")
	c := m.ConversationID
	running(t, st, c, "t-current") // a slow admission: the turn is the session's, its cap row is old
	st.RecordTurnCap("p", c, "t-current", 5)
	st.RecordTurnCap("p", c, "t-old", 5)
	st.db.Exec(`UPDATE coder_session_turn_caps SET created_at = '2020-01-01T00:00:00Z'`)
	st.DropUnstartedCaps()
	var cur, old int
	st.db.QueryRow(`SELECT COUNT(*) FROM coder_session_turn_caps WHERE turn_id = 't-current'`).Scan(&cur)
	st.db.QueryRow(`SELECT COUNT(*) FROM coder_session_turn_caps WHERE turn_id = 't-old'`).Scan(&old)
	if cur != 1 || old != 0 {
		t.Fatalf("current turn rows %d (want 1), unrelated old rows %d (want 0)", cur, old)
	}
}

func TestTheIssuerNotTheTurnIdMakesASendAMastersSend(t *testing.T) {
	for _, ids := range [][2]string{{"answer-m-2", "answer-m-3"}, {"x-1", "x-2"}} {
		t.Run(ids[0], func(t *testing.T) { issuerAuthority(t, ids[0], ids[1]) })
	}
}

func issuerAuthority(t *testing.T, id1, id2 string) {
	h, l, st := newHost(t)
	m, _ := master(t, st, "coding")
	a := spawn(t, st, m)
	// an issuer-tagged send can never be a mission, whatever its turn id
	if _, err := st.Enqueue(Command{ProfileID: "p", ConversationID: a, Kind: KindSend, TurnID: id1 + "-mission", Message: "x", Issuer: m.ConversationID, WantsMission: true}); err == nil {
		t.Fatal("an issuer-tagged send asked for a mission")
	}
	tagged := answer(t, st, m.ConversationID, a, id1)
	if _, err := st.Confirm(tagged, 0); err == nil {
		t.Fatal("an issuer-tagged send was confirmed")
	}
	if _, ok := st.PeekConfirm(tagged); ok {
		t.Fatal("an issuer-tagged send has a confirmation")
	}
	// a master- turn id with no issuer is the master tools' alone
	if _, err := st.Enqueue(Command{ProfileID: "p", ConversationID: a, Kind: KindSend, TurnID: "master-x", Message: "x"}); err == nil {
		t.Fatal("a caller with no issuer used a master- turn id (the operator CLI's --turn master-x)")
	}
	// it is gated like a master- send: the streak limit holds it back, and its turn counts toward the streak
	s, _ := st.GetSettings("p", a)
	s.CheckinLimit = 1
	st.SaveSettings("p", a, s)
	_ = h.Tick()
	if got, _, _ := st.GetCommand(tagged.ID); got.Status != CmdDone || len(l.started) != 1 {
		t.Fatalf("the first tagged send: %+v", got)
	}
	l.turn(id1).end()
	waitFor(t, "settle", func() bool { return state(t, st, a).State == StateIdle })
	addTurn(t, st, a, id1, "completed", "x", time.Now())
	if n, _, _, _ := st.CheckinStreak("p", a); n != 1 {
		t.Fatalf("streak = %d, the tagged turn counts like a master- turn", n)
	}
	second := answer(t, st, m.ConversationID, a, id2)
	_ = h.Tick()
	if got, _, _ := st.GetCommand(second.ID); got.Status != CmdFailed || !strings.Contains(got.Error, "in a row") || len(l.started) != 1 {
		t.Fatalf("the second tagged send past the limit: %+v", got)
	}
}

func TestTheHostDropsCapsOfTurnsThatNeverStarted(t *testing.T) {
	h, _, st := newHost(t)
	m, _ := master(t, st, "coding")
	st.RecordTurnCap("p", m.ConversationID, "t-crashed", 5)
	st.db.Exec(`UPDATE coder_session_turn_caps SET created_at = '2020-01-01T00:00:00Z'`)
	_ = h.Tick()
	if capRows(t, st, m.ConversationID) != 0 {
		t.Fatal("the host kept the cap of a turn that never started")
	}
}
