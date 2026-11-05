//go:build doomsday

package main

import (
	"context"
	"errors"
	"os"
	"sync/atomic"
	"testing"

	doomsdaycli "github.com/monoes/mono-agent/internal/cli/doomsday"
	"github.com/monoes/mono-agent/internal/coderhost"
	"github.com/monoes/mono-agent/internal/doomsday/stop"
)

// With no run the engine's wiring is inert: the strategy loop of the coder host does nothing but its precheck (no build, so
// no pinned connection, no vault key, no log line, no run directory), and the gate, the governor and the loop share one
// record of whether a run has been seen.
func TestDoomsdayEngineWiringWithNoRunIsInert(t *testing.T) {
	cfg := sendEnv(t)
	db, err := initDB(cfg)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()

	// The process keeps one gate and one governor per profile; an earlier test's (over its own database) must not stand in for these.
	hostGateMu.Lock()
	delete(hostGates, cfg.ProfileID)
	hostGateMu.Unlock()
	governorMu.Lock()
	delete(governors, cfg.ProfileID)
	governorMu.Unlock()
	var pins atomic.Int32
	realPin := doomsdaycli.PinFunc
	doomsdaycli.PinFunc = func(context.Context, stop.PinOptions) (*stop.Pinned, error) {
		pins.Add(1)
		return nil, errors.New("counted")
	}
	defer func() { doomsdaycli.PinFunc = realPin }()

	// What HostBuilt builds for a host: the gate, the ops route, the governor, the loop. None of it may pin with no run.
	hg := processHostGate(cfg, db.DB)
	doomsdayWiring(cfg, db.DB).InstallOps(coderhost.NewStore(db.DB))
	loop := newStrategyLoop(cfg, db.DB)
	if loop.Active == nil {
		t.Fatal("the production strategy loop has no run precheck")
	}
	var logs atomic.Int32
	loop.Logf = func(string, ...any) { logs.Add(1) }
	for i := 0; i < 10; i++ {
		tk, err := loop.Step(context.Background())
		if err != nil || tk.Idle != "no-run" {
			t.Fatalf("tick %d: %+v %v", i, tk, err)
		}
		loop.AfterStep(context.Background())
	}
	if logs.Load() != 0 {
		t.Fatalf("%d log line(s) with no run", logs.Load())
	}
	if _, err := os.Stat(doomsdayRunDirPath(cfg)); !os.IsNotExist(err) {
		t.Fatalf("the run directory exists with no run: %v", err)
	}
	if pins.Load() != 0 {
		t.Fatalf("%d pinned connection(s) made with no run", pins.Load())
	}
	if err := hg.Start(coderhost.PointTurnStart, func() (coderhost.StartedProc, error) { return nil, nil }); err != nil {
		t.Fatalf("a dispatch with no run: %v", err)
	}
	if pins.Load() != 0 {
		t.Fatalf("%d pinned connection(s) made by a dispatch with no run", pins.Load())
	}
	g := processGovernor(cfg, db.DB)
	if g.Activity == nil || g.Activity != hg.Activity {
		t.Fatal("the governor and the dispatch gate do not share one record of a seen run")
	}
}
