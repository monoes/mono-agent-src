package doomsday

import (
	"context"
	"os"
	"strings"
	"testing"
)

// The containment unit exists, proven and recorded, before the run is recorded: no unit, no run.
func TestStartCreatesAndRecordsTheUnitBeforeTheRun(t *testing.T) {
	w, db := newWire(t, func() error { return nil }, fakeKernel{}, nil)
	w.start(t)
	if len(w.world.started) != 1 || !strings.HasPrefix(w.world.started[0], "monoagent-run-1-") {
		t.Fatalf("the unit of generation 1 was not started: %v", w.world.started)
	}
	rec, ok, err := w.w.RunDir.ReadUnitFile(1)
	if err != nil || !ok || rec.Name != w.world.started[0] || rec.AnchorPID == 0 {
		t.Fatalf("the unit record is not durable and complete: %+v ok=%v err=%v", rec, ok, err)
	}
	if n := count(t, db, `SELECT COUNT(*) FROM doomsday_runs`); n != 1 {
		t.Fatalf("runs = %d", n)
	}
}

func TestStartWithoutAServiceManagerRefusesAndWritesNothing(t *testing.T) {
	w, db := newWire(t, func() error { return nil }, fakeKernel{}, nil)
	w.w.Host = nil
	e := w.w.Engine()
	w.deps.Engine = func() Engine { return e }
	h := fullHash(t, w.rig, w.charter)
	out, err := run(t, w.deps, "", "start", "--charter", w.charter, "--yes-i-lift", "--hash", h)
	if err == nil || !strings.Contains(err.Error(), "containment unit") {
		t.Fatalf("started with no service manager: %v %q", err, out)
	}
	if n := count(t, db, `SELECT COUNT(*) FROM doomsday_runs`); n != 0 {
		t.Fatalf("a run was recorded without a unit (%d)", n)
	}
	if _, ok, _ := w.w.RunDir.ReadUnitFile(1); ok {
		t.Fatal("a unit record exists for a refused start")
	}
}

func TestStartWithoutAUnitCreatorRefuses(t *testing.T) {
	e := realEngine(t)
	e.ContainmentProbe = func() error { return nil }
	e.CreateUnit = nil
	r := newRig()
	r.deps.Engine = func() Engine { return e }
	file := writeCharter(t, goodCharter)
	h := fullHash(t, r, file)
	if _, err := run(t, r.deps, "", "start", "--charter", file, "--yes-i-lift", "--hash", h); err == nil || !strings.Contains(err.Error(), "containment") {
		t.Fatalf("started with no unit creator: %v", err)
	}
	if s, _ := e.Status(context.Background()); s.Active {
		t.Fatal("a run exists")
	}
}

// A unit created for a start the store then refuses is stopped and its record removed, so it cannot be left
// running with no run to own it.
func TestAStartTheStoreRefusesDiscardsItsUnit(t *testing.T) {
	w, db := newWire(t, func() error { return nil }, fakeKernel{}, nil)
	w.start(t)
	h := fullHash(t, w.rig, w.charter)
	if _, err := run(t, w.deps, "", "start", "--charter", w.charter, "--yes-i-lift", "--hash", h); err == nil {
		t.Fatal("a second run started")
	}
	if len(w.world.started) != 2 || len(w.world.stopped) != 1 || w.world.stopped[0] != w.world.started[1] {
		t.Fatalf("started %v stopped %v: the refused start's unit must be the one stopped", w.world.started, w.world.stopped)
	}
	if _, ok, _ := w.w.RunDir.ReadUnitFile(2); ok {
		t.Fatal("the refused start left a unit record")
	}
	if n := count(t, db, `SELECT COUNT(*) FROM doomsday_runs`); n != 1 {
		t.Fatalf("runs = %d", n)
	}
}

// Stop ends the unit's service once its members are ended, and says "stopped" only then.
func TestStopStopsTheUnitService(t *testing.T) {
	w, _ := newWire(t, func() error { return nil }, fakeKernel{}, nil)
	w.start(t)
	out, err := run(t, w.deps, "", "stop")
	if err != nil || !strings.HasSuffix(out, "stopped\n") {
		t.Fatalf("stop: %v %q", err, out)
	}
	if len(w.world.stopped) != 1 || w.world.stopped[0] != w.world.started[0] {
		t.Fatalf("the run unit's service was not stopped: %v", w.world.stopped)
	}
	if _, err := os.Stat(w.w.RunDir.UnitPath(1)); err != nil {
		t.Fatalf("the record of a stopped unit must stay (a repeated stop proves it gone): %v", err)
	}
}
