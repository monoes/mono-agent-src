package coderhost

import (
	"errors"
	"fmt"
	"os"
	"os/exec"
	"strings"
	"testing"
	"time"
)

func TestHostMadeRecognisesEveryPrefix(t *testing.T) {
	for id, want := range map[string]bool{
		"checkin-ab12": true, "master-ab12": true, "answer-ab12": true,
		"t-1234": false, "": false, "Master-1": false, "xmaster-1": false, "checkin": false, "master": false, "answer": false, "Answer-1": false,
	} {
		if got := HostMade(id); got != want {
			t.Errorf("HostMade(%q) = %v, want %v", id, got, want)
		}
	}
}

func TestATokenRowWithoutAGenuineSignatureIdentifiesNoOne(t *testing.T) {
	st := newStoreT(t)
	m, _ := master(t, st, "coding")
	running(t, st, m.ConversationID, "t1")
	tok := mint(t, st, m.ConversationID, "t1")
	if _, err := st.VerifySessionToken(tok); err != nil {
		t.Fatal(err)
	}
	for name, q := range map[string]string{
		"a wrong signature": `UPDATE coder_session_tokens SET mac = 'abcd'`,
		"no signature":      `UPDATE coder_session_tokens SET mac = ''`,
		"an edited pid":     `UPDATE coder_session_tokens SET pid = pid + 1`,
		"an edited turn":    `UPDATE coder_session_tokens SET turn_id = 't1 '`,
		"a later expiry":    `UPDATE coder_session_tokens SET expires_at = '2999-01-01T00:00:00Z'`,
	} {
		t.Run(name, func(t *testing.T) {
			st.RevokeSessionTokens("p", m.ConversationID)
			tok := mint(t, st, m.ConversationID, "t1")
			if _, err := st.db.Exec(q); err != nil {
				t.Fatal(err)
			}
			if name == "an edited turn" {
				running(t, st, m.ConversationID, "t1 ")
			}
			if _, err := st.VerifySessionToken(tok); err == nil {
				t.Fatal("a tampered row identified a caller")
			}
		})
	}
	// a row an attacker inserted with a token of its own choosing, with no key to sign it
	st.RevokeSessionTokens("p", m.ConversationID)
	running(t, st, m.ConversationID, "t1")
	forged := "mst-my-own-token"
	st.db.Exec(`INSERT INTO coder_session_tokens (token_hash, profile_id, conversation_id, turn_id, created_at, expires_at, pid, stamp, mac) VALUES (?, 'p', ?, 't1', 'x', '2999-01-01T00:00:00Z', ?, '', '')`,
		hashToken(forged), m.ConversationID, os.Getpid())
	if _, err := st.VerifySessionToken(forged); err == nil {
		t.Fatal("an unsigned inserted row identified a caller")
	}
}

func TestATokenWorksOnlyForTheTurnsProcessAndItsDescendants(t *testing.T) {
	if !ancestrySupported {
		t.Skip("process ancestry is not read on this platform (documented fallback)")
	}
	st := newStoreT(t)
	m, _ := master(t, st, "coding")
	running(t, st, m.ConversationID, "t1")
	tok, _ := st.IssueSessionToken("p", m.ConversationID, "t1")
	if _, err := st.VerifySessionToken(tok); err == nil {
		t.Fatal("a token not yet bound to a process identified a caller")
	}
	bind := func(pid int, stamp string) {
		t.Helper()
		if err := st.BindSessionToken("p", m.ConversationID, "t1", pid, stamp); err != nil {
			t.Fatal(err)
		}
	}
	// a sibling: a process that is not an ancestor of the caller, even with the right stamp
	sib := exec.Command("sleep", "30")
	if err := sib.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { sib.Process.Kill(); sib.Wait() })
	bind(sib.Process.Pid, ProcStamp(sib.Process.Pid))
	if _, err := st.VerifySessionToken(tok); err == nil {
		t.Fatal("a caller that is not a descendant of the turn's process was identified")
	}
	// the right pid with the wrong start stamp is a reused pid
	bind(os.Getpid(), "start:1")
	if _, err := st.VerifySessionToken(tok); err == nil {
		t.Fatal("a pid with another start time was accepted")
	}
	bind(os.Getpid(), ProcStamp(os.Getpid()))
	if _, err := st.VerifySessionToken(tok); err != nil {
		t.Fatalf("the turn's own process: %v", err)
	}
	// a descendant (the test's parent chain stands in for the turn's tools and workers)
	bind(os.Getppid(), ProcStamp(os.Getppid()))
	if _, err := st.VerifySessionToken(tok); err != nil {
		t.Fatalf("a descendant of the turn's process: %v", err)
	}
	// an unreadable chain fails closed
	orig := callerChain
	t.Cleanup(func() { callerChain = orig })
	callerChain = func() ([]int, bool) { return nil, false }
	if _, err := st.VerifySessionToken(tok); err == nil {
		t.Fatal("an unreadable ancestry was accepted")
	}
	callerChain = func() ([]int, bool) { return []int{os.Getpid(), 4242, os.Getppid()}, true }
	if _, err := st.VerifySessionToken(tok); err != nil {
		t.Fatalf("a worker below the turn: %v", err)
	}
}

func TestAMasterCommandRunsOnlyWhileTheTargetIsStillItsChild(t *testing.T) {
	h, l, st := newHost(t)
	m1, _ := master(t, st, "coding")
	m2, _ := master(t, st, "coding")
	a := spawn(t, st, m1)
	send, _ := st.SendToChild(m1, a, "go")
	pause, _ := st.ControlChild(m1, a, KindPause)
	if send.Issuer != m1.ConversationID || pause.Issuer != m1.ConversationID {
		t.Fatalf("issuers = %q %q", send.Issuer, pause.Issuer)
	}
	if err := st.SetRole("p", a, "solo", ""); err != nil { // re-parented between queue and run
		t.Fatal(err)
	}
	if err := st.SetRole("p", a, "child", m2.ConversationID); err != nil {
		t.Fatal(err)
	}
	if err := h.Tick(); err != nil {
		t.Fatal(err)
	}
	for _, c := range []Command{send, pause} {
		if got, _, _ := st.GetCommand(c.ID); got.Status != CmdFailed || !strings.Contains(got.Error, "no longer a child") {
			t.Errorf("command %d = %+v", c.ID, got)
		}
	}
	if len(l.started) != 0 || state(t, st, a).State == StatePaused {
		t.Fatalf("a master that lost the child still drove it: %v %v", l.started, state(t, st, a).State)
	}
	// the new master's command runs
	st.SendToChild(m2, a, "mine")
	_ = h.Tick()
	if len(l.started) != 1 {
		t.Fatalf("started = %v", l.started)
	}
}

func TestAPausedMastersSendsAreHeldAndAStoppedMasterCancelsThem(t *testing.T) {
	h, l, st := newHost(t)
	m, _ := master(t, st, "coding")
	a := spawn(t, st, m)
	x := state(t, st, m.ConversationID)
	x.State = StatePaused
	st.SaveSession(x)
	c, _ := st.SendToChild(m, a, "while paused")
	_ = h.Tick()
	if got, _, _ := st.GetCommand(c.ID); got.Status != CmdPending || len(l.started) != 0 {
		t.Fatalf("a paused master's send was not held: %+v started = %v", got, l.started)
	}
	x.State = StateIdle // resumed
	st.SaveSession(x)
	_ = h.Tick()
	if got, _, _ := st.GetCommand(c.ID); got.Status != CmdDone || len(l.started) != 1 {
		t.Fatalf("after the resume: %+v started = %v", got, l.started)
	}
	waitFor(t, "the turn to be live", func() bool { return l.turn(c.TurnID) != nil })
	l.turn(c.TurnID).end()
	waitFor(t, "the turn to settle", func() bool { return state(t, st, a).State == StateIdle })
	// stopping the master drops what it had queued for its children (the child is paused, so they wait)
	ca := state(t, st, a)
	ca.State = StatePaused
	st.SaveSession(ca)
	s1, _ := st.SendToChild(m, a, "one")
	s2, _ := st.SendToChild(m, a, "two")
	person := send(t, st, a, "person-1", "mine")
	if _, err := st.Enqueue(Command{ProfileID: "p", ConversationID: m.ConversationID, Kind: KindStop}); err != nil {
		t.Fatal(err)
	}
	_ = h.Tick()
	for _, id := range []int64{s1.ID, s2.ID} {
		if got, _, _ := st.GetCommand(id); got.Status != CmdFailed || got.Error != "the master was stopped" {
			t.Errorf("master send %d = %+v", id, got)
		}
	}
	if got, _, _ := st.GetCommand(person.ID); got.Status != CmdPending {
		t.Errorf("a person's send was cancelled: %+v", got)
	}
}

func TestMasterSendsAreCappedAndExpireSoonerButLeaveRoomForAPerson(t *testing.T) {
	st := newStoreT(t)
	now := time.Date(2026, 10, 26, 10, 0, 0, 0, time.UTC)
	st.now = func() time.Time { return now }
	m, _ := master(t, st, "coding")
	a := spawn(t, st, m)
	for i := 0; i < MaxPendingMaster; i++ {
		if _, err := st.SendToChild(m, a, fmt.Sprint("m", i)); err != nil {
			t.Fatal(i, err)
		}
	}
	if _, err := st.SendToChild(m, a, "one too many"); !errors.Is(err, ErrQueueFull) {
		t.Fatalf("sixth master send: %v", err)
	}
	if _, err := st.Enqueue(Command{ProfileID: "p", ConversationID: a, Kind: KindSend, TurnID: "person-1", Message: "hi"}); err != nil {
		t.Fatalf("a person's send is refused behind a full master queue: %v", err)
	}
	now = now.Add(MaxCheckinAge + time.Minute)
	if err := st.ExpireSends(); err != nil {
		t.Fatal(err)
	}
	pend, _ := st.Pending()
	if len(pend) != 1 || pend[0].TurnID != "person-1" {
		t.Fatalf("after the short expiry: %+v", pend)
	}
}

func TestSpawnReturnsTheChildEvenWhenItsFirstMessageIsNotQueued(t *testing.T) {
	st := newStoreT(t)
	m, _ := master(t, st, "coding")
	if _, err := st.db.Exec(`ALTER TABLE coder_session_commands RENAME TO coder_session_commands_x`); err != nil {
		t.Fatal(err)
	}
	out, err := st.Spawn(m, SpawnRequest{Message: "hello"})
	if err == nil || out.ConversationID == "" || !strings.Contains(err.Error(), out.ConversationID) {
		t.Fatalf("out = %+v err = %v", out, err)
	}
	if x, ok, _ := st.GetSession("p", out.ConversationID); !ok || x.Parent != m.ConversationID {
		t.Fatal("the child exists and is linked")
	}
}

func TestAMeshAliasSetByAMasterIsUniqueAmongSessions(t *testing.T) {
	st := newStoreT(t)
	m, _ := master(t, st, "coding")
	a, b := spawn(t, st, m), spawn(t, st, m)
	if _, _, err := st.SetChild(m, a, []byte(`{"mesh_alias":"worker-1"}`)); err != nil {
		t.Fatal(err)
	}
	if _, _, err := st.SetChild(m, b, []byte(`{"mesh_alias":"Worker-1"}`)); !errors.Is(err, ErrInvalid) {
		t.Fatalf("a taken alias: %v", err)
	}
	if _, _, err := st.SetChild(m, a, []byte(`{"mesh_alias":"worker-1","checkin_minutes":9}`)); err != nil {
		t.Fatalf("a session keeps its own alias: %v", err)
	}
}

func TestASessionOfAnotherProfileIsNeverAStranger(t *testing.T) {
	st := newStoreT(t)
	m, _ := master(t, st, "coding")
	if _, err := st.EnsureSession("q", "stranger"); err != nil {
		t.Fatal(err)
	}
	st.db.Exec(`UPDATE coder_sessions SET parent_conversation_id = ?, role = 'child' WHERE profile_id = 'q'`, m.ConversationID)
	if st.IsDescendant("p", m.ConversationID, "stranger") {
		t.Fatal("a session of another profile counts as a descendant")
	}
	if _, err := st.SendToChild(m, "stranger", "x"); !errors.Is(err, ErrNotYours) {
		t.Fatalf("send: %v", err)
	}
	if _, err := st.ControlChild(m, "stranger", KindStop); !errors.Is(err, ErrNotYours) {
		t.Fatalf("stop: %v", err)
	}
	if st.PendingFor("q", "stranger") != 0 {
		t.Fatal("a command was queued in another profile")
	}
}

// usage records a usage.updated snapshot of a turn's lead (agent "") or of a worker.
func usage(t *testing.T, st *Store, conv, turn string, seq int, cost float64, agent string) {
	t.Helper()
	payload := fmt.Sprintf(`{"agentId":%q,"costUsd":%v,"source":"result"}`, agent, cost)
	if _, err := st.db.Exec(`INSERT INTO ai_chat_events (profile_id, conversation_id, turn_id, seq, created_at, version, type, payload) VALUES ('p', ?, ?, ?, 'now', 1, 'usage.updated', ?)`,
		conv, turn, seq, payload); err != nil {
		t.Fatal(err)
	}
}

func TestACappedSessionRunsEachTurnUnderWhatItHasLeftAndRefusesWhenNothingIs(t *testing.T) {
	h, l, st := newHost(t)
	m, _ := master(t, st, "coding")
	if err := st.SetBudget("p", m.ConversationID, 10); err != nil {
		t.Fatal(err)
	}
	out, err := st.Spawn(m, SpawnRequest{BudgetUSD: 3})
	if err != nil {
		t.Fatal(err)
	}
	a := out.ConversationID
	base := time.Now().Add(-time.Hour)
	addTurn(t, st, a, "t-old", "completed", "x", base)
	usage(t, st, a, "t-old", 1, 1.0, "")
	usage(t, st, a, "t-old", 2, 2.5, "")   // the newest snapshot of the turn is its total, not a sum with the first
	usage(t, st, a, "t-old", 3, 99, "w-1") // a worker's snapshot is not the lead's
	if spent, _ := st.Spent("p", a); spent != 2.5 {
		t.Fatalf("spent = %v", spent)
	}
	send(t, st, a, "t-1", "go on")
	if err := h.Tick(); err != nil {
		t.Fatal(err)
	}
	if len(l.caps) != 1 || l.caps[0] < 0.49 || l.caps[0] > 0.51 {
		t.Fatalf("caps = %v, want the 0.5 left of 3", l.caps)
	}
	// the master has 10, gave 3 away and spent nothing: it runs under 7
	cp, capped, err := st.TurnCap("p", m.ConversationID)
	if err != nil || !capped || cp != 7 {
		t.Fatalf("master cap = %v %v %v", cp, capped, err)
	}
	// nothing left: the send fails with the reason and no turn starts
	l.turn("t-1").end()
	waitFor(t, "the first turn to settle", func() bool { return state(t, st, a).State == StateIdle })
	addTurn(t, st, a, "t-2", "completed", "x", base.Add(time.Minute))
	usage(t, st, a, "t-2", 1, 0.6, "")
	c := send(t, st, a, "t-3", "more")
	_ = h.Tick()
	if got, _, _ := st.GetCommand(c.ID); got.Status != CmdFailed || !strings.Contains(got.Error, "budget") || len(l.started) != 1 {
		t.Fatalf("command = %+v started = %v", got, l.started)
	}
	if x := state(t, st, a); x.TurnID != "" || x.State != StateIdle {
		t.Fatalf("a refused send left the session as %+v", x)
	}
	// a session with no budget runs uncapped
	b := spawnUncapped(t, st)
	send2(t, st, b)
	_ = h.Tick()
	if len(l.caps) != 1 {
		t.Fatalf("an uncapped session was capped: %v", l.caps)
	}
}

func spawnUncapped(t *testing.T, st *Store) string {
	t.Helper()
	m, _ := master(t, st, "coding")
	return spawn(t, st, m)
}

func send2(t *testing.T, st *Store, conv string) { t.Helper(); send(t, st, conv, "u-1", "hi") }

// plainLauncher hides StartCapped: a launcher that cannot start a turn under a cap.
type plainLauncher struct{ Launcher }

func TestACappedSessionIsRefusedByALauncherThatCannotEnforceTheCap(t *testing.T) {
	h, l, st := newHost(t)
	h.L = plainLauncher{l}
	m, _ := master(t, st, "coding")
	st.SetBudget("p", m.ConversationID, 5)
	c := send(t, st, m.ConversationID, "t-1", "go")
	_ = h.Tick()
	if got, _, _ := st.GetCommand(c.ID); got.Status != CmdFailed || !strings.Contains(got.Error, "spend cap") || len(l.started) != 0 {
		t.Fatalf("command = %+v started = %v", got, l.started)
	}
}
