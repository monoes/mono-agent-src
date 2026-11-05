//go:build doomsday

package main

import (
	"context"
	"os"
	"sync/atomic"
	"testing"
)

// With no run the engine's wiring is inert: the strategy loop of the coder host does nothing but its precheck (no build, so
// no pinned connection, no vault key, no log line, no run directory), and the gate, the governor and the loop share one
// record of whether a run has been seen.
func TestEngineWiringWithNoRunIsInert(t *testing.T) {
	cfg := sendEnv(t)
	db, err := initDB(cfg)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()

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
	g, hg := processGovernor(cfg, db.DB), processHostGate(cfg, db.DB)
	if g.Activity == nil || g.Activity != hg.Activity {
		t.Fatal("the governor and the dispatch gate do not share one record of a seen run")
	}
}
