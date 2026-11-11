package main

import (
	"path/filepath"
	"testing"

	"github.com/monoes/mono-agent/internal/doomsday/stop"
)

// The kernel the production wiring hands every consumer (the stop order, the finish path, the merge placement and the host gate
// built from it) carries the witness as the session unit's leader ledger, and the host gate can tell when a unit has ended: a
// session unit proven, placed into or adopted into through a kernel without a ledger refuses, so this wiring is what makes the macOS
// unit usable once its kind is selected (#1582 S4).
func TestDoomsdayWiringKernelCarriesTheWitnessLedger(t *testing.T) {
	if doomsdayKernel() == nil {
		t.Skip("no containment kernel on this platform")
	}
	cfg := &globalConfig{DBPath: filepath.Join(t.TempDir(), "mono.db"), ProfileID: "p1"}
	w := doomsdayWiring(cfg, nil)
	if _, ok := w.Kernel.(stop.LeaderLedger); !ok {
		t.Fatalf("the wired kernel (%T) carries no leader ledger", w.Kernel)
	}
	t.Cleanup(func() { hostGateMu.Lock(); delete(hostGates, cfg.ProfileID); hostGateMu.Unlock() })
	g := processHostGate(cfg, nil)
	if _, ok := g.Kernel.(stop.LeaderLedger); !ok {
		t.Fatalf("the host gate's kernel (%T) carries no leader ledger", g.Kernel)
	}
	if g.UnitEnded == nil {
		t.Fatal("the host gate cannot tell when a unit has ended, so it would never let go of its held leaders")
	}
}
