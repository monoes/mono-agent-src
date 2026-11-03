package coderhost

import (
	"errors"
	"sync"
	"testing"

	"github.com/monoes/mono-agent/internal/doomsday/stop"
)

type tkReg struct {
	mu      sync.Mutex
	started []stop.TicketInfo
	aborted []stop.TicketInfo
}

func (r *tkReg) RecordStarted(t stop.TicketInfo, _ string) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.started = append(r.started, t)
	return nil
}
func (r *tkReg) RecordAborted(t stop.TicketInfo, _ string) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.aborted = append(r.aborted, t)
	return nil
}
func (r *tkReg) RecordUnknown(stop.TicketInfo) error { return nil }

func (r *tkReg) ops() (out []string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	for _, t := range r.started {
		out = append(out, t.Op)
	}
	return out
}

func tkGate() (StopGate, *stop.Gate, *tkReg) {
	reg := &tkReg{}
	g := stop.NewGate(stop.GateConfig{Generation: 1, Reg: reg, Clock: stop.SystemClock{}})
	return StopGate{G: g}, g, reg
}

func convCount(t *testing.T, st *Store) (n int) {
	t.Helper()
	if err := st.db.QueryRow(`SELECT COUNT(*) FROM coder_sessions`).Scan(&n); err != nil {
		t.Fatal(err)
	}
	return n
}

func tokenCount(t *testing.T, st *Store) (n int) {
	t.Helper()
	if err := st.db.QueryRow(`SELECT COUNT(*) FROM coder_session_tokens`).Scan(&n); err != nil {
		t.Fatal(err)
	}
	return n
}

// Default off: no gate installed means a plain call and the very same launcher.
func TestNoGateNothingIsWired(t *testing.T) {
	l := newFakeLauncher()
	if WrapLauncher(l, nil) != Launcher(l) {
		t.Fatal("a nil gate must return the launcher itself")
	}
	st := newStoreT(t)
	m, _ := master(t, st, "coding")
	spawn(t, st, m)
	if _, err := st.IssueSessionToken("p", m.ConversationID, "t1"); err != nil {
		t.Fatal(err)
	}
}

func TestSpawnNeedsATicketAndIsRefusedAfterTheGateCloses(t *testing.T) {
	st := newStoreT(t)
	m, _ := master(t, st, "coding")
	sg, g, reg := tkGate()
	st.SetDispatchGate(sg)
	spawn(t, st, m)
	if ops := reg.ops(); len(ops) != 1 || ops[0] != string(PointSessionSpawn) {
		t.Fatalf("registry %v", ops)
	}
	before := convCount(t, st)
	g.Seal()
	if _, err := st.Spawn(m, SpawnRequest{}); !errors.Is(err, ErrDispatchRefused) {
		t.Fatalf("spawn after close: %v", err)
	}
	if convCount(t, st) != before {
		t.Fatal("a refused spawn left a session behind")
	}
}

func TestTokenMintNeedsATicketButRevocationDoesNot(t *testing.T) {
	st := newStoreT(t)
	m, _ := master(t, st, "coding")
	c := m.ConversationID
	sg, g, reg := tkGate()
	st.SetDispatchGate(sg)
	if _, err := st.IssueSessionToken("p", c, "t1"); err != nil {
		t.Fatal(err)
	}
	if ops := reg.ops(); len(ops) != 1 || ops[0] != string(PointTokenMint) {
		t.Fatalf("registry %v", ops)
	}
	g.Seal()
	if _, err := st.IssueSessionToken("p", c, "t2"); !errors.Is(err, ErrDispatchRefused) {
		t.Fatalf("mint after close: %v", err)
	}
	if tokenCount(t, st) != 1 {
		t.Fatalf("tokens = %d, want the first one only", tokenCount(t, st))
	}
	// Stop's own step: revocation works through a closed gate.
	if err := st.RevokeSessionTokensErr("p", c); err != nil {
		t.Fatalf("revocation was gated: %v", err)
	}
	if tokenCount(t, st) != 0 {
		t.Fatal("revocation did not remove the token")
	}
}

func TestTurnStartNeedsATicketAndAClosedGateStartsNothing(t *testing.T) {
	h, l, st := newHost(t)
	sg, g, reg := tkGate()
	h.L = WrapLauncher(l, sg)
	if _, ok := h.L.(BudgetLauncher); !ok {
		t.Fatal("wrapping lost the budget launcher")
	}
	m, _ := master(t, st, "coding")
	c := m.ConversationID
	send(t, st, c, "t1", "hello")
	if err := h.Tick(); err != nil {
		t.Fatal(err)
	}
	if len(l.started) != 1 || len(reg.ops()) != 1 || reg.ops()[0] != string(PointTurnStart) {
		t.Fatalf("started %v registry %v", l.started, reg.ops())
	}
	l.turn("t1").end()
	h.Wait()
	g.Seal()
	send(t, st, c, "t2", "again")
	_ = h.Tick()
	if len(l.started) != 1 {
		t.Fatalf("a turn started through a closed gate: %v", l.started)
	}
}

// A ticket that is late (the gate closes while the process is being created) kills the process.
func TestLateTurnStartIsKilled(t *testing.T) {
	l := newFakeLauncher()
	sg, g, _ := tkGate()
	l.onStart = g.Seal
	w := WrapLauncher(l, sg)
	_, err := w.Start("p", "c", "t1", "m", nil, "")
	if !errors.Is(err, ErrDispatchRefused) {
		t.Fatalf("late start: %v", err)
	}
	if tn := l.turn("t1"); tn == nil || !tn.stopped {
		t.Fatal("the late process was not killed")
	}
}

func TestCappedStartGoesThroughTheGateToo(t *testing.T) {
	l := newFakeLauncher()
	sg, g, reg := tkGate()
	w := WrapLauncher(l, sg).(BudgetLauncher)
	if _, err := w.StartCapped("p", "c", "t1", "m", nil, "", 1.5); err != nil {
		t.Fatal(err)
	}
	if len(reg.ops()) != 1 || len(l.caps) != 1 {
		t.Fatalf("registry %v caps %v", reg.ops(), l.caps)
	}
	g.Seal()
	if _, err := w.StartCapped("p", "c", "t2", "m", nil, "", 1.5); !errors.Is(err, ErrDispatchRefused) || len(l.caps) != 1 {
		t.Fatalf("capped start through a closed gate: %v caps %v", err, l.caps)
	}
}
