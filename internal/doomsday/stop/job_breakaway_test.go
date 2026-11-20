package stop

import (
	"errors"
	"os"
	"os/exec"
	"strings"
	"testing"
)

// The turn is created out of the caller's job when that is allowed, so a run's job in another job chain can take it;
// when the caller's job forbids it the turn stays in that job, and a refusal then names the cause and the next step.
func TestJobPlaceCommitLeavesTheCallersJobWhenAllowed(t *testing.T) {
	for _, forbid := range []bool{false, true} {
		w := newFakeWin()
		w.noBreakaway = forbid
		rec, _, _ := createWin(t, w, 7)
		pl, _ := jobContainment{os: w}.Place(rec)
		cmd := exec.Command("monoagentcli")
		commit, err := pl.PlaceCommit(cmd)
		if err != nil {
			t.Fatal(err)
		}
		if w.brokeAway[cmd] == forbid {
			t.Fatalf("forbid=%v: created out of the caller's job = %v", forbid, w.brokeAway[cmd])
		}
		cmd.Process = &os.Process{}
		w.assignErr = errors.New("Access is denied.")
		err = commit()
		if !errors.Is(err, ErrNotPlaced) || strings.Contains(err.Error(), "same environment as the coder host") != forbid {
			t.Fatalf("forbid=%v: a refused turn: %v", forbid, err)
		}
	}
}

// A caller that cannot tell whether breakaway is allowed places nothing.
func TestJobPlaceCommitRefusesWhenBreakawayCannotBeTold(t *testing.T) {
	w := newFakeWin()
	rec, _, _ := createWin(t, w, 7)
	pl, _ := jobContainment{os: w}.Place(rec)
	w.breakawayErr = errors.New("no cmd.exe")
	if _, err := pl.PlaceCommit(exec.Command("monoagentcli")); !errors.Is(err, ErrNotPlaced) || len(w.suspended) != 0 {
		t.Fatalf("%v %v", err, w.suspended)
	}
}
