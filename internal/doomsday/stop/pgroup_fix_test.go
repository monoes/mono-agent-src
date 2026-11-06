package stop

import (
	"errors"
	"os/exec"
	"strings"
	"syscall"
	"testing"
	"time"
)

// ----- the anchor is tied to THIS run (SEC P2-1) -----

func TestPGroupAnchorCarriesTheRunsNonceAndTheProofReadsItBack(t *testing.T) {
	w := newFakePG()
	rec, _, _ := createPG(t, w, 7)
	if got := w.procs[rec.AnchorPID].argv0; got != rec.Name {
		t.Fatalf("the anchor was started under %q, not under the unit's name %q", got, rec.Name)
	}
	// a same-uid group leader that is not this run's anchor, named by a forged record
	w.procs[900] = &fakePGProc{stamp: "login-shell", comm: "zsh", argv0: "-zsh", pgid: 900, sid: 9}
	forged := rec
	forged.AnchorPID, forged.AnchorStamp = 900, "login-shell"
	forged.Handle = `{"pgid":900,"sid":9,"creator_pid":1,"creator_stamp":"s-self"}`
	if _, err := proveGroup(w, forged); !errors.Is(err, ErrNotProven) {
		t.Fatalf("a record aimed at any process group was proven: %v", err)
	}
	// the right process under another run's name
	w.procs[rec.AnchorPID].argv0 = "monoagent-run-7-" + strings.Repeat("0", 32) + ".service"
	if _, err := proveGroup(w, rec); !errors.Is(err, ErrNotProven) {
		t.Fatalf("an anchor of another run was proven: %v", err)
	}
	// unreadable arguments prove nothing
	w.procs[rec.AnchorPID].argv0 = rec.Name
	w.errs["Argv0"] = errors.New("denied")
	if _, err := proveGroup(w, rec); !errors.Is(err, ErrNotProven) {
		t.Fatalf("unreadable anchor arguments were taken as a match: %v", err)
	}
}

func TestPGroupSelfTestAnchorIsNamedToo(t *testing.T) {
	w := newFakePG()
	if err := pgSelfTest(w); err != nil {
		t.Fatal(err)
	}
}

func TestParseProcArgs2ReadsArgv0(t *testing.T) {
	// argc, the exec path, padding NULs, then argv
	b := append([]byte{2, 0, 0, 0}, []byte("/bin/sleep\x00\x00\x00monoagent-run-7-ab.service\x002147483647\x00HOME=/x\x00")...)
	if got, err := parseProcArgs2(b); err != nil || got != "monoagent-run-7-ab.service" {
		t.Fatalf("%q %v", got, err)
	}
	for _, bad := range [][]byte{nil, {1, 0, 0}, {0, 0, 0, 0, 'a', 0}, {2, 0, 0, 0, '/', 'b'}, {2, 0, 0, 0, '/', 'b', 0, 0}} {
		if got, err := parseProcArgs2(bad); err == nil {
			t.Errorf("%v parsed as %q", bad, got)
		}
	}
}

// ----- End acts only on a record the witness bound (SEC P2-1) -----

func endWith(t *testing.T, w *fakePG, dir RunDir, gen uint64, witness func(uint64) []UnitRecord) UnitOutcome {
	t.Helper()
	usePG(t, w)
	u := UnitEnder{K: newFakeKernel(), Dir: dir, Witness: witness, Cfg: EndConfig{Clock: newStepClock(), Known: func(string, int, string) bool { return true }}}
	return u.End(bg, gen, nil)
}

func TestPGroupEndDoesNotActOnARecordOnlyTheRunDirectoryHolds(t *testing.T) {
	w := newFakePG()
	rec, dir, _ := createPG(t, w, 7)
	w.add(rec, 500, "git")
	for name, witness := range map[string]func(uint64) []UnitRecord{
		"no witness wired":      nil,
		"witness holds nothing": func(uint64) []UnitRecord { return nil },
	} {
		out := endWith(t, w, dir, 7, witness)
		if out.Kind != UnitNotProven || len(w.groupSig) != 0 || len(w.pidSig) != 0 || len(w.procs) != 3 {
			t.Fatalf("%s: a live unit named only by the key-free file was signalled: %+v %v %v", name, out, w.groupSig, w.pidSig)
		}
		if !strings.Contains(out.Note, "witness") {
			t.Errorf("%s: the refusal does not say why: %q", name, out.Note)
		}
	}
	// the authenticated record ends it
	if out := endWith(t, w, dir, 7, func(uint64) []UnitRecord { return []UnitRecord{rec} }); out.Kind != UnitBestEffort {
		t.Fatalf("%+v", out)
	}
}

func TestPGroupForgedFileRecordNextToTheRealOneSignalsNothingOfItsOwn(t *testing.T) {
	w := newFakePG()
	rec, dir, _ := createPG(t, w, 7)
	// another process that carries a nonce-bearing name of its own and leads a group, named by the file
	w.procs[900] = &fakePGProc{stamp: "forger", comm: "sleep", argv0: rec.Name, pgid: 900, sid: 1, ppid: 1}
	forged := rec
	forged.AnchorPID, forged.AnchorStamp = 900, "forger"
	forged.Handle = `{"pgid":900,"sid":1,"creator_pid":1,"creator_stamp":"s-self"}`
	mustNoErr(t, dir.fsys().Remove(dir.UnitPath(7)))
	mustNoErr(t, dir.WriteUnitFile(forged))
	out := endWith(t, w, dir, 7, func(uint64) []UnitRecord { return []UnitRecord{rec} })
	if _, ok := w.procs[900]; !ok {
		t.Fatalf("the process the forged file named was killed: %+v", out)
	}
	if out.Kind != UnitNotProven {
		t.Fatalf("a disagreement between the file and the witness must not read as ended: %+v", out)
	}
}

func TestPGroupAFileRecordOfAnEndedUnitIsHarmlessOnTheNextStop(t *testing.T) {
	w := newFakePG()
	rec, dir, _ := createPG(t, w, 7)
	if out := endPG(t, w, rec, dir, nil); out.Kind != UnitBestEffort {
		t.Fatalf("%+v", out)
	}
	// the witness no longer lists an ended unit, the file still names it: nothing is left, nothing is acted on
	out := endWith(t, w, dir, 7, func(uint64) []UnitRecord { return nil })
	if out.Kind != UnitBestEffort {
		t.Fatalf("a second stop of an ended unit failed: %+v", out)
	}
}

// ----- a clock step is not a reboot (SEC P2-2) -----

func TestPGroupBootTimeMismatchDoesNotShortCircuitToGone(t *testing.T) {
	w := newFakePG()
	rec, dir, _ := createPG(t, w, 7)
	w.add(rec, 500, "git")
	rec.BootID = "boot-before-the-clock-step"
	p, err := proveGroup(w, rec)
	if err != nil || p.Gone || p.Cg == nil {
		t.Fatalf("a live unit was read as gone because kern.boottime moved: %+v %v", p, err)
	}
	p.Cg.Close()
	out := endPG(t, w, rec, dir, nil)
	if out.Kind != UnitBestEffort || len(w.procs) != 1 {
		t.Fatalf("the workers of a unit whose boot time moved were left running: %+v %d", out, len(w.procs))
	}
	// the anchor is alive but the group number belongs to someone else after a real reboot: not proven, not gone
	w2 := newFakePG()
	rec2, _, _ := createPG(t, w2, 7)
	w2.procs[rec2.AnchorPID].stamp = "after-reboot"
	w2.add(rec2, 500, "unrelated")
	rec2.BootID = "older-boot"
	if _, err := proveGroup(w2, rec2); !errors.Is(err, ErrNotProven) {
		t.Fatalf("%v", err)
	}
}

// ----- a record of another platform's kind is not proven on this one (SEC P2-3) -----

func TestPGroupAndCgroupRecordsDoNotCrossPlatforms(t *testing.T) {
	w := newFakePG()
	rec, dir, _ := createPG(t, w, 7)
	usePG(t, w) // a darwin host
	if _, err := ProveUnit(newFakeKernel(), testRecord()); !errors.Is(err, ErrNotProven) {
		t.Fatalf("a cgroup2 record was proven on a darwin host: %v", err)
	}
	if _, err := ProveUnit(newFakeKernel(), rec); err != nil {
		t.Fatalf("a pgroup record on its own platform: %v", err)
	}
	cg := testRecord()
	mustNoErr(t, dir.fsys().Remove(dir.UnitPath(7)))
	cg.Generation = 7
	mustNoErr(t, dir.WriteUnitFile(cg))
	if out := endWith(t, w, dir, 7, nil); out.Kind != UnitNotProven {
		t.Fatalf("a cgroup2 record on a darwin host did not fail closed: %+v", out)
	}
	if _, err := NewPlacer(newFakeKernel(), dir, 7); !errors.Is(err, ErrNotPlaced) {
		t.Fatalf("a cgroup2 record was placed into on a darwin host: %v", err)
	}
	// and the other way round: a pgroup record on a Linux host
	hostAs(t, "linux")
	if _, err := ProveUnit(newFakeKernel(), rec); !errors.Is(err, ErrNotProven) {
		t.Fatalf("a pgroup record was proven on a linux host: %v", err)
	}
	mustNoErr(t, dir.fsys().Remove(dir.UnitPath(7)))
	mustNoErr(t, dir.WriteUnitFile(rec))
	if _, err := NewPlacer(newFakeKernel(), dir, 7); !errors.Is(err, ErrNotPlaced) {
		t.Fatalf("a pgroup record was placed into on a linux host: %v", err)
	}
}

// ----- an anchor whose start time cannot be read is ended (SEC P2-5, ROB P2) -----

func TestPGroupAnchorWithAnUnreadableStartTimeIsEnded(t *testing.T) {
	w := newFakePG()
	w.stampFail = true
	if _, err := createPGroup(bg, w, CreateArgs{Dir: newRunDir(t, nil)}, 7); !errors.Is(err, ErrUnitNotCreated) {
		t.Fatalf("%v", err)
	}
	if len(w.procs) != 1 {
		t.Fatalf("create left the anchor it started running: %d processes", len(w.procs))
	}
	w = newFakePG()
	w.stampFail = true
	if err := pgSelfTest(w); err == nil || len(w.procs) != 1 {
		t.Fatalf("the self-test left the anchor running: %v %d", err, len(w.procs))
	}
}

// ----- a kill is asynchronous on macOS (ROB P2) -----

func countingPause(t *testing.T) *int {
	t.Helper()
	n := 0
	old := pgPause
	pgPause = func(time.Duration) { n++ }
	t.Cleanup(func() { pgPause = old })
	return &n
}

func TestPGroupEndWaitsForAKilledAnchorToLeave(t *testing.T) {
	pauses := countingPause(t)
	w := newFakePG()
	w.killDelay, w.zombies = 3, true
	rec, dir, _ := createPG(t, w, 7) // only the anchor: its end is the last thing Rmdir does
	out := endPG(t, w, rec, dir, nil)
	if out.Kind != UnitBestEffort || len(out.Survivors) != 0 {
		t.Fatalf("a clean stop read as %+v because the kill takes a moment", out)
	}
	if *pauses == 0 {
		t.Fatal("nothing waited for the kill to land")
	}
	// the anchor lingers as a zombie: it is not a member and not a unit any more
	if present, err := (pgContainment{os: w}).Present(rec); err != nil || present {
		t.Fatalf("a zombie anchor reads as a unit: %v %v", present, err)
	}
	// a process that never leaves is a straggler, and the wait is bounded
	pauses2 := countingPause(t)
	w2 := newFakePG()
	w2.killDelay = 1 << 30
	rec2, dir2, _ := createPG(t, w2, 7)
	if out := endPG(t, w2, rec2, dir2, nil); out.Kind != UnitStragglers {
		t.Fatalf("a process that did not leave read as %+v", out)
	}
	if *pauses2 > 1000 {
		t.Fatalf("the wait is not bounded: %d pauses", *pauses2)
	}
}

// ----- failures are not hidden (ROB P2) -----

func TestPGroupCreateSaysWhenTheAnchorCannotBeEndedEither(t *testing.T) {
	w := newFakePG()
	w.failAt = "group"
	w.errs["KillAnchor"] = errors.New("EPERM")
	_, err := createPGroup(bg, w, CreateArgs{Dir: newRunDir(t, nil)}, 7)
	if !errors.Is(err, ErrUnitNotCreated) || !strings.Contains(err.Error(), "could not be ended either") {
		t.Fatalf("%v", err)
	}
	for _, op := range []string{"BootID", "Self", "SelfGroup"} {
		w := newFakePG()
		w.errs[op] = errors.New("boom")
		if _, err := createPGroup(bg, w, CreateArgs{Dir: newRunDir(t, nil)}, 7); !errors.Is(err, ErrUnitNotCreated) {
			t.Errorf("%s: %v", op, err)
		}
		if len(w.procs) != 1 {
			t.Errorf("%s: a process was left behind: %d", op, len(w.procs))
		}
	}
}

func TestPGroupProveFailsClosedOnEveryUnreadableFact(t *testing.T) {
	for _, op := range []string{"Self", "SelfGroup", "Members", "PgidOf"} {
		w := newFakePG()
		rec, _, _ := createPG(t, w, 7)
		w.errs[op] = errors.New("boom")
		if p, err := proveGroup(w, rec); !errors.Is(err, ErrNotProven) || p.Cg != nil {
			t.Errorf("%s: %+v %v", op, p, err)
		}
	}
	// the boot id is no evidence either way, so an unreadable one only matters at create
	w := newFakePG()
	rec, _, _ := createPG(t, w, 7)
	w.errs["BootID"] = errors.New("boom")
	if p, err := proveGroup(w, rec); err != nil || p.Gone {
		t.Fatalf("an unreadable boot id changed the proof: %+v %v", p, err)
	}
}

func TestPGroupSignalErrorsAreNotAnEnd(t *testing.T) {
	// one failing path is covered by the other (the group kill and the per-member sweep): whatever the outcome says
	// must be true of the member
	for _, ops := range [][]string{{"SignalGroup"}, {"SignalPid"}, {"SignalGroup", "SignalPid"}} {
		w := newFakePG()
		rec, dir, _ := createPG(t, w, 7)
		w.add(rec, 500, "git")
		for _, op := range ops {
			w.errs[op] = errors.New("EPERM")
		}
		out := endPG(t, w, rec, dir, nil)
		_, alive := w.procs[500]
		if alive && (out.Kind == UnitBestEffort || out.Kind == UnitEnded) {
			t.Errorf("%v: a member that could not be signalled read as %+v", ops, out)
		}
		if len(ops) == 2 && (!alive || out.Kind != UnitStragglers) { // the anchor cannot be ended either
			t.Errorf("%v: nothing could be signalled: %+v alive=%v", ops, out, alive)
		}
	}
}

func TestPGroupPlaceReportsAPlacementThatCannotBeMade(t *testing.T) {
	w := newFakePG()
	rec, _, _ := createPG(t, w, 7)
	pl, err := pgContainment{os: w}.Place(rec)
	mustNoErr(t, err)
	w.errs["Prepare"] = syscall.EPERM // setpgid across sessions or into a vanished group
	release, err := pl.Place(exec.Command("git"))
	if !errors.Is(err, ErrNotPlaced) || release != nil {
		t.Fatalf("%v %v", release != nil, err)
	}
	if len(w.prepared) != 0 {
		t.Fatal("a command was prepared")
	}
}

// ----- a begun operation keeps the run pending (ROB P2) -----

func TestPGroupFlatGroupWithABegunOperationKeepsTheRunPending(t *testing.T) {
	w := newFakePG()
	rec, dir, _ := createPG(t, w, 7)
	w.add(rec, 500, "git")
	usePG(t, w)
	s, _, _, _ := newStopper(t, nil)
	s.Dir = dir
	mustNoErr(t, dir.CreateSecret(7))
	s.Host = pendingHost{}
	s.Ender = UnitEnder{K: newFakeKernel(), Dir: dir, Witness: func(uint64) []UnitRecord { return []UnitRecord{rec} },
		Cfg: EndConfig{Clock: newStepClock(), Known: func(string, int, string) bool { return true }}}
	rep := s.Stop(bg, 7, "ch1", "operator")
	if rep.Final != StateCleanupPending || rep.Stopped() || rep.StoppedBestEffort() || rep.ExitCode() == 0 {
		t.Fatalf("a begun operation in an unsplittable group ended the run: %s", joined(rep))
	}
	if len(w.groupSig) != 0 || len(w.pidSig) != 0 {
		t.Fatal("the group was killed around the begun operation")
	}
}
