package doomsday

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/monoes/mono-agent/internal/coderhost"
	"github.com/monoes/mono-agent/internal/doomsday/stop"
	"github.com/monoes/mono-agent/internal/doomsday/store"
)

type macCounter struct {
	in store.Keyer
	n  atomic.Int32
}

func (k *macCounter) MAC(d string, p ...[]byte) ([]byte, error) { k.n.Add(1); return k.in.MAC(d, p...) }

// Building what a coder host builds (the gate over the chained store, the ops route, the freshness check) and dispatching with
// no run makes no pinned connection, reads no key and warns about nothing: the chain is built when a run needs it.
func TestBuildingTheHostGateAndOpsWithNoRunPinsNothing(t *testing.T) {
	var pins, warns atomic.Int32
	real := PinFunc
	PinFunc = func(context.Context, stop.PinOptions) (*stop.Pinned, error) {
		pins.Add(1)
		return nil, errors.New("counted")
	}
	defer func() { PinFunc = real }()

	r, db := newWire(t, func() error { return nil }, fakeKernel{}, nil)
	keys := &macCounter{in: store.NewHMACKeyer([]byte("k"))}
	w := r.w
	w.RunDir = stop.RunDir{Dir: t.TempDir() + "/fresh-run"} // a pin cache key no earlier test used
	w.Store = store.New(db.DB, keys)
	ws := *w.Witness
	ws.Keys = keys
	ws.Warn = func(string) { warns.Add(1) }
	w.Witness = &ws
	pins.Store(0)
	keys.n.Store(0)

	g := &HostGate{HostLock: hostLockFor(t), Store: w.Chained(), Fresh: w.Fresh(), Profile: "p1", RunDir: w.RunDir, BindingOn: func() (bool, error) { return true, nil }}
	h := &coderhost.Host{Store: r.host, L: &fakeLauncher{}}
	g.Install(r.host, h)
	w.InstallOps(r.host)
	for i := 0; i < 5; i++ {
		if err := g.Start(coderhost.PointTurnStart, func() (coderhost.StartedProc, error) { return fakeProc{}, nil }); err != nil {
			t.Fatal(err)
		}
	}
	if pins.Load() != 0 || keys.n.Load() != 0 || warns.Load() != 0 {
		t.Fatalf("with no run: %d pin(s), %d key read(s), %d warning(s)", pins.Load(), keys.n.Load(), warns.Load())
	}
}

// A sequencer that could not be built is not remembered: the next use builds again, and a built one is kept.
func TestSeqStoreRetriesAFailedBuildAndKeepsASuccessfulOne(t *testing.T) {
	var calls atomic.Int32
	want := &stop.Sequencer{}
	s := &SeqStore{Lazy: func(context.Context) (*stop.Sequencer, error) {
		if calls.Add(1) == 1 {
			return nil, errors.New("pin failed")
		}
		return want, nil
	}}
	if _, err := s.seq(context.Background()); err == nil {
		t.Fatal("the first build should fail")
	}
	for i := 0; i < 3; i++ {
		if got, err := s.seq(context.Background()); err != nil || got != want {
			t.Fatalf("use %d: %v %v", i, got, err)
		}
	}
	if calls.Load() != 2 {
		t.Fatalf("the sequencer was built %d times, want 2 (one failure, one success)", calls.Load())
	}
}

// Concurrent first uses build once, and a build in flight (a slow or locked database) holds no lock: a caller whose context
// ends gets out at once instead of queueing behind it.
func TestSeqStoreBuildsOnceAndNeverBlocksACancelledCallerBehindABuild(t *testing.T) {
	var calls atomic.Int32
	started, release := make(chan struct{}), make(chan struct{})
	s := &SeqStore{Lazy: func(context.Context) (*stop.Sequencer, error) {
		if calls.Add(1) == 1 {
			close(started)
		}
		<-release
		return &stop.Sequencer{}, nil
	}}
	first := make(chan error, 1)
	go func() { _, err := s.seq(context.Background()); first <- err }()
	<-started
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	done := make(chan error, 1)
	go func() { _, err := s.seq(ctx); done <- err }()
	select {
	case err := <-done:
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("got %v", err)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("a caller with an ended context waited behind a build in flight")
	}
	var wg sync.WaitGroup
	for i := 0; i < 5; i++ {
		wg.Add(1)
		go func() { defer wg.Done(); _, _ = s.seq(context.Background()) }()
	}
	close(release)
	wg.Wait()
	if err := <-first; err != nil || calls.Load() != 1 {
		t.Fatalf("err %v, built %d times, want once", err, calls.Load())
	}
}

// With no run, admission through the gate and the freshness check make no run-directory trace: nothing creates the
// directory, the generation counter or the witness, so HasRunTraces stays false for an install that never ran the mode.
func TestAdmissionWithNoRunCreatesNoRunDirectoryTrace(t *testing.T) {
	real := PinFunc
	PinFunc = func(context.Context, stop.PinOptions) (*stop.Pinned, error) { return nil, errors.New("counted") }
	defer func() { PinFunc = real }()
	r, _ := newWire(t, func() error { return nil }, fakeKernel{}, nil)
	w := r.w
	w.RunDir = stop.RunDir{Dir: t.TempDir() + "/never-made"}
	g := &HostGate{HostLock: hostLockFor(t), Store: w.Chained(), Fresh: w.Fresh(), Profile: "p1", RunDir: w.RunDir, BindingOn: func() (bool, error) { return true, nil }}
	for i := 0; i < 3; i++ {
		if err := g.Start(coderhost.PointTurnStart, func() (coderhost.StartedProc, error) { return fakeProc{}, nil }); err != nil {
			t.Fatal(err)
		}
		if err := g.Hand(coderhost.PointSessionSpawn, func(context.Context) error { return nil }); err != nil {
			t.Fatal(err)
		}
		_, _ = g.PlaceRelease(exec.Command("true"), "p1", "c")
	}
	if _, err := os.Stat(w.RunDir.Dir); !os.IsNotExist(err) {
		t.Fatalf("the run directory exists after admissions with no run: %v", err)
	}
	if w.RunDir.HasRunTraces() {
		t.Fatal("traces with no run")
	}
}
