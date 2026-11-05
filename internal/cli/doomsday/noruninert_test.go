//go:build doomsday

package doomsday

import (
	"context"
	"errors"
	"os"
	"sync/atomic"
	"testing"
	"time"

	"github.com/monoes/mono-agent/internal/doomsday/governor/machine"
	"github.com/monoes/mono-agent/internal/doomsday/store"
)

func pinCount() int {
	pinsMu.Lock()
	defer pinsMu.Unlock()
	return len(pins)
}

func dirEntries(t *testing.T, dir string) int {
	t.Helper()
	es, err := os.ReadDir(dir)
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		t.Fatal(err)
	}
	return len(es)
}

func tableTotals(t *testing.T, r *govRig) int {
	n := 0
	for _, tbl := range []string{"doomsday_runs", "doomsday_audit", "doomsday_slots", "doomsday_queue", "doomsday_governor_state"} {
		n += count(t, r.db, `SELECT COUNT(*) FROM `+tbl)
	}
	return n
}

// The loop with Active false does nothing at all: no build (the build is what pins), no log, however many ticks.
func TestStrategyLoopWithNoActiveRunBuildsNothingAndLogsNothing(t *testing.T) {
	var builds, logs, checks atomic.Int32
	l := &StrategyLoop{
		Active: func(context.Context) bool { checks.Add(1); return false },
		Build:  func(context.Context) (*StrategyHost, error) { builds.Add(1); return nil, errors.New("x") },
		Logf:   func(string, ...any) { logs.Add(1) },
	}
	for i := 0; i < 20; i++ {
		if _, err := l.Step(bg); err != nil {
			t.Fatalf("an idle tick reported %v", err)
		}
	}
	if builds.Load() != 0 || logs.Load() != 0 || checks.Load() != 20 {
		t.Fatalf("builds %d logs %d checks %d", builds.Load(), logs.Load(), checks.Load())
	}
}

// With an active run the old behaviour stands: the host is built.
func TestStrategyLoopWithAnActiveRunBuilds(t *testing.T) {
	var builds atomic.Int32
	l := &StrategyLoop{
		Active: func(context.Context) bool { return true },
		Build:  func(context.Context) (*StrategyHost, error) { builds.Add(1); return nil, errors.New("not yet") },
	}
	if _, err := l.Step(bg); err == nil || builds.Load() != 1 {
		t.Fatalf("err %v builds %d", err, builds.Load())
	}
}

// Run loop end to end over the real precheck and the real build: an empty database never makes a pin, writes a row, logs, or
// makes a file in the run directory; the moment a run row exists the same loop builds (and pins).
func TestStrategyLoopOverAnEmptyDatabaseIsInert(t *testing.T) {
	r := newGovRig(t, "")
	r.w.DB = r.db.DB
	var logs atomic.Int32
	act := &RunActivity{}
	l := &StrategyLoop{
		Active: OpenRunPrecheck(r.db.DB, "p1", act, nil),
		Build: func(ctx context.Context) (*StrategyHost, error) {
			return r.w.StrategyHost(ctx, StrategyExtras{JudgeExec: (&fakeJudge{}).exec})
		},
		Logf:      func(string, ...any) { logs.Add(1) },
		AfterStep: r.gw.Tick,
		Every:     time.Millisecond,
	}
	before := tableTotals(t, r)
	ctx, cancel := context.WithTimeout(bg, 10*time.Second)
	defer cancel()
	for i := 0; i < 30; i++ {
		if _, err := l.Step(ctx); err != nil {
			t.Fatal(err)
		}
		l.AfterStep(ctx)
	}
	if pinCount() != 0 || logs.Load() != 0 || tableTotals(t, r) != before || dirEntries(t, r.w.RunDir.Dir) != 0 {
		t.Fatalf("not inert: pins %d logs %d rows %d->%d dir %d", pinCount(), logs.Load(), before, tableTotals(t, r), dirEntries(t, r.w.RunDir.Dir))
	}
}

// The precheck is read-only, needs no key, and flips with the run table; an unreadable table is "not active" and quiet.
func TestOpenRunPrecheckIsAReadOnlyCount(t *testing.T) {
	r := newGovRig(t, "")
	act := &RunActivity{}
	pre := OpenRunPrecheck(r.db.DB, "p1", act, nil)
	if pre(bg) || act.Known() {
		t.Fatal("active with no run")
	}
	seedRun(t, store.New(r.db.DB, store.NewHMACKeyer([]byte("k"))))
	if !pre(bg) || !act.Known() {
		t.Fatal("not active with a running run")
	}
	r2 := newGovRig(t, "")
	r2.db.DB.Close()
	if OpenRunPrecheck(r2.db.DB, "p1", &RunActivity{}, nil)(bg) {
		t.Fatal("active over an unreadable database")
	}
}

type countingKeyer struct {
	in store.Keyer
	n  atomic.Int32
}

func (k *countingKeyer) MAC(d string, p ...[]byte) ([]byte, error) {
	k.n.Add(1)
	return k.in.MAC(d, p...)
}

// The governor with no run: Tick and For read the run listing and nothing else (no key, no sampler, no log, no write).
func TestGovernorTickAndForWithNoRunTouchNothing(t *testing.T) {
	r := newGovRig(t, "")
	var logs atomic.Int32
	keys := &countingKeyer{in: store.NewHMACKeyer([]byte("k"))}
	r.gw.Runs = store.New(r.db.DB, keys)
	r.gw.Logf = func(string, ...any) { logs.Add(1) }
	before := tableTotals(t, r)
	for i := 0; i < 20; i++ {
		r.gw.Tick(bg)
		if rg, err := r.gw.For(bg); rg != nil || err != nil {
			t.Fatalf("%v %v", rg, err)
		}
	}
	if keys.n.Load() != 0 || r.smp.Calls() != 0 || logs.Load() != 0 || tableTotals(t, r) != before || dirEntries(t, r.w.RunDir.Dir) != 0 {
		t.Fatalf("keys %d sampler %d logs %d rows %d->%d", keys.n.Load(), r.smp.Calls(), logs.Load(), before, tableTotals(t, r))
	}
}

// An unreadable table with no run known: the governor admits plainly; once a run was seen it refuses.
func TestGovernorForFailsOpenWithNoRunAndClosedOnceOneIsKnown(t *testing.T) {
	r := newGovRig(t, "")
	if _, err := r.db.DB.Exec(`ALTER TABLE doomsday_runs RENAME TO moved_runs`); err != nil {
		t.Fatal(err)
	}
	if rg, err := r.gw.For(bg); rg != nil || err != nil {
		t.Fatalf("an unreadable table with no run refused: %v %v", rg, err)
	}
	if _, err := r.startTurn("a"); err != nil {
		t.Fatalf("a launch was refused with an unreadable table and no run: %v", err)
	}

	s := newSeededGate(t)
	seedRun(t, s.st)
	gw := &GovernorWire{DB: s.db.DB, Profile: "p1", Keys: store.NewHMACKeyer([]byte("k")), Runs: s.st, RunDir: s.dir, Sampler: machine.NewFake(machine.Step{Sample: machine.Healthy()})}
	if rg, err := gw.For(bg); rg == nil || err != nil {
		t.Fatalf("setup: %v %v", rg, err)
	}
	s.renameRuns(t)
	if _, err := gw.For(bg); err == nil {
		t.Fatal("a known run with an unreadable table was not refused")
	}
}

func TestGovernorForWithARunOnDiskAndAnUnreadableTableRefuses(t *testing.T) {
	s := newSeededGate(t)
	seedRun(t, s.st)
	if err := s.dir.CreateSecret(1); err != nil {
		t.Fatal(err)
	}
	gw := &GovernorWire{DB: s.db.DB, Profile: "p1", Keys: store.NewHMACKeyer([]byte("k")), Runs: s.st, RunDir: s.dir}
	s.renameRuns(t)
	if _, err := gw.For(bg); err == nil { // this governor has not read the row
		t.Fatal("not refused")
	}
}

// A precheck that cannot read the table says so once per fault when a run is known (the loop must not go quiet over a run it
// can no longer see), and not at all when none is known.
func TestOpenRunPrecheckLogsAnUnreadableTableOncePerFaultOnlyWhenARunIsKnown(t *testing.T) {
	s := newSeededGate(t)
	seedRun(t, s.st)
	var known, unknown atomic.Int32
	actKnown := &RunActivity{}
	preKnown := OpenRunPrecheck(s.db.DB, "p1", actKnown, func(string, ...any) { known.Add(1) })
	if !preKnown(bg) {
		t.Fatal("setup: the run must read")
	}
	preUnknown := OpenRunPrecheck(s.db.DB, "p1", &RunActivity{}, func(string, ...any) { unknown.Add(1) })
	s.renameRuns(t)
	for i := 0; i < 5; i++ {
		if preKnown(bg) || preUnknown(bg) {
			t.Fatal("active over an unreadable table")
		}
	}
	if known.Load() != 1 || unknown.Load() != 0 {
		t.Fatalf("log lines: known-run precheck %d (want 1), no-run precheck %d (want 0)", known.Load(), unknown.Load())
	}
}
