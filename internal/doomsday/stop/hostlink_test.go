package stop

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"runtime"
	"strings"
	"sync/atomic"
	"syscall"
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
	// The watcher writes its ack after the gate has closed, which a test sees first: wait for Run to return before the
	// temporary directory is removed, or the removal races the ack file and fails with "directory not empty".
	t.Cleanup(func() { cancel(); <-h.end })
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
	go func() {
		got <- ProcessHostLink{Dir: dir, Clock: clk, Wait: 2 * time.Second}.CloseHost(context.Background(), 5)
	}()
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

// Without the emergency message the watcher still closes the gate on the marker alone (the poll is the slow path), and on
// a run state that is no longer running.
func TestWatcherClosesOnTheMarkerAloneAndOnTheRunState(t *testing.T) {
	h, cancel := newHostRun(t)
	defer cancel()
	if _, err := h.dir.WriteMarker(5, "ch", "operator"); err != nil {
		t.Fatal(err)
	}
	h.advanceUntil(t, func() bool { return h.gate.Sealed() })

	dir := RunDir{Dir: t.TempDir()}
	clk := newFakeClock()
	g := NewGate(GateConfig{Generation: 5, Reg: &fakeRegistry{}, Clock: clk})
	var stopped atomic.Bool
	w := NewHostWatcher(&HostWatcher{Gate: g, Dir: dir, Gen: 5, CharterID: "ch", Clock: clk, Tick: time.Second,
		Stops: func(context.Context) bool { return stopped.Load() }})
	ctx, cancel2 := context.WithCancel(context.Background())
	defer cancel2()
	runEnd := make(chan struct{})
	go func() { w.Run(ctx); close(runEnd) }()
	t.Cleanup(func() { cancel2(); <-runEnd })
	stopped.Store(true)
	for i := 0; i < 2000 && !g.Sealed(); i++ {
		clk.Advance(time.Second)
		time.Sleep(time.Millisecond)
	}
	if !g.Sealed() {
		t.Fatal("the gate did not close when the run state left running")
	}
}

// plantAck writes an ack the way a process that can only create files in the run directory would: valid JSON, no key.
func plantAck(t *testing.T, dir RunDir, a HostAck) {
	t.Helper()
	b, err := json.Marshal(a)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(dir.AckPath(a.Generation), b, 0o600); err != nil {
		t.Fatal(err)
	}
}

// An ack nobody signed is no ack: a planted BARRIER must not read as "host closed". Mutant tried: ReadAck without the MAC
// check: red.
func TestAPlantedUnsignedAckIsNotTrusted(t *testing.T) {
	dir := RunDir{Dir: t.TempDir()}
	if err := dir.CreateSecret(5); err != nil {
		t.Fatal(err)
	}
	plantAck(t, dir, HostAck{Generation: 5, Kind: int(Barrier)})
	plantAck(t, dir, HostAck{Generation: 5, Kind: int(Barrier), Nonce: "n", MAC: "00"})
	if _, ok := dir.ReadAck(5); ok {
		t.Fatal("an ack with a bad MAC was read")
	}
	// a live host (it listens, never acks): the planted ack must not end the wait as closed
	srv := &EmergencyServer{Dir: dir, Gen: 5, UID: uint32(os.Getuid()), OnStop: func(uint64) {}}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	if err := srv.Start(ctx); err != nil {
		t.Fatal(err)
	}
	clk := newStepClock()
	got := ProcessHostLink{Dir: dir, Clock: clk, Wait: time.Second}.CloseHost(context.Background(), 5)
	if got.Kind != HostUnproven {
		t.Fatalf("a planted ack gave %v", got.Kind)
	}
}

// A signed ack survives a tampered field, a wrong generation and a missing secret. Mutants tried: the MAC covering fewer
// fields, ReadAck without the generation check (M2), no nonce requirement: red.
func TestAckMACCoversEveryFieldAndTheGeneration(t *testing.T) {
	dir := RunDir{Dir: t.TempDir()}
	for _, g := range []uint64{5, 6} {
		if err := dir.CreateSecret(g); err != nil {
			t.Fatal(err)
		}
	}
	if err := dir.WriteAck(HostAck{Generation: 5, Kind: int(Barrier)}); err != nil {
		t.Fatal(err)
	}
	a, ok := dir.ReadAck(5)
	if !ok || a.Nonce == "" || a.MAC == "" {
		t.Fatalf("a written ack did not read back signed: %+v %v", a, ok)
	}
	for name, mut := range map[string]func(*HostAck){
		"kind":    func(a *HostAck) { a.Kind = int(ClosedWithUnknown) },
		"unk":     func(a *HostAck) { a.Unknown = []uint64{1} },
		"pend":    func(a *HostAck) { a.Pending = []uint64{1} },
		"nonce":   func(a *HostAck) { a.Nonce = "other" },
		"nononce": func(a *HostAck) { a.Nonce = "" },
	} {
		b := a
		mut(&b)
		plantAck(t, dir, b)
		if _, ok := dir.ReadAck(5); ok {
			t.Errorf("%s: a tampered ack was read", name)
		}
	}
	// generation 5's valid ack copied to generation 6's name is refused by the generation check, not only by the MAC
	sec5, _ := dir.ReadSecret(5)
	if err := os.WriteFile(dir.SecretPath(6), sec5, 0o600); err != nil { // same key: only the generation check can refuse
		t.Fatal(err)
	}
	good, _ := json.Marshal(a)
	if err := os.WriteFile(dir.AckPath(6), good, 0o600); err != nil {
		t.Fatal(err)
	}
	if _, ok := dir.ReadAck(6); ok {
		t.Fatal("another generation's ack was read")
	}
	// the generation check alone: an ack that carries generation 5 but is signed with generation 6's secret still names 5
	sec6, _ := dir.ReadSecret(6)
	forged := a
	forged.Generation = 6
	forged.MAC = ackMAC(sec6, forged)
	fb, _ := json.Marshal(forged)
	if err := os.WriteFile(dir.AckPath(5), fb, 0o600); err != nil {
		t.Fatal(err)
	}
	if _, ok := dir.ReadAck(5); ok {
		t.Fatal("an ack of generation 6 was read as generation 5")
	}
	// no secret: nothing verifies
	if err := dir.WriteAck(HostAck{Generation: 5, Kind: int(Barrier)}); err != nil {
		t.Fatal(err)
	}
	_ = os.Remove(dir.SecretPath(5))
	if _, ok := dir.ReadAck(5); ok {
		t.Fatal("an ack was read without a secret to verify it")
	}
	if err := dir.WriteAck(HostAck{Generation: 5}); err == nil {
		t.Fatal("an ack was signed without a secret")
	}
}

// A new host incarnation clears an earlier ack. Mutant tried: reachLocked-style removal dropped: see the cli test.
func TestRemoveAckClearsAndIsIdempotent(t *testing.T) {
	dir := RunDir{Dir: t.TempDir()}
	if err := dir.CreateSecret(5); err != nil {
		t.Fatal(err)
	}
	if err := dir.RemoveAck(5); err != nil {
		t.Fatalf("no ack: %v", err)
	}
	if err := dir.WriteAck(HostAck{Generation: 5, Kind: int(Barrier)}); err != nil {
		t.Fatal(err)
	}
	if err := dir.RemoveAck(5); err != nil {
		t.Fatal(err)
	}
	if _, ok := dir.ReadAck(5); ok {
		t.Fatal("the ack survived RemoveAck")
	}
}

// The socket missing is not proof of absence: with the host lock held the host is alive, whatever the socket says. Mutant
// tried: HostNone on the socket alone (no hostAbsent): red.
func TestAMissingSocketOfALiveHostIsNotAbsence(t *testing.T) {
	dir := RunDir{Dir: t.TempDir()}
	if err := dir.CreateSecret(5); err != nil {
		t.Fatal(err)
	}
	release, err := AcquireHost(dir) // the live host
	if err != nil {
		t.Skipf("no host lock here: %v", err)
	}
	defer release()
	clk := newStepClock()
	got := ProcessHostLink{Dir: dir, Clock: clk, Wait: time.Second}.CloseHost(context.Background(), 5)
	if got.Kind != HostUnproven {
		t.Fatalf("a live host (lock held, no socket) read as %v", got.Kind)
	}
	release()
	got = ProcessHostLink{Dir: dir, Clock: clk, Wait: time.Second}.CloseHost(context.Background(), 5)
	if got.Kind != HostNone {
		t.Fatalf("no host, free lock: %v", got.Kind)
	}
	// a lock that cannot be taken for a reason other than being held is not absence either
	got = ProcessHostLink{Dir: dir, Clock: clk, Wait: time.Second, Lock: func(RunDir) (func(), error) { return nil, errors.New("eio") }}.CloseHost(context.Background(), 5)
	if got.Kind != HostUnproven {
		t.Fatalf("an unusable lock proved absence: %v", got.Kind)
	}
}

// Only a missing or refusing socket is "absent"; a timeout or a permission error is not. Mutant tried (M8): any dial error
// absent: red.
func TestAbsentIsOnlyAMissingOrRefusingSocket(t *testing.T) {
	for err, want := range map[error]bool{
		os.ErrNotExist:            true,
		syscall.ECONNREFUSED:      true,
		syscall.EACCES:            false,
		syscall.ETIMEDOUT:         false,
		syscall.EAGAIN:            false,
		errors.New("i/o timeout"): false,
	} {
		if absent(err) != want {
			t.Errorf("absent(%v) = %v", err, !want)
		}
	}
}

type blockingReg struct {
	fakeRegistry
	in, out chan struct{}
}

func (b *blockingReg) RecordStarted(t TicketInfo, h string) error {
	close(b.in)
	<-b.out
	return b.fakeRegistry.RecordStarted(t, h)
}

// T_hung counts a start call that does not return, not the host's own slow registry write of Started: a write stalled for
// many T_hung does not end a healthy host. Mutant tried: HungStarters without the recording exemption: red. A starter in
// BeginStart that never reaches Started is still hung.
func TestASlowRegistryWriteOfStartedIsNotAHungStarter(t *testing.T) {
	clk := newFakeClock()
	reg := &blockingReg{in: make(chan struct{}), out: make(chan struct{})}
	g := NewGate(GateConfig{Generation: 5, Reg: reg, Clock: clk, TClose: 10 * time.Second, THung: 30 * time.Second})
	tk, _ := g.Admit(Spec{Kind: KindE1, Op: "turn.start"})
	_ = g.BeginStart(tk)
	hung, _ := g.Admit(Spec{Kind: KindE1, Op: "turn.start"})
	_ = g.BeginStart(hung)
	done := make(chan error, 1)
	go func() { done <- g.Started(tk, "h") }()
	<-reg.in
	clk.Advance(10 * DefaultTHung)
	h := g.HungStarters()
	if len(h) != 1 || h[0] != hung.ID() {
		t.Fatalf("hung = %v: only the starter that never reached its registry write is hung (recording ticket %d, other %d)", h, tk.ID(), hung.ID())
	}
	close(reg.out)
	if err := <-done; err != nil {
		t.Fatal(err)
	}
}

// The enforcer goroutine ends with the watcher: after the gate closed no EnforceHung goroutine is left. Mutant tried: the
// goroutine bound to the caller's context only: red (it lives on).
func TestEnforcerStopsWhenTheGateCloses(t *testing.T) {
	dir := RunDir{Dir: t.TempDir()}
	clk := newFakeClock()
	g := NewGate(GateConfig{Generation: 5, Reg: &fakeRegistry{}, Clock: clk, TClose: 10 * time.Second, THung: 30 * time.Second})
	w := NewHostWatcher(&HostWatcher{Gate: g, Dir: dir, Gen: 5, CharterID: "ch", Clock: clk, Tick: time.Second, Exit: func([]uint64) {}})
	end := make(chan struct{})
	go func() { w.Run(context.Background()); close(end) }()
	if _, err := dir.WriteMarker(5, "ch", "operator"); err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 2000; i++ {
		select {
		case <-end:
			i = 2000
		default:
			clk.Advance(time.Second)
			time.Sleep(time.Millisecond)
		}
	}
	select {
	case <-end:
	default:
		t.Fatal("the watcher did not return")
	}
	for i := 0; i < 2000; i++ {
		buf := make([]byte, 1<<20)
		n := runtime.Stack(buf, true)
		if !strings.Contains(string(buf[:n]), "stop.EnforceHung") && !strings.Contains(string(buf[:n]), "(*HostWatcher).Run.func1") {
			return
		}
		clk.Advance(time.Second)
		time.Sleep(time.Millisecond)
	}
	t.Fatal("the enforcer goroutine outlived the gate")
}

// The enforcer is stopped the moment the watcher closes the gate, not when Run returns: Close waits for a starter that is
// still in flight (up to TClose), and the host must not exit itself under the Stop that is draining it, even when the
// starter's age passes T_hung meanwhile. Mutant tried: removing the stopEnforce call before closeAndAck (the deferred
// one runs only after Close returned) makes Exit fire during the drain: red.
func TestEnforcerDoesNotEndTheHostWhileTheClosingGateDrains(t *testing.T) {
	dir := RunDir{Dir: t.TempDir()}
	clk := newFakeClock()
	g := NewGate(GateConfig{Generation: 5, Reg: &fakeRegistry{}, Clock: clk, TClose: 120 * time.Second, THung: 30 * time.Second})
	tk, err := g.Admit(Spec{Kind: KindE1, Op: "turn.start"})
	if err != nil {
		t.Fatal(err)
	}
	if err := g.BeginStart(tk); err != nil { // a call in flight that never returns
		t.Fatal(err)
	}
	var exits atomic.Int32
	w := NewHostWatcher(&HostWatcher{Gate: g, Dir: dir, Gen: 5, CharterID: "ch", Clock: clk, Tick: time.Second, Exit: func([]uint64) { exits.Add(1) }})
	ctx, cancel := context.WithCancel(context.Background())
	end := make(chan struct{})
	go func() { w.Run(ctx); close(end) }()
	t.Cleanup(func() { // the drain ends at TClose on the fake clock: let it
		cancel()
		for i := 0; i < 4000; i++ {
			select {
			case <-end:
				return
			default:
				clk.Advance(5 * time.Second)
				time.Sleep(time.Millisecond)
			}
		}
		t.Error("the watcher did not return")
	})
	if _, err := dir.WriteMarker(5, "ch", "operator"); err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 2000 && !g.Sealed(); i++ { // the poll sees the marker and the drain begins
		clk.Advance(time.Second)
		time.Sleep(time.Millisecond)
	}
	if !g.Sealed() {
		t.Fatal("the gate did not close on the marker")
	}
	for i := 0; i < 60; i++ { // the starter is now older than T_hung, the drain is still waiting
		clk.Advance(time.Second)
		time.Sleep(2 * time.Millisecond)
	}
	if n := exits.Load(); n != 0 {
		t.Fatalf("the host ended itself %d times while the closing gate drained a starter", n)
	}
}
