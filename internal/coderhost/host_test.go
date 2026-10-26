package coderhost

import (
	"context"
	"errors"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/monoes/mono-agent/internal/testdb"
)

type fakeTurn struct {
	pid     int
	done    chan struct{}
	once    sync.Once
	stopped bool
}

func newFakeTurn(pid int) *fakeTurn { return &fakeTurn{pid: pid, done: make(chan struct{})} }
func (f *fakeTurn) PID() int        { return f.pid }
func (f *fakeTurn) Stamp() string   { return "stamp" }
func (f *fakeTurn) Wait() error     { <-f.done; return nil }
func (f *fakeTurn) end()            { f.once.Do(func() { close(f.done) }) }
func (f *fakeTurn) Stop()           { f.stopped = true; f.end() }

type fakeLauncher struct {
	mu       sync.Mutex
	started  []string
	tokens   []string
	turns    map[string]*fakeTurn
	finished []string
	alive    map[int]*fakeTurn
	startErr error
	next     int
	onStart  func() // runs inside Start, before the turn exists
}

func newFakeLauncher() *fakeLauncher {
	return &fakeLauncher{turns: map[string]*fakeTurn{}, alive: map[int]*fakeTurn{}, next: 100}
}

func (l *fakeLauncher) Start(profile, conv, turn, msg string, att []string, token string) (Turn, error) {
	if l.onStart != nil {
		l.onStart()
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.startErr != nil {
		return nil, l.startErr
	}
	l.next++
	t := newFakeTurn(l.next)
	l.started = append(l.started, turn+":"+msg)
	l.tokens = append(l.tokens, token)
	l.turns[turn] = t
	return t, nil
}

func (l *fakeLauncher) Adopt(pid int, stamp string) Turn {
	l.mu.Lock()
	defer l.mu.Unlock()
	if t := l.alive[pid]; t != nil {
		return t
	}
	return nil
}

func (l *fakeLauncher) Finish(profile, conv, turn, reason string) {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.finished = append(l.finished, turn)
}

func (l *fakeLauncher) turn(id string) *fakeTurn {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.turns[id]
}

func newHost(t *testing.T) (*Host, *fakeLauncher, *Store) {
	t.Helper()
	db := testdb.Open(t)
	t.Cleanup(func() { db.Close() })
	st := NewStore(db.DB)
	l := newFakeLauncher()
	return &Host{Store: st, L: l}, l, st
}

func send(t *testing.T, st *Store, conv, turn, msg string) Command {
	t.Helper()
	c, err := st.Enqueue(Command{ProfileID: "p", ConversationID: conv, Kind: KindSend, TurnID: turn, Message: msg})
	if err != nil {
		t.Fatal(err)
	}
	return c
}

func state(t *testing.T, st *Store, conv string) Session {
	t.Helper()
	x, ok, err := st.GetSession("p", conv)
	if err != nil || !ok {
		t.Fatalf("session %s: ok=%v err=%v", conv, ok, err)
	}
	return x
}

func waitFor(t *testing.T, what string, f func() bool) {
	t.Helper()
	for i := 0; i < 400; i++ {
		if f() {
			return
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatalf("timed out waiting for %s", what)
}

func TestEnqueueValidatesAndBoundsTheQueue(t *testing.T) {
	_, _, st := newHost(t)
	bad := []Command{
		{ProfileID: "p", ConversationID: "c", Kind: KindSend, TurnID: "t"},                                                  // no message
		{ProfileID: "p", ConversationID: "c", Kind: KindSend, Message: "x"},                                                 // no turn id
		{ProfileID: "p", ConversationID: "c", Kind: KindSend, TurnID: "t", Message: strings.Repeat("x", MaxMessageBytes+1)}, // too long
		{ProfileID: "p", ConversationID: "c", Kind: "kill"},
		{ConversationID: "c", Kind: KindStop},
	}
	for i, c := range bad {
		if _, err := st.Enqueue(c); err == nil {
			t.Errorf("case %d: accepted %+v", i, c.Kind)
		}
	}
	for i := 0; i < MaxPendingPerChat; i++ {
		if _, err := st.Enqueue(Command{ProfileID: "p", ConversationID: "c", Kind: KindPause}); err != nil {
			t.Fatalf("command %d: %v", i, err)
		}
	}
	if _, err := st.Enqueue(Command{ProfileID: "p", ConversationID: "c", Kind: KindPause}); !errors.Is(err, ErrQueueFull) {
		t.Fatalf("over the cap: %v, want ErrQueueFull", err)
	}
	if _, err := st.Enqueue(Command{ProfileID: "p", ConversationID: "other", Kind: KindPause}); err != nil {
		t.Fatalf("another chat is not affected: %v", err)
	}
}

func TestSendStartsATurnAndTheSessionReturnsToIdle(t *testing.T) {
	h, l, st := newHost(t)
	c := send(t, st, "c1", "t1", "hello")
	if err := h.Tick(); err != nil {
		t.Fatal(err)
	}
	if got := l.started; len(got) != 1 || got[0] != "t1:hello" {
		t.Fatalf("started = %v", got)
	}
	x := state(t, st, "c1")
	if x.State != StateRunning || x.TurnID != "t1" || x.TurnPID == 0 {
		t.Fatalf("session = %+v", x)
	}
	if got, _, _ := st.GetCommand(c.ID); got.Status != CmdDone {
		t.Fatalf("command = %+v", got)
	}
	l.turn("t1").end()
	h.Wait()
	if x := state(t, st, "c1"); x.State != StateIdle || x.TurnID != "" || x.TurnPID != 0 {
		t.Fatalf("after the turn: %+v", x)
	}
	if len(l.finished) != 1 || l.finished[0] != "t1" {
		t.Fatalf("finish calls = %v", l.finished)
	}
}

func TestASendToABusyChatWaitsItsTurnInOrder(t *testing.T) {
	h, l, st := newHost(t)
	send(t, st, "c1", "t1", "one")
	send(t, st, "c1", "t2", "two")
	send(t, st, "c1", "t3", "three")
	send(t, st, "c2", "u1", "other chat")
	if err := h.Tick(); err != nil {
		t.Fatal(err)
	}
	if len(l.started) != 2 { // t1 and u1: one turn per chat at a time
		t.Fatalf("started = %v", l.started)
	}
	l.turn("t1").end()
	waitFor(t, "c1 to be idle", func() bool { return state(t, st, "c1").State == StateIdle })
	_ = h.Tick()
	if len(l.started) != 3 || l.started[2] != "t2:two" {
		t.Fatalf("started = %v, want t2 next", l.started)
	}
}

func TestPausedSessionStartsNothingUntilResumed(t *testing.T) {
	h, l, st := newHost(t)
	st.Enqueue(Command{ProfileID: "p", ConversationID: "c1", Kind: KindPause})
	send(t, st, "c1", "t1", "hello")
	_ = h.Tick()
	if len(l.started) != 0 || state(t, st, "c1").State != StatePaused {
		t.Fatalf("started=%v state=%v", l.started, state(t, st, "c1").State)
	}
	st.Enqueue(Command{ProfileID: "p", ConversationID: "c1", Kind: KindResume})
	_ = h.Tick()
	_ = h.Tick()
	if len(l.started) != 1 {
		t.Fatalf("started = %v after resume", l.started)
	}
}

func TestPausingARunningSessionLetsItsTurnFinishAndStaysPaused(t *testing.T) {
	h, l, st := newHost(t)
	send(t, st, "c1", "t1", "hello")
	_ = h.Tick()
	st.Enqueue(Command{ProfileID: "p", ConversationID: "c1", Kind: KindPause})
	_ = h.Tick()
	l.turn("t1").end()
	h.Wait()
	if x := state(t, st, "c1"); x.State != StatePaused || x.TurnID != "" {
		t.Fatalf("session = %+v", x)
	}
}

func TestStopEndsTheRunningTurnAndFailsWhenThereIsNone(t *testing.T) {
	h, l, st := newHost(t)
	noTurn, _ := st.Enqueue(Command{ProfileID: "p", ConversationID: "c1", Kind: KindStop})
	_ = h.Tick()
	if got, _, _ := st.GetCommand(noTurn.ID); got.Status != CmdFailed {
		t.Fatalf("stop with no turn = %+v", got)
	}
	send(t, st, "c1", "t1", "hello")
	_ = h.Tick()
	st.Enqueue(Command{ProfileID: "p", ConversationID: "c1", Kind: KindStop})
	_ = h.Tick()
	h.Wait()
	if !l.turn("t1").stopped || state(t, st, "c1").State != StateIdle {
		t.Fatalf("stopped=%v state=%v", l.turn("t1").stopped, state(t, st, "c1").State)
	}
}

func TestAStartFailureFailsTheCommandAndLeavesTheSessionIdle(t *testing.T) {
	h, l, st := newHost(t)
	l.startErr = errors.New("no such runtime")
	c := send(t, st, "c1", "t1", "hello")
	_ = h.Tick()
	got, _, _ := st.GetCommand(c.ID)
	if got.Status != CmdFailed || !strings.Contains(got.Error, "no such runtime") || state(t, st, "c1").State != StateIdle {
		t.Fatalf("command=%+v state=%v", got, state(t, st, "c1").State)
	}
}

func TestRecoverReattachesToALiveTurnAndClosesADeadOne(t *testing.T) {
	h, l, st := newHost(t)
	live, dead := newFakeTurn(7), newFakeTurn(8)
	l.alive[7] = live
	for _, x := range []Session{
		{ProfileID: "p", ConversationID: "live", State: StateRunning, TurnID: "tl", TurnPID: 7, TurnStamp: "stamp"},
		{ProfileID: "p", ConversationID: "dead", State: StateRunning, TurnID: "td", TurnPID: 8, TurnStamp: "stamp"},
	} {
		if _, err := st.EnsureSession(x.ProfileID, x.ConversationID); err != nil {
			t.Fatal(err)
		}
		if err := st.SaveSession(x); err != nil {
			t.Fatal(err)
		}
	}
	_ = dead
	if err := h.Recover(); err != nil {
		t.Fatal(err)
	}
	if x := state(t, st, "dead"); x.State != StateIdle || x.TurnID != "" {
		t.Fatalf("dead session = %+v", x)
	}
	if len(l.finished) != 1 || l.finished[0] != "td" {
		t.Fatalf("finish calls = %v, want only the dead turn", l.finished)
	}
	if x := state(t, st, "live"); x.State != StateRunning {
		t.Fatalf("live session = %+v", x)
	}
	send(t, st, "live", "t2", "queued behind the live turn")
	_ = h.Tick()
	if len(l.started) != 0 {
		t.Fatalf("started a second turn in a chat with a live one: %v", l.started)
	}
	live.end()
	h.Wait()
	_ = h.Tick()
	if len(l.started) != 1 {
		t.Fatalf("started = %v, want the queued turn after the live one ended", l.started)
	}
}

func TestRunLeavesTurnsRunningWhenTheHostStops(t *testing.T) {
	h, l, st := newHost(t)
	h.Poll = 5 * time.Millisecond
	send(t, st, "c1", "t1", "hello")
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- h.Run(ctx) }()
	waitFor(t, "the turn to start", func() bool { l.mu.Lock(); defer l.mu.Unlock(); return len(l.started) == 1 })
	cancel()
	if err := <-done; err != nil {
		t.Fatal(err)
	}
	if l.turn("t1").stopped {
		t.Fatal("the host stopped a turn when it exited")
	}
	if x := state(t, st, "c1"); x.State != StateRunning || x.TurnPID == 0 {
		t.Fatalf("session = %+v, want it still running for the next host", x)
	}
}

func TestStopDropsTheSendsQueuedBehindTheRunningTurn(t *testing.T) {
	h, l, st := newHost(t)
	send(t, st, "c1", "t1", "one")
	_ = h.Tick()
	b := send(t, st, "c1", "t2", "two")
	st.Enqueue(Command{ProfileID: "p", ConversationID: "c1", Kind: KindStop})
	_ = h.Tick()
	waitFor(t, "c1 to be idle", func() bool { return state(t, st, "c1").State == StateIdle })
	_ = h.Tick()
	if len(l.started) != 1 {
		t.Fatalf("started = %v, want no second turn after a stop", l.started)
	}
	if got, _, _ := st.GetCommand(b.ID); got.Status != CmdFailed || !strings.Contains(got.Error, "stop") {
		t.Fatalf("queued send = %+v", got)
	}
}

func TestBlockedSendsCannotStarveALaterResume(t *testing.T) {
	h, l, st := newHost(t)
	for c := 0; c < 5; c++ { // 5 paused chats x 50 sends is more than one Pending window
		conv := string(rune('a' + c))
		st.Enqueue(Command{ProfileID: "p", ConversationID: conv, Kind: KindPause})
		_ = h.Tick()
		for i := 0; i < MaxPendingPerChat; i++ {
			send(t, st, conv, conv+"-"+strings.Repeat("x", i%3)+string(rune('A'+i%26))+string(rune('a'+i/26)), "m")
		}
	}
	st.Enqueue(Command{ProfileID: "p", ConversationID: "e", Kind: KindResume})
	for i := 0; i < 2; i++ {
		if err := h.Tick(); err != nil {
			t.Fatal(err)
		}
	}
	if len(l.started) == 0 {
		t.Fatal("a resume queued behind 250 blocked sends never ran")
	}
}

func TestASendThatWaitedTooLongExpires(t *testing.T) {
	h, l, st := newHost(t)
	c := send(t, st, "c1", "t1", "hello")
	later := time.Now().Add(MaxSendAge + time.Minute)
	st.now = func() time.Time { return later }
	_ = h.Tick()
	if got, _, _ := st.GetCommand(c.ID); got.Status != CmdFailed || !strings.Contains(got.Error, "expired") || len(l.started) != 0 {
		t.Fatalf("command = %+v, started %v", got, l.started)
	}
}

func TestSaveSessionRefusesPid1(t *testing.T) {
	_, _, st := newHost(t)
	x, _ := st.EnsureSession("p", "c1")
	x.TurnPID = 1
	if err := st.SaveSession(x); err == nil {
		t.Fatal("pid 1 was saved as a turn's pid")
	}
}

// Commands run in the order they were queued: a send followed by a stop on an
// idle chat starts the turn and then stops it, and never the other way round.
func TestCommandsRunInTheOrderTheyWereQueued(t *testing.T) {
	h, l, st := newHost(t)
	send(t, st, "c1", "t1", "hello")
	st.Enqueue(Command{ProfileID: "p", ConversationID: "c1", Kind: KindStop})
	send(t, st, "c1", "t2", "after the stop")
	if err := h.Tick(); err != nil {
		t.Fatal(err)
	}
	if len(l.started) != 1 || !l.turn("t1").stopped {
		t.Fatalf("started=%v stopped=%v: want the first send started, then stopped", l.started, l.turn("t1") != nil && l.turn("t1").stopped)
	}
	waitFor(t, "c1 to be idle", func() bool { return state(t, st, "c1").State == StateIdle })
	_ = h.Tick()
	if len(l.started) != 2 || l.started[1] != "t2:after the stop" {
		t.Fatalf("started = %v: a send queued after the stop must run once the chat is free", l.started)
	}
}

func TestSendThenPauseOnAnIdleChatStartsTheTurnThenPauses(t *testing.T) {
	h, l, st := newHost(t)
	send(t, st, "c1", "t1", "hello")
	st.Enqueue(Command{ProfileID: "p", ConversationID: "c1", Kind: KindPause})
	_ = h.Tick()
	if len(l.started) != 1 || state(t, st, "c1").State != StatePaused {
		t.Fatalf("started=%v state=%v", l.started, state(t, st, "c1").State)
	}
}

// The turn is recorded before its process starts, so an app that starts up while
// the host is still admitting it does not take it for an orphan.
func TestTheTurnIsRecordedBeforeItStarts(t *testing.T) {
	h, l, st := newHost(t)
	var during Session
	l.onStart = func() { during, _, _ = st.GetSession("p", "c1") }
	send(t, st, "c1", "t1", "hello")
	_ = h.Tick()
	if during.TurnID != "t1" || during.State != StateRunning || during.TurnPID != 0 {
		t.Fatalf("session during Start = %+v", during)
	}
	if x := state(t, st, "c1"); x.TurnPID == 0 {
		t.Fatalf("session after Start = %+v, want the pid recorded", x)
	}
}
