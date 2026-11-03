package stop

import (
	"context"
	"os"
	"strings"
	"testing"
	"time"
)

type hostRun struct {
	gate *Gate
	dir  RunDir
	w    *HostWatcher
	clk  *fakeClock
	end  chan struct{}
}

func newHostRun(t *testing.T) (*hostRun, context.CancelFunc) {
	t.Helper()
	dir := RunDir{Dir: t.TempDir()}
	clk := newFakeClock()
	g := NewGate(GateConfig{Generation: 5, Reg: &fakeRegistry{}, Clock: clk, TClose: 10 * time.Second, THung: 30 * time.Second})
	h := &hostRun{gate: g, dir: dir, clk: clk, end: make(chan struct{})}
	h.w = NewHostWatcher(&HostWatcher{Gate: g, Dir: dir, Gen: 5, CharterID: "ch", Clock: clk, Tick: 250 * time.Millisecond})
	ctx, cancel := context.WithCancel(context.Background())
	// As in production: the per-run secret and the emergency socket exist before the gate is used.
	if err := dir.CreateSecret(5); err != nil {
		t.Fatal(err)
	}
	srv := &EmergencyServer{Dir: dir, Gen: 5, UID: uint32(os.Getuid()), OnStop: func(uint64) { h.w.Kick() }}
	if err := srv.Start(ctx); err != nil {
		t.Fatal(err)
	}
	go func() { h.w.Run(ctx); close(h.end) }()
	return h, cancel
}

// advance moves the fake clock until done reports true (the watcher and Close wait on it).
func (h *hostRun) advanceUntil(t *testing.T, done func() bool) {
	t.Helper()
	for i := 0; i < 2000 && !done(); i++ {
		h.clk.Advance(250 * time.Millisecond)
		time.Sleep(time.Millisecond)
	}
	if !done() {
		t.Fatal("condition not reached")
	}
}

// A Stop in another process reaches the host's gate through the marker: the gate closes, the Stop waits for a starter
// that is in flight, and only a BARRIER acknowledgement lets it reach quiescent.
func TestCrossProcessStopClosesTheHostGateAndWaitsOnAStarter(t *testing.T) {
	h, cancel := newHostRun(t)
	defer cancel()
	tk, err := h.gate.Admit(Spec{Kind: KindE1, Op: "turn.start"})
	if err != nil {
		t.Fatal(err)
	}
	if err := h.gate.BeginStart(tk); err != nil { // STARTING: the call is in flight
		t.Fatal(err)
	}
	link := ProcessHostLink{Dir: h.dir, Clock: h.clk, Wait: 60 * time.Second}
	var out HostOutcome
	finished := make(chan struct{})
	go func() {
		defer close(finished)
		s := &Stopper{Dir: h.dir, Store: newFakeRunStore(), Ender: &fakeEnder{}, Host: link, Clock: h.clk}
		rep := s.Stop(context.Background(), 5, "ch", "operator")
		out = *rep.Host
		if rep.Stopped() {
			t.Errorf("a Stop with a starter in flight reached quiescent: %s", joined(rep))
		}
	}()
	h.advanceUntil(t, func() bool {
		select {
		case <-finished:
			return true
		default:
			return false
		}
	})
	if !h.gate.Sealed() {
		t.Fatal("the host's gate was not closed by the marker")
	}
	if out.Kind != HostOpen {
		t.Fatalf("host outcome %v: %s", out.Kind, out.Note)
	}
	if _, err := h.gate.Admit(Spec{Kind: KindE1, Op: "turn.start"}); err == nil {
		t.Fatal("the closed gate admitted a ticket")
	}
}

func TestCrossProcessStopWithAnIdleHostIsClosedAndQuiescent(t *testing.T) {
	h, cancel := newHostRun(t)
	defer cancel()
	done := make(chan Report, 1)
	go func() {
		s := &Stopper{Dir: h.dir, Store: newFakeRunStore(), Ender: &fakeEnder{}, Host: ProcessHostLink{Dir: h.dir, Clock: h.clk, Wait: 60 * time.Second}, Clock: h.clk}
		done <- s.Stop(context.Background(), 5, "ch", "operator")
	}()
	var rep Report
	got := false
	h.advanceUntil(t, func() bool {
		select {
		case rep = <-done:
			got = true
		default:
		}
		return got
	})
	if !rep.Stopped() || rep.Host == nil || rep.Host.Kind != HostClosed {
		t.Fatalf("report: %s", joined(rep))
	}
}

// With no link and no gate the Stop cannot see the host: it must not reach quiescent, and says why.
func TestStopWithoutAGateOrAHostLinkFailsClosed(t *testing.T) {
	s, _, _, _ := newStopper(t, nil)
	s.Host = nil
	rep := s.Stop(bg, 7, "ch1", "operator")
	if rep.Stopped() || !strings.Contains(joined(rep), "host's gate is not proven closed") {
		t.Fatalf("report: %s", joined(rep))
	}
}

// A host that never answers (a paused or hung host) is not proof of an empty gate.
func TestAHostThatNeverAcknowledgesIsUnproven(t *testing.T) {
	dir := RunDir{Dir: t.TempDir()}
	if err := dir.CreateSecret(5); err != nil {
		t.Fatal(err)
	}
	clk := newFakeClock()
	srv := &EmergencyServer{Dir: dir, Gen: 5, UID: uint32(os.Getuid()), OnStop: func(uint64) {}} // listens, never acks
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	if err := srv.Start(ctx); err != nil {
		t.Fatal(err)
	}
	got := make(chan HostOutcome, 1)
	go func() { got <- ProcessHostLink{Dir: dir, Clock: clk, Wait: 2 * time.Second}.CloseHost(context.Background(), 5) }()
	for i := 0; i < 4000; i++ {
		select {
		case o := <-got:
			if o.Kind != HostUnproven {
				t.Fatalf("outcome %v", o.Kind)
			}
			return
		default:
			clk.Advance(100 * time.Millisecond)
			time.Sleep(time.Millisecond)
		}
	}
	t.Fatal("never returned")
}

// The emergency message is the fast path: the watcher closes without waiting for its poll.
func TestEmergencyMessageKicksTheWatcher(t *testing.T) {
	h, cancel := newHostRun(t)
	defer cancel()
	if err := SendEmergency(h.dir, 5); err != nil {
		t.Fatal(err)
	}
	h.advanceUntil(t, func() bool { return h.gate.Sealed() })
}

// A start call that never returns makes the host exit itself (crash-only), wired through the watcher.
func TestWatcherEnforcesTheHungStarterRule(t *testing.T) {
	dir := RunDir{Dir: t.TempDir()}
	clk := newFakeClock()
	g := NewGate(GateConfig{Generation: 5, Reg: &fakeRegistry{}, Clock: clk, TClose: 10 * time.Second, THung: 30 * time.Second})
	exited := make(chan []uint64, 1)
	w := NewHostWatcher(&HostWatcher{Gate: g, Dir: dir, Gen: 5, CharterID: "ch", Clock: clk, Tick: time.Second, Exit: func(s []uint64) { exited <- s }})
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go w.Run(ctx)
	tk, _ := g.Admit(Spec{Kind: KindE1, Op: "turn.start"})
	_ = g.BeginStart(tk)
	for i := 0; i < 4000; i++ {
		select {
		case <-exited:
			return
		default:
			clk.Advance(time.Second)
			time.Sleep(time.Millisecond)
		}
	}
	t.Fatal("a starter older than T_hung did not end the host")
}
