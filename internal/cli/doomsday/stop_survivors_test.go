package doomsday

import (
	"context"
	"strings"
	"testing"

	"github.com/monoes/mono-agent/internal/doomsday/stop"
)

// A member that was ended but whose identity could not be read is reported by the report lines, not as a surviving process;
// a real survivor is still listed (the command then prints "NOT stopped: N process(es) survive").
func TestOnlyRealSurvivorsAreListedAsSurvivingProcesses(t *testing.T) {
	rep := stop.Report{Unit: stop.UnitOutcome{
		Survivors: []stop.Survivor{{PID: 9, Comm: "tool"}},
		Unknown:   []stop.Survivor{{PID: 5, Comm: "gone", Why: "identity unreadable"}},
	}}
	got := unitSurvivors(rep)
	if len(got) != 1 || got[0] != "pid 9 (tool)" {
		t.Fatalf("%v", got)
	}
}

// The call site: a member that was ended but whose identity could not be read keeps the run cleanup-pending (it is in
// Incomplete) and is NOT named as a surviving process. Mutant tried: Wiring.stop adding Unit.Unknown to Survivors again.
func TestStopDoesNotNameAnEndedUnknownMemberAsASurvivor(t *testing.T) {
	r := newStopRig(t)
	r.world.mu.Lock()
	r.world.member, r.world.noIdent = true, true
	r.world.mu.Unlock()
	o, err := r.w.stop(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(o.Survivors) != 0 || len(o.Runs) != 0 || !strings.Contains(strings.Join(o.Incomplete, "\n"), "unknown process") {
		t.Fatalf("outcome: %+v", o)
	}
}
