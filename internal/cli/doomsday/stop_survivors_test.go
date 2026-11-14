package doomsday

import (
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
