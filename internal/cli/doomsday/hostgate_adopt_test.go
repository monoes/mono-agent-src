package doomsday

import (
	"errors"
	"fmt"
	"os/exec"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/monoes/mono-agent/internal/coderhost"
	"github.com/monoes/mono-agent/internal/doomsday/stop"
)

// ----- the host gate's half of the session unit: adopt a started turn, release the holds when the unit ends -----

// adopterPlace stands in for the session unit's placement (the real one is only built for a darwin host).
type adopterPlace struct {
	mu     sync.Mutex
	adopts [][2]any
	err    error
	unit   uint64
}

func (p *adopterPlace) Place(*exec.Cmd) (func(), error)               { return func() {}, nil }
func (p *adopterPlace) PlaceTicket(uint64, *exec.Cmd) (func(), error) { return func() {}, nil }
func (p *adopterPlace) PlaceLeased(*exec.Cmd) error                   { return nil }
func (p *adopterPlace) UnitID() uint64                                { return p.unit }
func (p *adopterPlace) Adopt(pid int, stamp string) error {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.adopts = append(p.adopts, [2]any{pid, stamp})
	return p.err
}

// plainPlace takes a command at birth and has no adoption (cgroup, job object, process group).
type plainPlace struct{}

func (plainPlace) Place(*exec.Cmd) (func(), error)               { return func() {}, nil }
func (plainPlace) PlaceTicket(uint64, *exec.Cmd) (func(), error) { return func() {}, nil }
func (plainPlace) PlaceLeased(*exec.Cmd) error                   { return nil }

func withPlacement(t *testing.T, p stop.Placement, err error) {
	t.Helper()
	old := newPlacer
	newPlacer = func(stop.Kernel, stop.RunDir, uint64) (stop.Placement, error) { return p, err }
	t.Cleanup(func() { newPlacer = old })
}

type holdSpy struct {
	mu       sync.Mutex
	released []uint64
}

func (s *holdSpy) release(unit uint64) {
	s.mu.Lock()
	s.released = append(s.released, unit)
	s.mu.Unlock()
}
func (s *holdSpy) got() []uint64 {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]uint64(nil), s.released...)
}

func adoptGate(t *testing.T) (*HostGate, *wireRig, *holdSpy) {
	t.Helper()
	w, _ := newWire(t, func() error { return nil }, fakeKernel{}, nil)
	spy := &holdSpy{}
	g := &HostGate{Store: w.w.Chained(), Fresh: w.w.Fresh(), Profile: "p1", RunDir: w.w.RunDir, Kernel: w.w.Kernel,
		ReleaseHolds: spy.release, HoldPoll: 5 * time.Millisecond}
	t.Cleanup(g.ReleaseAllHolds)
	return g, w, spy
}

func eventually(t *testing.T, what string, f func() bool) {
	t.Helper()
	for end := time.Now().Add(5 * time.Second); time.Now().Before(end); time.Sleep(5 * time.Millisecond) {
		if f() {
			return
		}
	}
	t.Fatalf("never: %s", what)
}

// With no run open nothing is adopted and nothing is asked of the placement.
func TestAdoptWithNoRunOpenAsksNothing(t *testing.T) {
	g, _, _ := adoptGate(t)
	p := &adopterPlace{unit: 9}
	withPlacement(t, p, nil)
	held, err := g.Adopt("p1", "c1", 123, "1.000001")
	if held || err != nil || len(p.adopts) != 0 {
		t.Fatalf("held=%v err=%v adopts=%v", held, err, p.adopts)
	}
}

// With a run open the started process is handed to the run's unit, and the leader is then the unit's to reap.
func TestAdoptHandsTheStartedProcessToTheRunsUnit(t *testing.T) {
	g, w, _ := adoptGate(t)
	w.start(t)
	p := &adopterPlace{unit: 9}
	withPlacement(t, p, nil)
	held, err := g.Adopt("p1", "c1", 123, "5.000007")
	if !held || err != nil {
		t.Fatalf("held=%v err=%v", held, err)
	}
	if len(p.adopts) != 1 || p.adopts[0] != [2]any{123, "5.000007"} {
		t.Fatalf("adopts %v", p.adopts)
	}
}

// A refusal is an error; whether the launcher still must not reap follows the unit's own word (ErrReapOwned).
func TestAdoptRefusalSaysWhoReaps(t *testing.T) {
	g, w, _ := adoptGate(t)
	w.start(t)
	p := &adopterPlace{unit: 9, err: fmt.Errorf("%w: refused", stop.ErrNotPlaced)}
	withPlacement(t, p, nil)
	if held, err := g.Adopt("p1", "c1", 1234, "5.000007"); held || !errors.Is(err, stop.ErrNotPlaced) {
		t.Fatalf("a refusal before the hold: held=%v err=%v", held, err)
	}
	p.err = fmt.Errorf("%w: %w: refused", stop.ErrNotPlaced, stop.ErrReapOwned)
	if held, err := g.Adopt("p1", "c1", 1234, "5.000007"); !held || err == nil {
		t.Fatalf("a refusal after the hold: held=%v err=%v", held, err)
	}
}

// A placement that takes the process at birth has nothing to adopt: the leader stays the launcher's own.
func TestAdoptIsANoOpForAPlacementThatPlacesAtBirth(t *testing.T) {
	g, w, _ := adoptGate(t)
	w.start(t)
	withPlacement(t, plainPlace{}, nil)
	if held, err := g.Adopt("p1", "c1", 123, "5.000007"); held || err != nil {
		t.Fatalf("held=%v err=%v", held, err)
	}
}

// With a run open and a unit that cannot be reached, or another profile, the adoption fails closed (the launcher ends the child).
func TestAdoptFailsClosed(t *testing.T) {
	g, w, _ := adoptGate(t)
	w.start(t)
	withPlacement(t, nil, fmt.Errorf("%w: no unit", stop.ErrNotPlaced))
	if held, err := g.Adopt("p1", "c1", 123, "1.000001"); held || err == nil {
		t.Fatalf("an unreachable unit: held=%v err=%v", held, err)
	}
	withPlacement(t, &adopterPlace{unit: 9}, nil)
	if _, err := g.Adopt("other", "c1", 123, "1.000001"); err == nil {
		t.Fatal("another profile was adopted")
	}
}

// The holds of a unit are let go when its end line appears and not before; the watch ends with it.
func TestHoldsAreReleasedWhenTheUnitsEndLineAppears(t *testing.T) {
	g, w, spy := adoptGate(t)
	w.start(t)
	withPlacement(t, &adopterPlace{unit: 9}, nil)
	var ended atomic.Bool
	var calls atomic.Int64
	g.UnitEnded = func(gen, unit uint64) (bool, error) {
		calls.Add(1)
		if unit != 9 {
			t.Errorf("asked about unit %d", unit)
		}
		return ended.Load(), nil
	}
	for i := 0; i < 3; i++ { // three turns, one watch
		if _, err := g.Adopt("p1", "c1", 100+i, "1.000001"); err != nil {
			t.Fatal(err)
		}
	}
	eventually(t, "the unit is asked about", func() bool { return calls.Load() >= 3 })
	if got := spy.got(); len(got) != 0 {
		t.Fatalf("holds were released before the unit ended: %v", got)
	}
	ended.Store(true)
	eventually(t, "the holds are released", func() bool { return len(spy.got()) == 1 })
	if got := spy.got(); got[0] != 9 {
		t.Fatalf("released %v", got)
	}
	before := calls.Load()
	time.Sleep(60 * time.Millisecond)
	if calls.Load() != before || len(spy.got()) != 1 {
		t.Fatalf("the watch outlived the unit's end (%d more calls, released %v)", calls.Load()-before, spy.got())
	}
}

// A witness that cannot be read is not an end: the holds stay, and the watch goes on asking.
func TestHoldsAreKeptWhileTheEndCannotBeRead(t *testing.T) {
	g, w, spy := adoptGate(t)
	w.start(t)
	withPlacement(t, &adopterPlace{unit: 9}, nil)
	var calls atomic.Int64
	g.UnitEnded = func(uint64, uint64) (bool, error) { calls.Add(1); return true, errors.New("witness unreadable") }
	if _, err := g.Adopt("p1", "c1", 100, "1.000001"); err != nil {
		t.Fatal(err)
	}
	eventually(t, "the unit is asked about repeatedly", func() bool { return calls.Load() >= 4 })
	if got := spy.got(); len(got) != 0 {
		t.Fatalf("an unreadable end released the holds: %v", got)
	}
}

// At host shutdown every unit's holds are let go, ended or not, and the watches stop.
func TestHoldsAreReleasedAtShutdown(t *testing.T) {
	g, w, spy := adoptGate(t)
	w.start(t)
	var calls atomic.Int64
	g.UnitEnded = func(uint64, uint64) (bool, error) { calls.Add(1); return false, nil }
	withPlacement(t, &adopterPlace{unit: 9}, nil)
	if _, err := g.Adopt("p1", "c1", 100, "1.000001"); err != nil {
		t.Fatal(err)
	}
	withPlacement(t, &adopterPlace{unit: 11}, nil)
	if _, err := g.Adopt("p1", "c2", 101, "1.000002"); err != nil {
		t.Fatal(err)
	}
	g.ReleaseAllHolds()
	got := spy.got()
	if len(got) != 2 || !(got[0] == 9 && got[1] == 11 || got[0] == 11 && got[1] == 9) {
		t.Fatalf("released %v", got)
	}
	g.ReleaseAllHolds() // idempotent: nothing is released twice
	if len(spy.got()) != 2 {
		t.Fatalf("released again: %v", spy.got())
	}
	before := calls.Load()
	time.Sleep(60 * time.Millisecond)
	if calls.Load() > before+1 {
		t.Fatalf("the watches outlived the shutdown (%d more calls)", calls.Load()-before)
	}
}

// Install hands the launcher the adopt hook, and it is the gate's.
func TestInstallGivesTheLauncherAnAdoptHook(t *testing.T) {
	w, _ := newWire(t, func() error { return nil }, fakeKernel{}, nil)
	var got Hooks
	h := &coderhost.Host{Store: w.host, L: &fakeLauncher{}}
	g := &HostGate{Store: w.w.Store, Profile: "p1", RunDir: w.w.RunDir, Kernel: w.w.Kernel,
		InstallHooks: func(_ *coderhost.Store, _ *coderhost.Host, hk Hooks) error { got = hk; return nil }}
	if err := g.Install(w.host, h); err != nil {
		t.Fatal(err)
	}
	if got.Adopt == nil {
		t.Fatal("Install did not give the launcher an Adopt hook")
	}
	if held, err := got.Adopt("p1", "c1", 123, "1.000001"); held || err != nil { // no run open
		t.Fatalf("held=%v err=%v", held, err)
	}
}
