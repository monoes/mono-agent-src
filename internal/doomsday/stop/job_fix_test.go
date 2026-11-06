package stop

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"strings"
	"testing"
	"time"
)

func init() { jobPoll = 0 }

// ----- P2-1: a name that is not visible is not a job that is gone -----

func TestJobNotVisibleWhileTheAnchorLivesIsNotGone(t *testing.T) {
	w := newFakeWin()
	rec, _, _ := createWin(t, w, 7)
	w.session = 3 // this Stop runs in another logon session: the session-local name is not found
	delete(w.jobs, jobName(rec.Name))
	if p, err := proveJob(w, rec); !errors.Is(err, ErrNotProven) || p.Gone {
		t.Fatalf("a Stop from another session called a live unit gone: %+v %v", p, err)
	}
	// the anchor is really gone: nothing holds the job any more, so it is gone
	w.KillAnchor(rec.AnchorPID, rec.AnchorStamp)
	if p, err := proveJob(w, rec); err != nil || !p.Gone || !p.BestEffort {
		t.Fatalf("%+v %v", p, err)
	}
}

func TestJobHandleRecordsTheCreatorsSession(t *testing.T) {
	w := newFakeWin()
	w.session = 5
	rec, _, _ := createWin(t, w, 7)
	var h jobHandle
	if err := jsonUnmarshal(rec.Handle, &h); err != nil || h.Session != 5 {
		t.Fatalf("%+v %v", h, err)
	}
}

func TestJobAnchorDeathKillsTheMembersAndTheNameGoes(t *testing.T) {
	w := newFakeWin()
	rec, _, _ := createWin(t, w, 7)
	w.procs[500] = &fakeWinProc{stamp: "c", comm: "git.exe", job: jobName(rec.Name)}
	w.KillAnchor(rec.AnchorPID, rec.AnchorStamp)
	if _, ok := w.procs[500]; ok {
		t.Fatal("KILL_ON_JOB_CLOSE is not modelled: a member outlived the anchor")
	}
	if p, err := proveJob(w, rec); err != nil || !p.Gone || !p.BestEffort {
		t.Fatalf("%+v %v", p, err)
	}
}

// ----- P2-2: a best-effort proof is not proof of empty -----

func TestProvenEmptyHonoursBestEffort(t *testing.T) {
	w := newFakeWin()
	rec, _, _ := createWin(t, w, 7)
	useWin(t, w)
	if empty, err := ProvenEmpty(newFakeKernel(), rec); err != nil || empty {
		t.Fatalf("a best-effort empty is not proof (a process may have left the group): %v %v", empty, err)
	}
	w.KillAnchor(rec.AnchorPID, rec.AnchorStamp)
	if empty, err := ProvenEmpty(newFakeKernel(), rec); err != nil || empty {
		t.Fatalf("a best-effort gone is not proof: %v %v", empty, err)
	}
}

func TestProvenEmptyOfALinuxUnitStillProves(t *testing.T) {
	k := newFakeKernel()
	k.absent = true
	if empty, err := ProvenEmpty(k, testRecord()); err != nil || !empty {
		t.Fatalf("%v %v", empty, err)
	}
}

func TestRunProbeAllNamesEveryFailingStep(t *testing.T) {
	steps := []ProbeStep{
		{"a", func() error { return nil }},
		{"b", func() error { return errors.New("no b") }},
		{"c", func() error { return errors.New("no c") }},
	}
	err := RunProbeAll(steps)
	if !errors.Is(err, ErrContainmentUnavailable) || !strings.Contains(err.Error(), "b: no b") || !strings.Contains(err.Error(), "c: no c") || strings.Contains(err.Error(), "a:") {
		t.Fatalf("%v", err)
	}
	if err := RunProbeAll(steps[:1]); err != nil {
		t.Fatal(err)
	}
}

// ----- UnitNone and honesty on a platform that cannot prove -----

func TestNoUnitOnWindowsIsNotABareStopped(t *testing.T) {
	r := Report{Generation: 1, Final: StateQuiescent, Unit: UnitOutcome{Kind: UnitNone}}
	if !r.Stopped() || r.StoppedBestEffort() {
		t.Fatal("Linux: a run that never had a unit is stopped")
	}
	hostAs(t, "windows")
	if r.Stopped() || !r.StoppedBestEffort() || !strings.Contains(strings.Join(r.Lines(), "\n"), "stopped (best effort") {
		t.Fatalf("Windows: no unit proves nothing: %v", r.Lines())
	}
}

// ----- the flat group with a begun operation keeps the run pending -----

type pendingHost struct{}

func (pendingHost) CloseHost(context.Context, uint64) HostOutcome {
	return HostOutcome{Kind: HostClosed, Pending: []uint64{3}}
}

func TestFlatGroupWithABegunOperationKeepsTheRunPending(t *testing.T) {
	w := newFakeWin()
	rec, dir, _ := createWin(t, w, 7)
	w.procs[500] = &fakeWinProc{stamp: "c", comm: "git.exe", job: jobName(rec.Name)}
	useWin(t, w)
	s, _, _, _ := newStopper(t, nil)
	s.Dir = dir
	mustNoErr(t, dir.CreateSecret(7))
	s.Host = pendingHost{}
	s.Ender = UnitEnder{K: newFakeKernel(), Dir: dir, Cfg: EndConfig{Clock: newStepClock(), Known: func(string, int, string) bool { return true }}}
	rep := s.Stop(bg, 7, "ch1", "operator")
	if rep.Final != StateCleanupPending || rep.Stopped() || rep.StoppedBestEffort() || rep.ExitCode() == 0 {
		t.Fatalf("a begun operation in an unsplittable unit ended the run: %s", joined(rep))
	}
	if w.terminate != 0 {
		t.Fatal("the unit was killed around the begun operation")
	}
}

// ----- failure paths -----

func TestJobCreateJournalAndAnchorKillFailures(t *testing.T) {
	for _, which := range []string{"intent", "bound"} {
		w := newFakeWin()
		l := &failJournal{fail: which}
		_, err := createJob(bg, w, CreateArgs{Dir: newRunDir(t, nil), Journal: l}, 7)
		if !errors.Is(err, ErrUnitNotCreated) {
			t.Fatalf("%s: %v", which, err)
		}
		wantKilled := 0
		if which == "bound" {
			wantKilled = 1 // the anchor existed by then
		}
		if len(w.killed) != wantKilled || (which == "intent" && len(w.procs) != 1) {
			t.Fatalf("%s: killed %v procs %d", which, w.killed, len(w.procs))
		}
	}
	w := newFakeWin()
	w.failAt = "adopt"
	w.killAnchorErr = errors.New("access denied")
	_, err := createJob(bg, w, CreateArgs{Dir: newRunDir(t, nil)}, 7)
	if !errors.Is(err, ErrUnitNotCreated) || !strings.Contains(err.Error(), "could not be ended either") || !strings.Contains(err.Error(), "still running") {
		t.Fatalf("an anchor that cannot be ended must be named as still running: %v", err)
	}
}

type failJournal struct{ fail string }

func (f *failJournal) Intent(uint64, string, string, string) error {
	if f.fail == "intent" {
		return errors.New("witness refused")
	}
	return nil
}
func (f *failJournal) Bound(UnitRecord) error {
	if f.fail == "bound" {
		return errors.New("witness refused")
	}
	return nil
}

func TestJobTerminateErrorsAreStragglersNotEnded(t *testing.T) {
	w := newFakeWin()
	rec, dir, _ := createWin(t, w, 7)
	w.procs[500] = &fakeWinProc{stamp: "c", comm: "git.exe", job: jobName(rec.Name)}
	w.terminateErr = errors.New("access denied")
	out := endWin(t, w, rec, dir, nil)
	if out.Kind != UnitStragglers || len(out.Survivors) == 0 {
		t.Fatalf("a job that could not be terminated: %+v", out)
	}
}

func TestJobTerminateMemberErrorSurfaces(t *testing.T) {
	w := newFakeWin()
	rec, _, _ := createWin(t, w, 7)
	w.procs[500] = &fakeWinProc{stamp: "c", comm: "git.exe", job: jobName(rec.Name)}
	p, err := proveJob(w, rec)
	if err != nil {
		t.Fatal(err)
	}
	w.memberErr = errors.New("denied")
	if err := p.Cg.Signal("", 500, 9); err == nil {
		t.Fatal("a failed member kill was swallowed")
	}
	w.memberErr = nil
	if err := p.Cg.Signal("", 500, 15); err != nil || len(w.members(jobName(rec.Name))) != 2 {
		t.Fatal("SIGTERM must not end anything on Windows")
	}
	if err := p.Cg.Signal("", 500, 9); err != nil || w.procs[500] != nil {
		t.Fatalf("SIGKILL did not end the member: %v", err)
	}
}

func TestJobPlaceRefusalsAfterAGoodProof(t *testing.T) {
	w := newFakeWin()
	rec, _, _ := createWin(t, w, 7)
	pl, _ := jobContainment{os: w}.Place(rec)
	w.parentErr = errors.New("OpenProcess denied")
	if _, err := pl.Place(exec.Command("git")); !errors.Is(err, ErrNotPlaced) {
		t.Fatalf("%v", err)
	}
	w.parentErr = nil
	started := exec.Command("git")
	started.Process = &os.Process{}
	if _, err := pl.Place(started); !errors.Is(err, ErrNotPlaced) {
		t.Fatalf("an already started command was placed: %v", err)
	}
	if _, err := pl.Place(nil); !errors.Is(err, ErrNotPlaced) {
		t.Fatalf("nil: %v", err)
	}
	if len(w.parentFor) != 0 {
		t.Fatal("a refused command was prepared")
	}
	cmd := exec.Command("git")
	if err := pl.PlaceLeased(cmd); err != nil || len(w.parentFor) != 1 {
		t.Fatalf("PlaceLeased: %v %v", err, w.parentFor)
	}
}

// ----- the absence race and the probe -----

func TestJobEndToleratesAJobNameThatLingersBriefly(t *testing.T) {
	w := newFakeWin()
	w.lingers = 3
	rec, dir, _ := createWin(t, w, 7)
	out := endWin(t, w, rec, dir, nil)
	if out.Kind != UnitBestEffort {
		t.Fatalf("a clean stop read as %+v because the name closes late", out)
	}
	w2 := newFakeWin()
	w2.lingers = 1000
	rec2, dir2, _ := createWin(t, w2, 7)
	if out := endWin(t, w2, rec2, dir2, nil); out.Kind != UnitStragglers {
		t.Fatalf("a job that stays is not ended: %+v", out)
	}
}

func TestJobSelfTestToleratesLateClosureAndChecksTheChildJoined(t *testing.T) {
	w := newFakeWin()
	w.lingers = 3
	if err := jobSelfTest(w); err != nil {
		t.Fatal(err)
	}
	w = newFakeWin()
	w.childOutside = true
	if err := jobSelfTest(w); err == nil || !strings.Contains(err.Error(), "not in the job") {
		t.Fatalf("a child created under the anchor that did not join the job passed the probe: %v", err)
	}
	if len(w.procs) != 1 {
		t.Fatalf("the self-test left processes behind: %d", len(w.procs))
	}
	w = newFakeWin()
	w.parentErr = errors.New("denied")
	if err := jobSelfTest(w); err == nil {
		t.Fatal("a child that cannot be created under the anchor passed the probe")
	}
}

// ----- identity numbers and labels -----

type fixedRand struct{ b []byte }

func (f *fixedRand) Read(p []byte) (int, error) { return copy(p, f.b), nil }

func TestJobIdentityUsesTheWholeNonce(t *testing.T) {
	mk := func(last byte) []byte {
		b := make([]byte, 16)
		b[15] = last // differs only in the last byte
		return b
	}
	var ids []uint64
	for _, last := range []byte{1, 2} {
		w := newFakeWin()
		rec, err := createJob(bg, w, CreateArgs{Dir: newRunDir(t, nil), Rand: io.Reader(&fixedRand{mk(last)})}, 7)
		if err != nil {
			t.Fatal(err)
		}
		ids = append(ids, rec.CgroupID)
	}
	if ids[0] == ids[1] || ids[0] == 0 {
		t.Fatalf("nonces that differ in one byte share an identity: %v", ids)
	}
}

func TestWitnessEndLabelIsTruthfulForABestEffortEnd(t *testing.T) {
	if got := endedHow(UnitOutcome{Kind: UnitBestEffort}); got == "rmdir" || got == "" {
		t.Fatalf("how=%q for a job end", got)
	}
	if got := endedHow(UnitOutcome{Kind: UnitEnded}); got != "rmdir" {
		t.Fatalf("Linux label changed: %q", got)
	}
}

var _ = fmt.Sprint
var _ = time.Second

func jsonUnmarshal(s string, v any) error { return json.Unmarshal([]byte(s), v) }
