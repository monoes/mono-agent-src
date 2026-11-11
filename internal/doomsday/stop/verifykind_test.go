package stop

import (
	"errors"
	"testing"
)

// ----- what a macOS run needs from the library once its unit kind becomes the session unit (#1582 S4), proven before the flip -----
//
// The run's unit on macOS is to become KindSession (platformKinds["darwin"]); a finish-verification unit keeps the process-group
// unit there (owner decision D3: its creator is its placer, and a criterion is not a turn leader, so neither cause that stops a group
// holding a turn applies). These tests flip the macOS entry in the test only, which is exactly the one-line S4 change, and pin that
// everything else follows from the two entries.

func flipDarwinToSession(t *testing.T) {
	t.Helper()
	old := platformKinds["darwin"]
	platformKinds["darwin"] = KindSession
	t.Cleanup(func() { platformKinds["darwin"] = old })
	hostAs(t, "darwin")
}

func TestVerifyKindIsThePlatformsKindExceptOnMacOS(t *testing.T) {
	for goos, want := range map[string]string{"linux": KindCgroup2, "windows": KindJobObject, "darwin": KindPGroup} {
		if got := verifyKindOf(goos); got != want {
			t.Errorf("%s: verification unit kind %q, want %q", goos, got, want)
		}
	}
	flipDarwinToSession(t)
	if verifyKindOf("darwin") != KindPGroup || hostKind() != KindSession {
		t.Fatalf("after the flip: verify %q, run %q", verifyKindOf("darwin"), hostKind())
	}
	if _, ok := verifyKinds["linux"]; ok {
		t.Fatal("Linux verification units are no longer the platform's unit")
	}
}

// Both kinds are proven on a flipped macOS host; the kinds of other platforms still are not.
func TestProveUnitAcceptsTheRunsKindAndTheVerifyKindOnMacOS(t *testing.T) {
	r := &recContainment{}
	swapContainment(t, KindPGroup, r)
	swapContainment(t, KindSession, r)
	flipDarwinToSession(t)
	for _, kind := range []string{KindSession, KindPGroup} {
		rec := testRecord()
		rec.Kind = kind
		if p, err := ProveUnit(newFakeKernel(), rec); err != nil || p.Note != "seam" {
			t.Errorf("%s record on a flipped macOS host: %+v %v", kind, p, err)
		}
	}
	for _, kind := range []string{KindCgroup2, KindJobObject} {
		rec := testRecord()
		rec.Kind = kind
		swapContainment(t, kind, r)
		if _, err := ProveUnit(newFakeKernel(), rec); !errors.Is(err, ErrNotProven) {
			t.Errorf("a %s record was proven on macOS: %v", kind, err)
		}
	}
}

// A run's turns are placed only into the run's unit kind: a process-group record is refused by NewPlacer on a flipped host (a turn
// leader could never join it), and a verification placement accepts only the verification kind.
func TestPlacersAcceptOnlyTheirOwnKind(t *testing.T) {
	r := &recContainment{}
	swapContainment(t, KindPGroup, r)
	swapContainment(t, KindSession, r)
	flipDarwinToSession(t)
	dir := newRunDir(t, nil)
	for kind, c := range map[string]struct{ run, verify bool }{KindSession: {true, false}, KindPGroup: {false, true}} {
		rec := testRecord()
		rec.Kind = kind
		mustNoErr(t, dir.WriteUnitFile(rec))
		_, errRun := NewPlacer(newFakeKernel(), dir, rec.Generation)
		_, errVerify := NewVerifyPlacer(newFakeKernel(), dir, rec.Generation)
		if (errRun == nil) != c.run || (errVerify == nil) != c.verify {
			t.Errorf("%s: NewPlacer err=%v (want ok=%v), NewVerifyPlacer err=%v (want ok=%v)", kind, errRun, c.run, errVerify, c.verify)
		}
		if errRun != nil && !errors.Is(errRun, ErrNotPlaced) || errVerify != nil && !errors.Is(errVerify, ErrNotPlaced) {
			t.Errorf("%s: a refusal is not ErrNotPlaced: %v %v", kind, errRun, errVerify)
		}
		_ = dir.fsys().Remove(dir.UnitPath(rec.Generation))
	}
}

// CreateVerifyUnit makes the verification kind, CreateUnit the run's.
func TestCreateVerifyUnitUsesTheVerifyKindAndCreateUnitTheRunsKind(t *testing.T) {
	pg, ss := &recContainment{}, &recContainment{}
	swapContainment(t, KindPGroup, pg)
	swapContainment(t, KindSession, ss)
	flipDarwinToSession(t)
	if _, err := CreateVerifyUnit(bg, newFakeKernel(), CreateArgs{}, 7); err != nil || pg.created != 1 || ss.created != 0 {
		t.Fatalf("verify: err=%v pgroup=%d session=%d", err, pg.created, ss.created)
	}
	if _, err := CreateUnit(bg, newFakeKernel(), CreateArgs{}, 7); err != nil || ss.created != 1 || pg.created != 1 {
		t.Fatalf("run: err=%v pgroup=%d session=%d", err, pg.created, ss.created)
	}
	hostAs(t, "plan9")
	if _, err := CreateVerifyUnit(bg, newFakeKernel(), CreateArgs{}, 7); !errors.Is(err, ErrUnitNotCreated) {
		t.Fatalf("a platform with no containment made a verification unit: %v", err)
	}
}

// ----- Discard of a session unit -----

// A session unit has no service and no process before its first turn, so discarding a run that was never entered only removes its
// record; it needs no service manager. It refuses a record that is not the one on disk, and a unit that already holds a leader.
func TestDiscardOfASessionUnitRemovesItsRecordWithoutAServiceManager(t *testing.T) {
	dir := newRunDir(t, nil)
	rec := UnitRecord{Kind: KindSession, Name: "monoagent-run-7-" + testNonce + ".service", Nonce: testNonce, Generation: 7, CgroupID: 4242, BootID: "b"}
	mustNoErr(t, dir.WriteUnitFile(rec))
	c := UnitCreator{Dir: dir} // no Host: macOS has none
	other := rec
	other.CgroupID = 1
	if err := c.Discard(bg, other); err == nil {
		t.Fatal("a record that is not the one on disk was discarded")
	}
	if _, ok, _ := dir.ReadUnitFile(7); !ok {
		t.Fatal("the unit record was removed by a refused discard")
	}
	sessHolds.mu.Lock()
	sessHolds.by[rec.CgroupID] = map[int]heldLeader{500: {stamp: "1.000001", release: func() {}}}
	sessHolds.mu.Unlock()
	if err := c.Discard(bg, rec); err == nil {
		t.Fatal("a unit that holds a leader was discarded")
	}
	ReleaseUnitHolds(rec.CgroupID)
	if err := c.Discard(bg, rec); err != nil {
		t.Fatalf("discard: %v", err)
	}
	if _, ok, _ := dir.ReadUnitFile(7); ok {
		t.Fatal("the unit record is still there, so the generation's next attempt could not create its own")
	}
}

// Every other kind still needs its service manager.
func TestDiscardOfAServiceUnitStillNeedsAServiceManager(t *testing.T) {
	dir := newRunDir(t, nil)
	rec := testRecord()
	mustNoErr(t, dir.WriteUnitFile(rec))
	if err := (UnitCreator{Dir: dir}).Discard(bg, rec); err == nil {
		t.Fatal("a cgroup unit was discarded without a service manager")
	}
	if _, ok, _ := dir.ReadUnitFile(7); !ok {
		t.Fatal("its record was removed")
	}
}
