package doomsday

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/monoes/mono-agent/internal/coderhost"
	"github.com/monoes/mono-agent/internal/doomsday/stop"
	"github.com/monoes/mono-agent/internal/doomsday/store"
	"github.com/monoes/mono-agent/internal/storage"
)

type fakeTurn struct{ stopped bool }

func (*fakeTurn) PID() int      { return 4242 }
func (*fakeTurn) Stamp() string { return "s" }
func (*fakeTurn) Wait() error   { return nil }
func (t *fakeTurn) Stop()       { t.stopped = true }

type fakeLauncher struct{ starts int }

func (l *fakeLauncher) Start(string, string, string, string, []string, string) (coderhost.Turn, error) {
	l.starts++
	return &fakeTurn{}, nil
}
func (*fakeLauncher) Adopt(int, string) coderhost.Turn      { return nil }
func (*fakeLauncher) Finish(string, string, string, string) {}

type hostRig struct {
	*wireRig
	db   *storage.Database
	l    *fakeLauncher
	h    *coderhost.Host
	gate *HostGate
}

func newHostRig(t *testing.T) *hostRig {
	t.Helper()
	w, db := newWire(t, func() error { return nil }, fakeKernel{}, nil)
	l := &fakeLauncher{}
	h := &coderhost.Host{Store: w.host, L: l}
	g := &HostGate{Store: w.w.Chained(), Fresh: w.w.Fresh(), Profile: "p1", RunDir: w.w.RunDir, BindingOn: func() (bool, error) { return true, nil }}
	g.Install(w.host, h)
	return &hostRig{wireRig: w, db: db, l: l, h: h, gate: g}
}

func (r *hostRig) startTurn() error {
	_, err := r.h.L.Start("p1", "conv", "t1", "hi", nil, "")
	return err
}

func (r *hostRig) spawn() error {
	_, err := r.host.Spawn(coderhost.Caller{ProfileID: "p1", ConversationID: "nobody"}, coderhost.SpawnRequest{})
	return err
}

func (r *hostRig) mint() error {
	_, err := r.host.IssueSessionToken("p1", "conv", "t1")
	return err
}

func refused(err error) bool { return errors.Is(err, coderhost.ErrDispatchRefused) }

// With no open run the gate is a pass-through: the host does what it does without the mode and writes nothing.
func TestHostGateWithNoRunChangesNothing(t *testing.T) {
	r := newHostRig(t)
	if err := r.startTurn(); err != nil || r.l.starts != 1 {
		t.Fatalf("a turn start was refused with no run: %v", err)
	}
	if err := r.spawn(); refused(err) {
		t.Fatalf("a spawn was refused by the barrier with no run: %v", err)
	}
	if err := r.mint(); err != nil {
		t.Fatalf("a token mint was refused with no run: %v", err)
	}
	if n := count(t, r.db, `SELECT COUNT(*) FROM doomsday_audit`); n != 0 {
		t.Fatalf("%d audit row(s) written with no run", n)
	}
}

// With a run, each dispatch takes a ticket (registered in the audit stream).
func TestHostGateGivesEveryDispatchATicketWhileTheRunIsRunning(t *testing.T) {
	r := newHostRig(t)
	r.start(t)
	before := count(t, r.db, `SELECT COUNT(*) FROM doomsday_audit`)
	if err := r.startTurn(); err != nil || r.l.starts != 1 {
		t.Fatalf("turn start: %v", err)
	}
	if err := r.mint(); err != nil {
		t.Fatalf("mint: %v", err)
	}
	if err := r.spawn(); err == nil || refused(err) {
		t.Fatalf("a spawn inside the run must reach the host's own checks, got %v", err)
	}
	if n := count(t, r.db, `SELECT COUNT(*) FROM doomsday_audit WHERE kind = 'ticket.started'`); n != 3 {
		t.Fatalf("%d ticket(s) registered, want 3 (audit before: %d)", n, before)
	}
	if err := r.w.Store.VerifyAudit(context.Background(), "p1", 1); err != nil {
		t.Fatalf("the audit chain broke under tickets: %v", err)
	}
}

func assertNothingDispatches(t *testing.T, r *hostRig) {
	t.Helper()
	starts := r.l.starts
	if err := r.startTurn(); !refused(err) || r.l.starts != starts {
		t.Fatalf("a turn started: %v", err)
	}
	if err := r.spawn(); !refused(err) {
		t.Fatalf("a spawn was not refused by the barrier: %v", err)
	}
	if err := r.mint(); !refused(err) {
		t.Fatalf("a token mint was not refused: %v", err)
	}
}

// After Stop (here a Stop that cannot reach quiescent, so the run stays open) nothing dispatches.
func TestHostGateRefusesEverythingAfterStop(t *testing.T) {
	r := newHostRig(t)
	r.start(t)
	r.w.Kernel = nil // the unit cannot be proven: the run stays cleanup-pending
	e := r.w.Engine()
	r.deps.Engine = func() Engine { return e }
	if out, err := run(t, r.deps, "", "stop"); err == nil {
		t.Fatalf("expected an incomplete stop: %q", out)
	}
	assertNothingDispatches(t, r)
}

// The marker alone is enough: it is written before the database intent, so a dispatch between the two is refused.
func TestHostGateRefusesOnTheMarkerBeforeTheDatabaseKnows(t *testing.T) {
	r := newHostRig(t)
	r.start(t)
	if _, err := r.w.RunDir.WriteMarker(1, "", "test"); err != nil {
		t.Fatal(err)
	}
	if st, _ := r.w.Store.GetRun(context.Background(), "p1", 1); st.State != store.StateRunning {
		t.Fatal("setup: the database must still say running")
	}
	assertNothingDispatches(t, r)
}

// A run state that cannot be read refuses: the barrier fails closed.
func TestHostGateFailsClosedWhenTheRunCannotBeRead(t *testing.T) {
	r := newHostRig(t)
	r.start(t)
	r.db.DB.Close()
	if err := r.startTurn(); !refused(err) || r.l.starts != 0 {
		t.Fatalf("a turn started with an unreadable run state: %v", err)
	}
}

// The tickets of a stopping run name the barrier in the refusal, not a generic failure.
func TestHostGateRefusalNamesTheStop(t *testing.T) {
	r := newHostRig(t)
	r.start(t)
	if _, err := r.w.RunDir.WriteMarker(1, "", "test"); err != nil {
		t.Fatal(err)
	}
	if err := r.startTurn(); err == nil || !strings.Contains(err.Error(), "stopping") {
		t.Fatalf("refusal does not say the run is stopping: %v", err)
	}
}

// A new host incarnation of a generation clears an acknowledgement it finds: nothing an earlier incarnation (or anyone) left
// is read as this host's answer. Mutant tried: reachLocked without RemoveAck: red.
func TestANewHostIncarnationClearsAnExistingAck(t *testing.T) {
	r := newHostRig(t)
	r.start(t)
	if err := r.w.RunDir.WriteAck(stop.HostAck{Generation: 1, Kind: int(stop.Barrier)}); err != nil {
		t.Fatal(err)
	}
	if _, ok := r.w.RunDir.ReadAck(1); !ok {
		t.Fatal("setup: the ack must read before the host exists")
	}
	if err := r.startTurn(); err != nil {
		t.Fatal(err)
	}
	if _, ok := r.w.RunDir.ReadAck(1); ok {
		t.Fatal("a stale ack of the generation survived the new host incarnation")
	}
}

// A gate whose emergency channel cannot be bound refuses the dispatch (a Stop would read the missing socket as "no host").
// Mutant tried: reachLocked ignoring the bind error: red.
func TestAnEmergencySocketThatCannotBeBoundRefusesDispatch(t *testing.T) {
	r := newHostRig(t)
	r.start(t)
	sock := r.w.RunDir.SockPath(1)
	_ = os.Remove(sock)
	if err := os.MkdirAll(filepath.Join(sock, "x"), 0o700); err != nil { // a socket path that cannot be replaced
		t.Fatal(err)
	}
	if err := r.startTurn(); !refused(err) || !strings.Contains(err.Error(), "emergency channel") || r.l.starts != 0 {
		t.Fatalf("a turn started with no emergency channel: %v", err)
	}
}
