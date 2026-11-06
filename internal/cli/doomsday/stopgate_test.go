package doomsday

import (
	"context"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/monoes/mono-agent/internal/doomsday/stop"
)

// The Stop the CLI runs is production-constructed (Wiring.Stopper) and reaches the host's real HostGate: the gate closes on
// the marker, Stop waits while a starter is in flight, and only the gate's BARRIER lets it declare quiescent.
func TestProductionStopperClosesTheHostGateAndWaitsOnAStarter(t *testing.T) {
	r := newHostRig(t)
	r.gate.Tick = 10 * time.Millisecond
	r.start(t)
	ctx := context.Background()
	gate, run, err := r.gate.currentRun(ctx) // the host's first dispatch builds the gate, its watcher and its socket
	if err != nil || gate == nil {
		t.Fatalf("gate: %v %v", gate, err)
	}
	tk, err := gate.Admit(stop.Spec{Kind: stop.KindE1, Op: "turn.start"})
	if err != nil {
		t.Fatal(err)
	}
	if err := gate.BeginStart(tk); err != nil { // a start call in flight
		t.Fatal(err)
	}

	st := r.w.Stopper("operator", r.w.ender())
	if st.Gate == nil && st.Host == nil {
		t.Fatal("the production Stopper has neither a gate nor a host link: it could not see the host")
	}
	done := make(chan stop.Report, 1)
	go func() { done <- st.Stop(ctx, uint64(run.Generation), run.CharterID, "operator stop") }()

	select {
	case rep := <-done:
		t.Fatalf("Stop returned with a starter in flight: %s", strings.Join(rep.Lines(), "\n"))
	case <-time.After(500 * time.Millisecond):
	}
	if !gate.Sealed() {
		t.Fatal("Stop did not close the host's gate")
	}
	if _, err := gate.Admit(stop.Spec{Kind: stop.KindE1, Op: "turn.start"}); err == nil {
		t.Fatal("the closed gate admitted a new ticket")
	}
	gate.Abort(tk) // the starter resolves: the gate drains to a BARRIER
	select {
	case rep := <-done:
		if !rep.Stopped() || rep.Host == nil && rep.Gate == nil {
			t.Fatalf("report: %s", strings.Join(rep.Lines(), "\n"))
		}
		if rep.Host.Kind != stop.HostClosed {
			t.Fatalf("host outcome %v: %s", rep.Host.Kind, rep.Host.Note)
		}
	case <-time.After(20 * time.Second):
		t.Fatal("Stop never finished after the starter ended")
	}
}

// With no host running (nothing holds the generation's gate) Stop is clean without waiting out the ack deadline.
func TestProductionStopperWithNoHostProcessIsHostNone(t *testing.T) {
	r := newHostRig(t)
	r.start(t)
	runs, _ := r.w.Store.OpenRuns(context.Background(), "p1")
	st := r.w.Stopper("operator", r.w.ender())
	start := time.Now()
	rep := st.Stop(context.Background(), uint64(runs[0].Generation), runs[0].CharterID, "operator stop")
	if !rep.Stopped() || rep.Host == nil || rep.Host.Kind != stop.HostNone || time.Since(start) > 5*time.Second {
		t.Fatalf("report: %s", strings.Join(rep.Lines(), "\n"))
	}
}

// The host takes the run directory's host lock when it first builds a gate: a second host is refused, nothing dispatches.
func TestHostGateTakesTheHostLockAndARefusedLockRefusesTheDispatch(t *testing.T) {
	r := newHostRig(t)
	r.start(t)
	r.gate.HostLock = func(stop.RunDir) (func(), error) { return nil, stop.ErrSecondHost }
	if err := r.startTurn(); !refused(err) {
		t.Fatalf("a dispatch ran although another host holds the lock: %v", err)
	}
	var took int
	r.gate.HostLock = func(stop.RunDir) (func(), error) { took++; return func() {}, nil }
	if err := r.startTurn(); err != nil || took != 1 {
		t.Fatalf("turn: %v, lock taken %d times", err, took)
	}
}

// A host whose lock file was removed (a second host could now lock a new one) refuses every dispatch from then on.
func TestHostGateRefusesWhenItsHostLockFileWasRemoved(t *testing.T) {
	r := newHostRig(t)
	r.start(t)
	r.gate.HostLock = nil // the real lock
	// a witness that does not verify the lock: only the gate's own look can refuse (the witness lock refuses too, in production)
	r.gate.Fresh = func(context.Context, int64) error { return nil }
	if err := r.startTurn(); err != nil {
		t.Fatalf("first turn: %v", err)
	}
	if err := r.startTurn(); err != nil { // control: a second turn with the lock in place is fine
		t.Fatalf("second turn: %v", err)
	}
	if runtime.GOOS == "windows" {
		// safe by share mode: the lock handle is opened without FILE_SHARE_DELETE, so the file cannot be removed at all
		if err := os.Remove(filepath.Join(r.gate.RunDir.Dir, "host.lock")); err == nil {
			t.Fatal("a held host lock file was removed on Windows")
		}
		return
	}
	if err := os.Remove(filepath.Join(r.gate.RunDir.Dir, "host.lock")); err != nil {
		t.Fatal(err)
	}
	// the look itself (a turn would also be refused later, by the witness lock its registry write takes)
	if _, _, err := r.gate.currentRun(context.Background()); !refused(err) {
		t.Fatalf("the gate was handed out although the host lock file is gone: %v", err)
	}
	if err := r.startTurn(); !refused(err) {
		t.Fatalf("a dispatch ran although the host lock file is gone: %v", err)
	}
}
