package stop

import (
	"encoding/json"
	"errors"
	"fmt"
	"os/exec"
	"strings"
	"syscall"
	"testing"
)

// ----- a fake macOS: processes with a start stamp and a process group -----

type fakePGProc struct {
	stamp, comm string
	argv0       string
	pgid, sid   int
	ppid        int
	zombie      bool
	immune      bool // survives SIGKILL (a process the kernel cannot end, for the survivor report)
	dying       int  // Members calls left before a killed process leaves (macOS ends a killed process asynchronously)
}

type fakePG struct {
	procs    map[int]*fakePGProc
	next     int
	self     int
	failAt   string // "anchor", "group"
	killed   []int
	groupSig []string // "pgid:signal"
	pidSig   []string
	prepared []int

	errs      map[string]error // an operation that fails: BootID Self SelfGroup KillAnchor PgidOf Members SignalGroup SignalPid Prepare Argv0
	killDelay int              // a killed process stays listed for this many Members calls
	zombies   bool             // a killed anchor lingers as a zombie (pid and group number stay reserved) instead of vanishing
	stampFail bool             // StartAnchor starts the anchor and then cannot read its start time
	members   int              // Members calls
}

func newFakePG() *fakePG {
	w := &fakePG{procs: map[int]*fakePGProc{}, next: 100, self: 1, errs: map[string]error{}}
	w.procs[1] = &fakePGProc{stamp: "s-self", comm: "monoagentcli", argv0: "monoagentcli", pgid: 1, sid: 1}
	return w
}

func (w *fakePG) BootID() (string, error) { return "boot-m", w.errs["BootID"] }
func (w *fakePG) Self() (int, string, error) {
	return w.self, w.procs[w.self].stamp, w.errs["Self"]
}
func (w *fakePG) SelfGroup() (int, int, error) {
	p := w.procs[w.self]
	return p.pgid, p.sid, w.errs["SelfGroup"]
}
func (w *fakePG) StartAnchor(argv0 string) (int, string, error) {
	if w.failAt == "anchor" {
		return 0, "", errors.New("no sleep")
	}
	w.next++
	pg := w.next
	if w.failAt == "group" {
		pg = 1 // the anchor did not become a group leader
	}
	w.procs[w.next] = &fakePGProc{stamp: fmt.Sprintf("s%d", w.next), comm: "sleep", argv0: argv0, pgid: pg, sid: w.procs[w.self].sid, ppid: w.self}
	if w.stampFail {
		return w.next, "", errors.New("the anchor's start time cannot be read")
	}
	return w.next, w.procs[w.next].stamp, nil
}

// KillAnchor ends the anchor with that start stamp; an empty stamp means "the child this process just started and has
// not reaped" (its pid cannot have been reused), which is what the real mechanism accepts for a start that failed.
func (w *fakePG) KillAnchor(pid int, stamp string) error {
	if err := w.errs["KillAnchor"]; err != nil {
		return err
	}
	if p, ok := w.procs[pid]; ok && (p.stamp == stamp || (stamp == "" && p.ppid == w.self && p.pgid == pid)) {
		w.killed = append(w.killed, pid)
		delete(w.procs, pid)
	}
	return nil
}
func (w *fakePG) ProcInfo(pid int) (string, string, error) {
	p := w.procs[pid]
	if p == nil || p.zombie {
		return "", "", errors.New("no such process")
	}
	return p.stamp, p.comm, nil
}
func (w *fakePG) Argv0(pid int) (string, error) {
	if err := w.errs["Argv0"]; err != nil {
		return "", err
	}
	p := w.procs[pid]
	if p == nil || p.zombie {
		return "", errors.New("no such process")
	}
	return p.argv0, nil
}
func (w *fakePG) PgidOf(pid int) (int, error) {
	if err := w.errs["PgidOf"]; err != nil {
		return 0, err
	}
	p := w.procs[pid]
	if p == nil || p.zombie {
		return 0, errors.New("no such process")
	}
	return p.pgid, nil
}
func (w *fakePG) Members(pgid int) ([]int, error) {
	w.members++
	if err := w.errs["Members"]; err != nil {
		return nil, err
	}
	for pid, p := range w.procs { // a killed process leaves a moment after the signal
		if p.dying > 0 {
			if p.dying--; p.dying == 0 {
				w.die(pid, p)
			}
		}
	}
	var out []int
	for pid, p := range w.procs {
		if p.pgid == pgid && !p.zombie {
			out = append(out, pid)
		}
	}
	return out, nil
}

// die is how a killed process leaves: gone, or (an anchor with zombies on) a zombie that still holds its pid.
func (w *fakePG) die(pid int, p *fakePGProc) {
	if w.zombies && p.comm == "sleep" {
		p.zombie = true
		return
	}
	delete(w.procs, pid)
}
func (w *fakePG) kill(pid int, p *fakePGProc) {
	switch {
	case p.immune:
	case w.killDelay > 0:
		if p.dying == 0 {
			p.dying = w.killDelay
		}
	default:
		w.die(pid, p)
	}
}
func (w *fakePG) SignalGroup(pgid int, sig syscall.Signal) error {
	if err := w.errs["SignalGroup"]; err != nil {
		return err
	}
	w.groupSig = append(w.groupSig, fmt.Sprintf("%d:%d", pgid, sig))
	if sig != syscall.SIGKILL {
		return nil
	}
	for pid, p := range w.procs {
		if p.pgid == pgid {
			w.kill(pid, p)
		}
	}
	return nil
}
func (w *fakePG) SignalPid(pid int, sig syscall.Signal) error {
	if err := w.errs["SignalPid"]; err != nil {
		return err
	}
	w.pidSig = append(w.pidSig, fmt.Sprintf("%d:%d", pid, sig))
	if p := w.procs[pid]; p != nil && sig == syscall.SIGKILL {
		w.kill(pid, p)
	}
	return nil
}
func (w *fakePG) Prepare(cmd *exec.Cmd, pgid int) error {
	if err := w.errs["Prepare"]; err != nil {
		return err
	}
	w.prepared = append(w.prepared, pgid)
	return nil
}

func (w *fakePG) add(rec UnitRecord, pid int, comm string) *fakePGProc {
	p := &fakePGProc{stamp: "m", comm: comm, argv0: comm, pgid: rec.AnchorPID, sid: 1}
	w.procs[pid] = p
	return p
}

func createPG(t *testing.T, w *fakePG, gen uint64) (UnitRecord, RunDir, *jlog) {
	t.Helper()
	dir := newRunDir(t, nil)
	l := &jlog{}
	rec, err := createPGroup(bg, w, CreateArgs{Dir: dir, Journal: l}, gen)
	if err != nil {
		t.Fatal(err)
	}
	return rec, dir, l
}

func usePG(t *testing.T, w *fakePG) {
	t.Helper()
	hostAs(t, "darwin") // a recorded unit is only proven, ended or placed through its own platform's kind
	swapContainment(t, KindPGroup, pgContainment{os: w})
}

// ----- create -----

func TestPGroupCreateRecordsAnIdentityAndProvesIt(t *testing.T) {
	w := newFakePG()
	rec, dir, l := createPG(t, w, 7)
	var h pgHandle
	if rec.Kind != KindPGroup || json.Unmarshal([]byte(rec.Handle), &h) != nil || h.Pgid != rec.AnchorPID || h.CreatorPID != 1 || h.CreatorStamp != "s-self" || h.Sid != 1 {
		t.Fatalf("record %+v handle %+v", rec, h)
	}
	if rec.CgroupID == 0 || rec.AnchorStamp == "" || rec.Generation != 7 || rec.BootID != "boot-m" {
		t.Fatalf("record %+v", rec)
	}
	if strings.Join(l.ev, ",") != "intent,bound" {
		t.Fatalf("journal order %v", l.ev)
	}
	if got, ok, err := dir.ReadUnitFile(7); err != nil || !ok || got != rec {
		t.Fatalf("unit file %+v %v %v", got, ok, err)
	}
	p, err := proveGroup(w, rec)
	if err != nil || p.Gone || !p.BestEffort || p.Cg == nil {
		t.Fatalf("proof %+v %v", p, err)
	}
	if pids, _ := p.Cg.Procs(""); len(pids) != 0 {
		t.Fatalf("the anchor is not a member: %v", pids)
	}
	if empty, err := UnitEmpty(p.Cg, rec); err != nil || !empty {
		t.Fatalf("a fresh unit holds only its anchor: %v %v", empty, err)
	}
}

func TestPGroupUnitsOfOneGenerationHaveDistinctIdentities(t *testing.T) {
	w := newFakePG()
	a, _, _ := createPG(t, w, 7)
	b, _, _ := createPG(t, w, 7)
	if a.CgroupID == b.CgroupID || a.Name == b.Name || a.AnchorPID == b.AnchorPID {
		t.Fatal("two units of one generation share an identity")
	}
}

func TestPGroupCreateEndsTheAnchorOnEveryLaterFailure(t *testing.T) {
	w := newFakePG()
	w.failAt = "group" // the anchor is not its own group leader: the unit is refused and the anchor ended
	_, err := createPGroup(bg, w, CreateArgs{Dir: newRunDir(t, nil)}, 7)
	if !errors.Is(err, ErrUnitNotCreated) || len(w.killed) != 1 || len(w.procs) != 1 {
		t.Fatalf("err %v killed %v procs %d", err, w.killed, len(w.procs))
	}
	w = newFakePG()
	w.failAt = "anchor"
	if _, err := createPGroup(bg, w, CreateArgs{Dir: newRunDir(t, nil)}, 7); !errors.Is(err, ErrUnitNotCreated) {
		t.Fatal(err)
	}
	w = newFakePG()
	fs := newFaultFS()
	fs.failCreate[".unit"] = true
	if _, err := createPGroup(bg, w, CreateArgs{Dir: newRunDir(t, fs)}, 7); !errors.Is(err, ErrUnitNotCreated) || len(w.killed) != 1 {
		t.Fatalf("err %v killed %v", err, w.killed)
	}
}

// ----- prove -----

func TestPGroupProveRefusesWhatItCannotTie(t *testing.T) {
	w := newFakePG()
	rec, _, _ := createPG(t, w, 7)
	cases := map[string]func(*UnitRecord){
		"no handle":         func(r *UnitRecord) { r.Handle = "" },
		"handle of another": func(r *UnitRecord) { r.Handle = `{"pgid":999,"sid":1,"creator_pid":1,"creator_stamp":"x"}` },
		"no creator stamp":  func(r *UnitRecord) { r.Handle = fmt.Sprintf(`{"pgid":%d,"sid":1,"creator_pid":1}`, r.AnchorPID) },
		"generation":        func(r *UnitRecord) { r.Generation = 8 },
		"nonce":             func(r *UnitRecord) { r.Nonce = strings.Repeat("0", 32) },
		"no anchor":         func(r *UnitRecord) { r.AnchorPID, r.AnchorStamp = 0, "" },
	}
	for name, mut := range cases {
		other := rec
		mut(&other)
		if _, err := proveGroup(w, other); !errors.Is(err, ErrNotProven) {
			t.Errorf("%s: %v", name, err)
		}
	}
}

func TestPGroupProveRefusesAnAnchorThatIsNotTheGroupLeader(t *testing.T) {
	w := newFakePG()
	rec, _, _ := createPG(t, w, 7)
	w.procs[rec.AnchorPID].pgid = 1 // the anchor left for another group: pgid != anchor pid
	if _, err := proveGroup(w, rec); !errors.Is(err, ErrNotProven) {
		t.Fatalf("a pgid the anchor no longer leads was proven: %v", err)
	}
}

func TestPGroupProveRefusesAReusedAnchorPidWhileTheGroupHasMembers(t *testing.T) {
	w := newFakePG()
	rec, _, _ := createPG(t, w, 7)
	w.procs[rec.AnchorPID].stamp = "reused"
	if _, err := proveGroup(w, rec); !errors.Is(err, ErrNotProven) {
		t.Fatalf("a pid with another start time was proven as the anchor: %v", err)
	}
	// nothing left in the group, whoever holds the pid now: there is nothing to signal
	delete(w.procs, rec.AnchorPID)
	if p, err := proveGroup(w, rec); err != nil || !p.Gone || !p.BestEffort {
		t.Fatalf("%+v %v", p, err)
	}
}

func TestPGroupDeadAnchorWithLivingMembersIsNotProven(t *testing.T) {
	w := newFakePG()
	rec, _, _ := createPG(t, w, 7)
	w.add(rec, 500, "git")
	w.procs[rec.AnchorPID].zombie = true
	if _, err := proveGroup(w, rec); !errors.Is(err, ErrNotProven) {
		t.Fatalf("a group number that may have been reused was signalled through: %v", err)
	}
}

func TestPGroupProveRefusesAGroupThatHoldsTheSignaller(t *testing.T) {
	w := newFakePG()
	rec, _, _ := createPG(t, w, 7)
	w.procs[1].pgid = rec.AnchorPID
	if _, err := proveGroup(w, rec); !errors.Is(err, ErrNotProven) {
		t.Fatalf("a Stop that is inside the group it would kill: %v", err)
	}
}

func TestPGroupProveAfterRebootIsGoneBestEffort(t *testing.T) {
	w := newFakePG()
	rec, _, _ := createPG(t, w, 7)
	rec.BootID = "older-boot"
	delete(w.procs, rec.AnchorPID) // after a reboot nothing of the unit is left: the anchor and the group are gone
	p, err := proveGroup(w, rec)
	if err != nil || !p.Gone || !p.BestEffort {
		t.Fatalf("%+v %v", p, err)
	}
}

// ----- end -----

// endPG ends the unit as the production wiring does: the record is the witness's (authenticated) one as well as the
// run directory's.
func endPG(t *testing.T, w *fakePG, rec UnitRecord, dir RunDir, exclude []uint64) UnitOutcome {
	t.Helper()
	usePG(t, w)
	u := UnitEnder{K: newFakeKernel(), Dir: dir, Witness: func(uint64) []UnitRecord { return []UnitRecord{rec} }, Cfg: EndConfig{Clock: newStepClock(), Known: func(string, int, string) bool { return true }}}
	return u.End(bg, rec.Generation, exclude)
}

func TestPGroupEndTermsEveryMemberThenKillsTheGroupAndIsBestEffort(t *testing.T) {
	w := newFakePG()
	rec, dir, _ := createPG(t, w, 7)
	w.add(rec, 500, "git")
	w.add(rec, 501, "sh")
	w.add(rec, 502, "node")
	out := endPG(t, w, rec, dir, nil)
	if out.Kind != UnitBestEffort || len(out.Survivors) != 0 {
		t.Fatalf("a group end is never a proven end: %+v", out)
	}
	if len(w.pidSig) < 3 || !strings.HasSuffix(w.pidSig[0], fmt.Sprintf(":%d", syscall.SIGTERM)) {
		t.Fatalf("members were not asked to end first: %v", w.pidSig)
	}
	for pid := range w.procs {
		if pid != 1 {
			t.Fatalf("process %d is still alive", pid)
		}
	}
}

func TestPGroupKillSignalsTheGroupOnlyAndNeverAnOutsider(t *testing.T) {
	w := newFakePG()
	rec, _, _ := createPG(t, w, 7)
	w.add(rec, 500, "git")
	w.procs[900] = &fakePGProc{stamp: "o", comm: "bystander", pgid: 900, sid: 9}
	g := &pgGroup{os: w, rec: rec, pgid: rec.AnchorPID}
	if err := g.Kill(""); err != nil {
		t.Fatal(err)
	}
	if _, ok := w.procs[900]; !ok {
		t.Fatal("a process outside the group was killed")
	}
	if len(w.groupSig) == 0 || w.groupSig[0] != fmt.Sprintf("%d:%d", rec.AnchorPID, syscall.SIGKILL) {
		t.Fatalf("the kill did not go to the recorded group: %v", w.groupSig)
	}
}

func TestPGroupSignalRefusesAPidThatLeftTheGroup(t *testing.T) {
	w := newFakePG()
	rec, _, _ := createPG(t, w, 7)
	p := w.add(rec, 500, "daemon")
	p.pgid = 500 // called setpgid: it is no longer ours to signal through this unit
	g := &pgGroup{os: w, rec: rec, pgid: rec.AnchorPID}
	if err := g.Signal("", 500, syscall.SIGTERM); err == nil || len(w.pidSig) != 0 {
		t.Fatalf("a process outside the group was signalled: %v %v", err, w.pidSig)
	}
}

func TestPGroupEscapedProcessIsNotSeenAndTheEndIsStillBestEffort(t *testing.T) {
	w := newFakePG()
	rec, dir, _ := createPG(t, w, 7)
	w.procs[700] = &fakePGProc{stamp: "e", comm: "double-forked", pgid: 700, sid: 700} // setsid: out of the group
	out := endPG(t, w, rec, dir, nil)
	if out.Kind != UnitBestEffort {
		t.Fatalf("%+v", out)
	}
	if _, ok := w.procs[700]; !ok {
		t.Fatal("a process that left the group was signalled")
	}
}

func TestPGroupSurvivorsSayTheyStayedInTheGroupAfterKill(t *testing.T) {
	w := newFakePG()
	rec, dir, _ := createPG(t, w, 7)
	w.add(rec, 500, "unkillable").immune = true
	out := endPG(t, w, rec, dir, nil)
	if out.Kind != UnitStragglers || len(out.Survivors) != 1 || out.Survivors[0].PID != 500 ||
		!strings.Contains(out.Survivors[0].Why, "stayed in the group after KILL") {
		t.Fatalf("%+v", out)
	}
}

func TestPGroupEndOfAnEmptyGroupEndsItsAnchorAndIsBestEffort(t *testing.T) {
	w := newFakePG()
	rec, dir, _ := createPG(t, w, 7)
	if out := endPG(t, w, rec, dir, nil); out.Kind != UnitBestEffort {
		t.Fatalf("%+v", out)
	}
	if _, ok := w.procs[rec.AnchorPID]; ok {
		t.Fatal("the anchor was left running")
	}
	if present, err := (pgContainment{os: w}).Present(rec); err != nil || present {
		t.Fatalf("present %v %v", present, err)
	}
}

func TestPGroupStopWaitsForABegunOperationInsteadOfKillingAroundIt(t *testing.T) {
	w := newFakePG()
	rec, dir, _ := createPG(t, w, 7)
	w.add(rec, 500, "git")
	out := endPG(t, w, rec, dir, []uint64{3})
	if len(w.groupSig) != 0 || len(w.pidSig) != 0 || len(w.procs) != 3 {
		t.Fatalf("the begun operation's command was signalled: %v %v", w.groupSig, w.pidSig)
	}
	// an end that did nothing is not an end; the run staying pending is pinned by TestPGroupFlatGroupWithABegunOperationKeepsTheRunPending
	if out.Kind == UnitBestEffort || out.Kind == UnitEnded || !strings.Contains(out.Note, "not ended") {
		t.Fatalf("the outcome of an end that did nothing must say so: %+v", out)
	}
}

// ----- place, dispatch, probe -----

func TestPGroupPlaceStartsChildrenInTheAnchorsGroupAndRefusesWhatItCannotProve(t *testing.T) {
	w := newFakePG()
	rec, _, _ := createPG(t, w, 7)
	pl, err := pgContainment{os: w}.Place(rec)
	if err != nil {
		t.Fatal(err)
	}
	for name, place := range map[string]func(*exec.Cmd) (func(), error){
		"Place":       pl.Place,
		"PlaceTicket": func(c *exec.Cmd) (func(), error) { return pl.PlaceTicket(5, c) },
	} {
		release, err := place(exec.Command("git"))
		if err != nil || release == nil {
			t.Fatalf("%s: %v", name, err)
		}
		release()
	}
	if len(w.prepared) != 2 || w.prepared[0] != rec.AnchorPID {
		t.Fatalf("children were not prepared for the anchor's group: %v", w.prepared)
	}
	w.procs[1].sid = 77 // the caller is in another session: a process cannot join a group across sessions
	if _, err := pl.Place(exec.Command("git")); !errors.Is(err, ErrNotPlaced) {
		t.Fatalf("a placement that cannot work was accepted: %v", err)
	}
	w.procs[1].sid = 1
	w.procs[rec.AnchorPID].stamp = "reused"
	if _, err := pl.Place(exec.Command("git")); !errors.Is(err, ErrNotPlaced) {
		t.Fatalf("an unproven unit placed a command: %v", err)
	}
	if len(w.prepared) != 2 {
		t.Fatal("a command was prepared for an unproven unit")
	}
}

func TestPGroupCreateDispatchesByPlatformAndProbeRunsTheSelfTest(t *testing.T) {
	w := newFakePG()
	usePG(t, w)
	if platformKinds["darwin"] != KindPGroup {
		t.Fatalf("darwin maps to %q", platformKinds["darwin"])
	}
	rec, err := createUnit(bg, "darwin", nil, CreateArgs{Dir: newRunDir(t, nil)}, 9)
	if err != nil || rec.Kind != KindPGroup {
		t.Fatalf("%+v %v", rec, err)
	}
	if err := RunProbe(pgSelfTestSteps(w)); err != nil {
		t.Fatal(err)
	}
	if len(w.procs) != 2 { // the creator and the unit just created; the self-test left nothing behind
		t.Fatalf("the self-test left a process: %d", len(w.procs))
	}
	w.failAt = "group"
	if err := RunProbe(pgSelfTestSteps(w)); !errors.Is(err, ErrContainmentUnavailable) {
		t.Fatalf("%v", err)
	}
	if err := (pgContainment{}).Probe(); !errors.Is(err, ErrContainmentUnavailable) {
		t.Fatalf("no macOS API: %v", err)
	}
}

func TestPGroupKillNeverSignalsTheGroupNumberOnceTheAnchorIsGone(t *testing.T) {
	w := newFakePG()
	rec, _, _ := createPG(t, w, 7)
	w.add(rec, 500, "git")
	delete(w.procs, rec.AnchorPID) // the number is no longer pinned: it may be someone else's group
	g := &pgGroup{os: w, rec: rec, pgid: rec.AnchorPID}
	if err := g.Kill(""); err != nil {
		t.Fatal(err)
	}
	if len(w.groupSig) != 0 || len(w.pidSig) != 1 {
		t.Fatalf("group %v pid %v", w.groupSig, w.pidSig)
	}
}
