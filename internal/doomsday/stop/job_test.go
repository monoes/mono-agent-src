package stop

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os/exec"
	"sort"
	"strings"
	"syscall"
	"testing"
)

// ----- a fake Windows: processes, jobs, and the anchor's handle -----

type fakeWinProc struct {
	stamp, comm string
	job         string // the job the process is born in or was put in
}

type fakeWin struct {
	procs     map[int]*fakeWinProc
	jobs      map[string]*fakeWinJob
	next      int
	self      int
	failAt    string // "create", "adopt", "anchor"
	killed    []int
	parentFor []int
	terminate int
}

type fakeWinJob struct {
	w    *fakeWin
	name string
}

func newFakeWin() *fakeWin {
	w := &fakeWin{procs: map[int]*fakeWinProc{}, jobs: map[string]*fakeWinJob{}, next: 100, self: 1}
	w.procs[1] = &fakeWinProc{stamp: "s-self", comm: "monoagentcli.exe"}
	return w
}

func (w *fakeWin) BootID() (string, error) { return "boot-w", nil }
func (w *fakeWin) Self() (int, string, error) {
	return w.self, w.procs[w.self].stamp, nil
}
func (w *fakeWin) StartAnchor() (int, string, error) {
	if w.failAt == "anchor" {
		return 0, "", errors.New("no ping.exe")
	}
	w.next++
	w.procs[w.next] = &fakeWinProc{stamp: fmt.Sprintf("s%d", w.next), comm: "ping.exe"}
	return w.next, w.procs[w.next].stamp, nil
}
func (w *fakeWin) KillAnchor(pid int, stamp string) error {
	if p, ok := w.procs[pid]; ok && p.stamp == stamp {
		w.killed = append(w.killed, pid)
		w.endProc(pid)
	}
	return nil
}
func (w *fakeWin) endProc(pid int) {
	if p := w.procs[pid]; p != nil && p.job != "" {
		// the job lives while a process of it lives (the anchor holds its handle)
		p.job = ""
	}
	delete(w.procs, pid)
	for name := range w.jobs {
		if len(w.members(name)) == 0 && name != "" && !w.held(name) {
			delete(w.jobs, name)
		}
	}
}
func (w *fakeWin) held(string) bool { return false }
func (w *fakeWin) members(name string) []int {
	var out []int
	for pid, p := range w.procs {
		if p.job == name {
			out = append(out, pid)
		}
	}
	sort.Ints(out)
	return out
}
func (w *fakeWin) CreateJob(name string) (Job, error) {
	if w.failAt == "create" {
		return nil, errors.New("access denied")
	}
	if _, ok := w.jobs[name]; ok {
		return nil, errors.New("exists")
	}
	j := &fakeWinJob{w: w, name: name}
	w.jobs[name] = j
	return j, nil
}
func (w *fakeWin) OpenJob(name string) (Job, error) {
	if j, ok := w.jobs[name]; ok {
		return &fakeWinJob{w: w, name: j.name}, nil
	}
	return nil, ErrJobAbsent
}
func (w *fakeWin) Adopt(j Job, pid int, stamp string) error {
	if w.failAt == "adopt" {
		return errors.New("assign refused")
	}
	p := w.procs[pid]
	if p == nil || p.stamp != stamp {
		return errors.New("not the anchor")
	}
	p.job = j.(*fakeWinJob).name
	return nil
}
func (w *fakeWin) ProcInfo(pid int) (string, string, error) {
	p := w.procs[pid]
	if p == nil {
		return "", "", errors.New("no such process")
	}
	return p.stamp, p.comm, nil
}
func (w *fakeWin) ParentFor(cmd *exec.Cmd, pid int, stamp string) (func(), error) {
	if p := w.procs[pid]; p == nil || p.stamp != stamp {
		return nil, errors.New("anchor changed")
	}
	w.parentFor = append(w.parentFor, pid)
	return func() {}, nil
}

func (j *fakeWinJob) Pids() ([]int, error) { return j.w.members(j.name), nil }
func (j *fakeWinJob) Close() error         { return nil }
func (j *fakeWinJob) Contains(pid int) (bool, error) {
	p := j.w.procs[pid]
	return p != nil && p.job == j.name, nil
}
func (j *fakeWinJob) Terminate() error {
	j.w.terminate++
	for _, pid := range j.w.members(j.name) {
		j.w.endProc(pid)
	}
	delete(j.w.jobs, j.name)
	return nil
}
func (j *fakeWinJob) TerminateMember(pid int) error {
	if ok, _ := j.Contains(pid); !ok {
		return errors.New("not a member")
	}
	j.w.endProc(pid)
	return nil
}

// useWin makes the job kind answer through w and returns the containment.
func useWin(t *testing.T, w *fakeWin) jobContainment {
	t.Helper()
	c := jobContainment{os: w}
	hostAs(t, "windows")
	swapContainment(t, KindJobObject, c)
	// swapContainment replaces the entry by a function returning c, so the real dispatch reaches the fake
	return c
}

type jlog struct{ ev []string }

func (l *jlog) Intent(gen uint64, name, nonce, boot string) error {
	l.ev = append(l.ev, "intent")
	return nil
}
func (l *jlog) Bound(rec UnitRecord) error { l.ev = append(l.ev, "bound"); return nil }

func createWin(t *testing.T, w *fakeWin, gen uint64) (UnitRecord, RunDir, *jlog) {
	t.Helper()
	dir := newRunDir(t, nil)
	l := &jlog{}
	rec, err := createJob(bg, w, CreateArgs{Dir: dir, Journal: l}, gen)
	if err != nil {
		t.Fatal(err)
	}
	return rec, dir, l
}

// ----- create -----

func TestJobCreateRecordsAnIdentityAndProvesIt(t *testing.T) {
	w := newFakeWin()
	rec, dir, l := createWin(t, w, 7)
	var h jobHandle
	if rec.Kind != KindJobObject || json.Unmarshal([]byte(rec.Handle), &h) != nil || h.Job == "" || h.CreatorPID != 1 || h.CreatorStamp != "s-self" {
		t.Fatalf("record %+v handle %+v", rec, h)
	}
	if rec.CgroupID == 0 || rec.AnchorPID == 0 || rec.AnchorStamp == "" || rec.Generation != 7 {
		t.Fatalf("record %+v", rec)
	}
	if strings.Join(l.ev, ",") != "intent,bound" {
		t.Fatalf("journal order %v", l.ev)
	}
	if got, ok, err := dir.ReadUnitFile(7); err != nil || !ok || got != rec {
		t.Fatalf("unit file %+v %v %v", got, ok, err)
	}
	p, err := proveJob(w, rec)
	if err != nil || p.Gone || !p.BestEffort || p.Cg == nil {
		t.Fatalf("proof %+v %v", p, err)
	}
	if pids, _ := p.Cg.Procs(""); len(pids) != 0 {
		t.Fatalf("the anchor is not a member: %v", pids)
	}
	if all, _ := p.Cg.(*jobGroup).job.Pids(); len(all) != 1 || all[0] != rec.AnchorPID {
		t.Fatalf("the job holds %v, want its anchor", all)
	}
	if empty, err := UnitEmpty(p.Cg, rec); err != nil || !empty {
		t.Fatalf("a fresh unit holds only its anchor: %v %v", empty, err)
	}
}

func TestJobUnitsOfOneGenerationHaveDistinctIdentities(t *testing.T) {
	w := newFakeWin()
	a, _, _ := createWin(t, w, 7)
	b, _, _ := createWin(t, w, 7)
	if a.CgroupID == b.CgroupID || a.Name == b.Name {
		t.Fatal("two units of one generation share an identity (the witness keys its unit-ended line by it)")
	}
}

func TestJobCreateEndsTheAnchorOnEveryLaterFailure(t *testing.T) {
	for _, at := range []string{"create", "adopt"} {
		w := newFakeWin()
		w.failAt = at
		_, err := createJob(bg, w, CreateArgs{Dir: newRunDir(t, nil)}, 7)
		if !errors.Is(err, ErrUnitNotCreated) || len(w.killed) != 1 || len(w.procs) != 1 {
			t.Fatalf("%s: err %v killed %v procs %d", at, err, w.killed, len(w.procs))
		}
	}
	w := newFakeWin()
	w.failAt = "anchor"
	if _, err := createJob(bg, w, CreateArgs{Dir: newRunDir(t, nil)}, 7); !errors.Is(err, ErrUnitNotCreated) {
		t.Fatal(err)
	}
	// the key-free file cannot be written: the unit does not exist for the run
	w = newFakeWin()
	fs := newFaultFS()
	fs.failCreate[".unit"] = true
	dir := newRunDir(t, fs)
	if _, err := createJob(bg, w, CreateArgs{Dir: dir}, 7); !errors.Is(err, ErrUnitNotCreated) || len(w.killed) != 1 {
		t.Fatalf("err %v killed %v", err, w.killed)
	}
}

// ----- prove -----

func TestJobProveRefusesWhatItCannotTie(t *testing.T) {
	w := newFakeWin()
	rec, _, _ := createWin(t, w, 7)
	other := rec
	cases := map[string]func(*UnitRecord){
		"no handle":           func(r *UnitRecord) { r.Handle = "" },
		"handle of another":   func(r *UnitRecord) { r.Handle = `{"job":"monoagent-run-7-aaaa","creator_pid":1,"creator_stamp":"x"}` },
		"no creator stamp":    func(r *UnitRecord) { r.Handle = fmt.Sprintf(`{"job":%q,"creator_pid":1}`, jobName(r.Name)) },
		"generation":          func(r *UnitRecord) { r.Generation = 8 },
		"nonce":               func(r *UnitRecord) { r.Nonce = strings.Repeat("0", 32) },
		"anchor stamp":        func(r *UnitRecord) { r.AnchorStamp = "reused" },
		"no anchor":           func(r *UnitRecord) { r.AnchorPID, r.AnchorStamp = 0, "" },
		"anchor is not alive": func(r *UnitRecord) { r.AnchorPID = 9999 },
	}
	for name, mut := range cases {
		other = rec
		mut(&other)
		if _, err := proveJob(w, other); !errors.Is(err, ErrNotProven) {
			t.Errorf("%s: %v", name, err)
		}
	}
}

func TestJobProveRefusesAJobThatHoldsTheSignaller(t *testing.T) {
	w := newFakeWin()
	rec, _, _ := createWin(t, w, 7)
	w.procs[1].job = jobName(rec.Name)
	if _, err := proveJob(w, rec); !errors.Is(err, ErrNotProven) {
		t.Fatalf("a Stop that is inside the job it would kill: %v", err)
	}
}

func TestJobAnchorOutsideTheJobIsNotProven(t *testing.T) {
	w := newFakeWin()
	rec, _, _ := createWin(t, w, 7)
	w.procs[rec.AnchorPID].job = ""
	if _, err := proveJob(w, rec); !errors.Is(err, ErrNotProven) {
		t.Fatalf("a job of that name that does not hold the recorded anchor was proven: %v", err)
	}
}

func TestJobAbsentIsGoneButOnlyBestEffort(t *testing.T) {
	w := newFakeWin()
	rec, _, _ := createWin(t, w, 7)
	w.KillAnchor(rec.AnchorPID, rec.AnchorStamp)
	p, err := proveJob(w, rec)
	if err != nil || !p.Gone || !p.BestEffort {
		t.Fatalf("%+v %v", p, err)
	}
}

// ----- end -----

func endWin(t *testing.T, w *fakeWin, rec UnitRecord, dir RunDir, exclude []uint64) UnitOutcome {
	t.Helper()
	useWin(t, w)
	u := UnitEnder{K: newFakeKernel(), Dir: dir, Cfg: EndConfig{Clock: newStepClock(), Known: func(string, int, string) bool { return true }}}
	return u.End(bg, rec.Generation, exclude)
}

func TestJobEndKillsEveryMemberAndIsBestEffort(t *testing.T) {
	w := newFakeWin()
	rec, dir, _ := createWin(t, w, 7)
	w.procs[500] = &fakeWinProc{stamp: "c", comm: "git.exe", job: jobName(rec.Name)}
	w.procs[501] = &fakeWinProc{stamp: "d", comm: "sh.exe", job: jobName(rec.Name)}
	out := endWin(t, w, rec, dir, nil)
	if out.Kind != UnitBestEffort || len(out.Survivors) != 0 {
		t.Fatalf("%+v", out)
	}
	if out.Kind == UnitEnded {
		t.Fatal("a job end is never a proven end")
	}
	if _, ok := w.procs[500]; ok || w.terminate == 0 {
		t.Fatal("the members were not killed through the job")
	}
	if len(w.jobs) != 0 {
		t.Fatalf("jobs left: %v", w.jobs)
	}
}

func TestJobEndOfAnAbsentJobIsBestEffort(t *testing.T) {
	w := newFakeWin()
	rec, dir, _ := createWin(t, w, 7)
	w.KillAnchor(rec.AnchorPID, rec.AnchorStamp)
	if out := endWin(t, w, rec, dir, nil); out.Kind != UnitBestEffort {
		t.Fatalf("%+v", out)
	}
}

func TestJobStopWaitsForABegunOperationInsteadOfKillingAroundIt(t *testing.T) {
	w := newFakeWin()
	rec, dir, _ := createWin(t, w, 7)
	w.procs[500] = &fakeWinProc{stamp: "c", comm: "git.exe", job: jobName(rec.Name)}
	out := endWin(t, w, rec, dir, []uint64{3})
	if w.terminate != 0 || len(w.members(jobName(rec.Name))) != 2 {
		t.Fatalf("the begun operation's command was killed: terminate %d, %+v", w.terminate, out)
	}
}

func TestJobStragglerIsReportedNotBestEffort(t *testing.T) {
	w := newFakeWin()
	rec, dir, _ := createWin(t, w, 7)
	useWin(t, w)
	p, err := proveJob(w, rec)
	if err != nil {
		t.Fatal(err)
	}
	w.procs[500] = &fakeWinProc{stamp: "c", comm: "git.exe", job: jobName(rec.Name)}
	// a group that refuses to die: Kill does nothing
	g := &stubbornGroup{Group: p.Cg}
	out := EndUnit(g, EndConfig{Clock: newStepClock()})
	if out.Kind != UnitStragglers {
		t.Fatalf("%+v", out)
	}
	_ = dir
}

type stubbornGroup struct{ Group }

func (*stubbornGroup) Kill(string) error                        { return nil }
func (*stubbornGroup) Signal(string, int, syscall.Signal) error { return nil }

// ----- place, create dispatch, probe -----

func TestJobPlaceBornThroughTheAnchorAndRefusesAnUnprovenUnit(t *testing.T) {
	w := newFakeWin()
	rec, _, _ := createWin(t, w, 7)
	pl, err := jobContainment{os: w}.Place(rec)
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
	if len(w.parentFor) != 2 || w.parentFor[0] != rec.AnchorPID {
		t.Fatalf("children were not created under the anchor: %v", w.parentFor)
	}
	started := exec.Command("git")
	started.Process = nil
	w.procs[rec.AnchorPID].stamp = "reused"
	if _, err := pl.Place(exec.Command("git")); !errors.Is(err, ErrNotPlaced) {
		t.Fatalf("an unproven unit placed a command: %v", err)
	}
	if len(w.parentFor) != 2 {
		t.Fatal("a command was prepared for an unproven unit")
	}
}

func TestPlacerAndCreateDispatchByKind(t *testing.T) {
	w := newFakeWin()
	useWin(t, w)
	dir := newRunDir(t, nil)
	rec, err := createUnit(bg, "windows", nil, CreateArgs{Dir: dir}, 9)
	if err != nil || rec.Kind != KindJobObject {
		t.Fatalf("%+v %v", rec, err)
	}
	pl, err := NewPlacer(newFakeKernel(), dir, 9)
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := pl.(*jobPlacer); !ok {
		t.Fatalf("NewPlacer returned %T for a job unit", pl)
	}
	for _, goos := range []string{"darwin", "freebsd", ""} {
		if _, err := createUnit(bg, goos, nil, CreateArgs{Dir: dir}, 10); !errors.Is(err, ErrUnitNotCreated) {
			t.Fatalf("%q: %v", goos, err)
		}
	}
}

func TestJobProbeRunsTheSelfTestAndRefusesOffWindows(t *testing.T) {
	w := newFakeWin()
	if err := RunProbe(jobSelfTestSteps(w)); err != nil {
		t.Fatal(err)
	}
	if len(w.procs) != 1 || len(w.jobs) != 0 {
		t.Fatalf("the self-test left a process or job behind: %d %d", len(w.procs), len(w.jobs))
	}
	w.failAt = "adopt"
	if err := RunProbe(jobSelfTestSteps(w)); !errors.Is(err, ErrContainmentUnavailable) {
		t.Fatalf("%v", err)
	}
	if err := (jobContainment{}).Probe(); !errors.Is(err, ErrContainmentUnavailable) {
		t.Fatalf("no Windows API: %v", err)
	}
	if err := probe("windows", func() []ProbeStep { t.Fatal("linux steps consulted"); return nil }); !errors.Is(err, ErrContainmentUnavailable) {
		t.Fatalf("off Windows the job kind refuses: %v", err)
	}
}

func TestWindowsMapsToTheJobObject(t *testing.T) {
	if platformKinds["windows"] != KindJobObject {
		t.Fatalf("windows maps to %q", platformKinds["windows"])
	}
	if _, err := (jobContainment{}).Present(UnitRecord{}); err == nil {
		t.Fatal("presence cannot be answered without the Windows API")
	}
	var _ context.Context = bg
}
