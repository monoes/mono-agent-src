package stop

import (
	"errors"
	"os"
	"os/exec"
	"strings"
	"testing"
)

// A turn is placed without the anchor as its parent (#1723): it is created suspended, and commit puts it in the run's
// job (the one the identity proof named) and only then resumes it. The anchor is never named as its parent.
func TestJobPlaceCommitPutsTheSuspendedProcessInTheJob(t *testing.T) {
	w := newFakeWin()
	rec, _, _ := createWin(t, w, 7)
	pl, _ := jobContainment{os: w}.Place(rec)
	cmd := exec.Command("monoagentcli")
	commit, err := pl.PlaceCommit(cmd)
	if err != nil {
		t.Fatal(err)
	}
	if !w.suspended[cmd] || len(w.parentFor) != 0 {
		t.Fatalf("the command was not created suspended, or was put under the anchor: %v %v", w.suspended, w.parentFor)
	}
	cmd.Process = &os.Process{} // started
	if err := commit(); err != nil {
		t.Fatal(err)
	}
	if got := w.assigned[cmd]; got != jobName(rec.Name) {
		t.Fatalf("the process was put in %q, not the run's job %q", got, jobName(rec.Name))
	}
	if err := commit(); err != nil || len(w.assigned) != 1 {
		t.Fatalf("a second commit did something: %v %v", err, w.assigned)
	}
}

// A process the job refuses was ended before it ran, and commit fails with ErrNotPlaced, so the start is a failure.
func TestJobPlaceCommitFailsClosedWhenTheJobRefusesTheProcess(t *testing.T) {
	w := newFakeWin()
	rec, _, _ := createWin(t, w, 7)
	pl, _ := jobContainment{os: w}.Place(rec)
	cmd := exec.Command("monoagentcli")
	commit, err := pl.PlaceCommit(cmd)
	if err != nil {
		t.Fatal(err)
	}
	cmd.Process = &os.Process{}
	w.assignErr = errors.New("Access is denied.")
	if err := commit(); !errors.Is(err, ErrNotPlaced) || !strings.Contains(err.Error(), "Access is denied") {
		t.Fatalf("a refused assignment: %v", err)
	}
	if len(w.ended) != 1 || len(w.assigned) != 0 {
		t.Fatalf("the refused process was not ended: ended %d assigned %v", len(w.ended), w.assigned)
	}
}

// A placement that panics does not unwind the caller, and it fails closed: the process is ended and commit is ErrNotPlaced.
func TestJobPlaceCommitThatPanicsEndsTheProcess(t *testing.T) {
	w := newFakeWin()
	rec, _, _ := createWin(t, w, 7)
	pl, _ := jobContainment{os: w}.Place(rec)
	cmd := exec.Command("sleep", "30") // a real process: the recovery ends it
	commit, err := pl.PlaceCommit(cmd)
	if err != nil {
		t.Fatal(err)
	}
	if err := cmd.Start(); err != nil {
		t.Skipf("no sleep here: %v", err)
	}
	w.assignPanic = true
	if err := commit(); !errors.Is(err, ErrNotPlaced) || len(w.assigned) != 0 {
		t.Fatalf("a panicking placement: %v %v", err, w.assigned)
	}
	if err := cmd.Wait(); err == nil {
		t.Fatal("the process of a panicking placement was not ended")
	}
}

// A command that never started has nothing to place: commit does nothing and does not fail.
func TestJobPlaceCommitOfACommandThatDidNotStart(t *testing.T) {
	w := newFakeWin()
	rec, _, _ := createWin(t, w, 7)
	pl, _ := jobContainment{os: w}.Place(rec)
	commit, err := pl.PlaceCommit(exec.Command("monoagentcli"))
	if err != nil {
		t.Fatal(err)
	}
	if err := commit(); err != nil || len(w.assigned)+len(w.ended) != 0 {
		t.Fatalf("%v %v %v", err, w.assigned, w.ended)
	}
}

// Nothing is prepared for a nil or started command, a unit that cannot be proven, or a command that cannot be suspended.
func TestJobPlaceCommitRefusals(t *testing.T) {
	w := newFakeWin()
	rec, _, _ := createWin(t, w, 7)
	pl, _ := jobContainment{os: w}.Place(rec)
	started := exec.Command("git")
	started.Process = &os.Process{}
	for name, cmd := range map[string]*exec.Cmd{"nil": nil, "started": started} {
		if _, err := pl.PlaceCommit(cmd); !errors.Is(err, ErrNotPlaced) {
			t.Errorf("%s: %v", name, err)
		}
	}
	w.suspendErr = errors.New("another parent")
	if _, err := pl.PlaceCommit(exec.Command("git")); !errors.Is(err, ErrNotPlaced) {
		t.Errorf("unsuspendable: %v", err)
	}
	w.suspendErr = nil
	w.procs[rec.AnchorPID].stamp = "reused" // the anchor is not the recorded process any more
	if _, err := pl.PlaceCommit(exec.Command("git")); !errors.Is(err, ErrNotPlaced) {
		t.Errorf("unproven unit: %v", err)
	}
	if len(w.suspended) != 0 {
		t.Fatalf("a refused command was prepared: %v", w.suspended)
	}
}

// The probe fails when a turn cannot be put in the job the way PlaceCommit puts it, or is not a member afterwards.
func TestJobSelfTestChecksTheTurnPath(t *testing.T) {
	w := newFakeWin()
	w.assignErr = errors.New("Access is denied.")
	if err := jobSelfTest(w); err == nil || !strings.Contains(err.Error(), "a turn cannot be started suspended") {
		t.Fatalf("a refused turn passed the probe: %v", err)
	}
	if len(w.procs) != 1 {
		t.Fatalf("the self-test left processes behind: %d", len(w.procs))
	}
}
