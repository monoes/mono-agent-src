package health

import (
	"context"
	"strings"
	"testing"
)

const strandedReason = "the run's process group was made by a coder host that is not the running one"

// An open run whose process group a past host made cannot take a turn from this host on macOS: the Open-ended mode row and the
// turns row warn and give the next step, where they used to stay green.
func TestStrandedUnitWarnsOnBothRows(t *testing.T) {
	f := readyFacts()
	f.ActiveRun, f.StrandedUnit = true, strandedReason
	env := openModeEnv(f, "ok")

	om := checkOpenMode(context.Background(), env)
	row := rowOf(t, om, "unit")
	if row.Status != StatusWarn || !strings.Contains(row.Summary, strandedReason) ||
		!strings.Contains(row.Detail, "doomsday stop") || !strings.Contains(row.Detail, "a host restart ends open-ended runs on this platform") {
		t.Errorf("unit row: %+v", row)
	}
	if om.Status != StatusWarn {
		t.Errorf("parent stays green: %+v", om)
	}
	tr := checkTurnsCanStart(context.Background(), env)
	if tr.Status != StatusWarn || !strings.Contains(tr.Summary, strandedReason) || !strings.Contains(tr.Detail, "doomsday stop") {
		t.Errorf("turns row: %+v", tr)
	}

	f.StrandedUnit = ""
	env = openModeEnv(f, "ok")
	if r := rowOf(t, checkOpenMode(context.Background(), env), "unit"); r.Status != StatusOK {
		t.Errorf("no stranded unit, unit row: %+v", r)
	}
	if tr := checkTurnsCanStart(context.Background(), env); tr.Status != StatusOK {
		t.Errorf("no stranded unit, turns row: %+v", tr)
	}
}
