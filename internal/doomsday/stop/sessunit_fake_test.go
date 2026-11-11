package stop

import (
	"errors"
	"fmt"
	"os/exec"
	"sort"
	"sync"
	"syscall"
	"testing"
)

// ----- a fake macOS: processes with a start stamp, a parent, a process group and a session -----

type fakeSessProc struct {
	SessProc
	zombie    bool // never listed
	immune    bool // survives SIGKILL
	noTerm    bool // ignores SIGTERM
	stopped   bool
	deniedSig bool // the signal is refused (EPERM: a process of another user)
}

type fakeSess struct {
	mu    sync.Mutex
	procs map[int]*fakeSessProc
	self  int
	uid   uint32
	next  int
	clock int
	held  map[int]bool // numbers kept reserved by an unreaped leader (Hold): a dead one stays a zombie, never listed

	boot string
	errs map[string]error // BootID Self Scan Info SelfTest CheckCmd

	log []string // "term:5" "stop:5" "kill:5" "killgrp:7" in the order they were made

	// onScan runs inside every Scan (a hook for a process that forks while the unit is ended); onSignal before a signal lands.
	onScan   func(w *fakeSess, n int)
	onSignal func(w *fakeSess, pid int, sig syscall.Signal)
	// afterScan runs once the snapshot a Scan returns was taken: what it changes is first seen by the next read.
	afterScan func(w *fakeSess, n int)
	scans     int
}

func newFakeSess() *fakeSess {
	w := &fakeSess{procs: map[int]*fakeSessProc{}, held: map[int]bool{}, self: 10, uid: 501, next: 1000, boot: "boot-m", errs: map[string]error{}}
	w.put(1, 0, 1, 1, "launchd")
	w.put(10, 1, 10, 10, "monoagentcli") // the signaller: the host or `doomsday stop`, in a session of its own
	return w
}

func (w *fakeSess) stamp(pid int) string { return fmt.Sprintf("%d.%06d", 1_800_000_000+pid, pid) }

// put adds a process; callers pass pid, ppid, pgid, sid and a name.
func (w *fakeSess) put(pid, ppid, pgid, sid int, comm string) *fakeSessProc {
	p := &fakeSessProc{SessProc: SessProc{PID: pid, PPID: ppid, PGID: pgid, SID: sid, Stamp: w.stamp(pid), UID: w.uid, Comm: comm}}
	w.procs[pid] = p
	return p
}

// leader adds a session leader that is a child of the signaller (what a started turn is).
func (w *fakeSess) leader(pid int) *fakeSessProc { return w.put(pid, w.self, pid, pid, "claude") }

// tryPut is put for a number the kernel may refuse: a number held by an unreaped leader is not handed out.
func (w *fakeSess) tryPut(pid, ppid, pgid, sid int, comm string) (*fakeSessProc, error) {
	if w.held[pid] {
		return nil, fmt.Errorf("pid %d is reserved by an unreaped process", pid)
	}
	return w.put(pid, ppid, pgid, sid, comm), nil
}

// Hold keeps the number reserved (the real binding leaves the child unreaped until release).
func (w *fakeSess) Hold(pid int, stamp string) (func(), error) {
	if err := w.errs["Hold"]; err != nil {
		return nil, err
	}
	w.mu.Lock()
	defer w.mu.Unlock()
	p := w.procs[pid]
	if p == nil || p.Stamp != stamp || p.PPID != w.self {
		return nil, errors.New("not an unreaped child of the caller")
	}
	w.held[pid] = true
	return func() { w.mu.Lock(); delete(w.held, pid); w.mu.Unlock() }, nil
}

func (w *fakeSess) BootID() (string, error) { return w.boot, w.errs["BootID"] }
func (w *fakeSess) Self() (int, string, error) {
	return w.self, w.procs[w.self].Stamp, w.errs["Self"]
}
func (w *fakeSess) CurrentUID() uint32 { return w.uid }

func (w *fakeSess) Scan() ([]SessProc, error) {
	w.mu.Lock()
	w.scans++
	n := w.scans
	hook := w.onScan
	w.mu.Unlock()
	if hook != nil {
		hook(w, n)
	}
	w.mu.Lock()
	defer w.mu.Unlock()
	if err := w.errs["Scan"]; err != nil {
		return nil, err
	}
	var out []SessProc
	for _, p := range w.procs {
		if !p.zombie {
			out = append(out, p.SessProc)
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].PID < out[j].PID })
	if w.afterScan != nil {
		after := w.afterScan
		w.mu.Unlock()
		after(w, n)
		w.mu.Lock()
	}
	return out, nil
}

func (w *fakeSess) Info(pid int) (SessProc, error) {
	w.mu.Lock()
	defer w.mu.Unlock()
	if err := w.errs["Info"]; err != nil {
		return SessProc{}, err
	}
	p := w.procs[pid]
	if p == nil || p.zombie {
		return SessProc{}, errors.New("no such process")
	}
	return p.SessProc, nil
}

func (w *fakeSess) logf(f string, a ...any) { w.log = append(w.log, fmt.Sprintf(f, a...)) }

func (w *fakeSess) Signal(pid int, sig syscall.Signal) error {
	w.mu.Lock()
	hook := w.onSignal
	w.mu.Unlock()
	if hook != nil {
		hook(w, pid, sig)
	}
	w.mu.Lock()
	defer w.mu.Unlock()
	if pid <= 1 {
		return fmt.Errorf("refusing to signal process %d", pid)
	}
	p := w.procs[pid]
	if p == nil || p.zombie {
		return nil
	}
	switch sig {
	case syscall.SIGTERM:
		w.logf("term:%d", pid)
	case sessSigStop:
		w.logf("stop:%d", pid)
	case syscall.SIGKILL:
		w.logf("kill:%d", pid)
	}
	if p.deniedSig {
		return syscall.EPERM
	}
	w.apply(p, sig)
	return nil
}

func (w *fakeSess) apply(p *fakeSessProc, sig syscall.Signal) {
	switch sig {
	case sessSigStop:
		p.stopped = true
	case syscall.SIGTERM:
		if !p.noTerm {
			delete(w.procs, p.PID)
		}
	case syscall.SIGKILL:
		if !p.immune {
			delete(w.procs, p.PID)
		}
	}
}

func (w *fakeSess) SignalGroup(pgid int, sig syscall.Signal) error {
	w.mu.Lock()
	defer w.mu.Unlock()
	if pgid <= 1 {
		return fmt.Errorf("refusing to signal group %d", pgid)
	}
	w.logf("killgrp:%d", pgid)
	for pid, p := range w.procs {
		if p.PGID == pgid && !p.deniedSig {
			w.apply(w.procs[pid], sig)
		}
	}
	return nil
}

func (w *fakeSess) CheckCmd(*exec.Cmd) error { return w.errs["CheckCmd"] }
func (w *fakeSess) SelfTest() error          { return w.errs["SelfTest"] }

func (w *fakeSess) has(pid int) bool {
	w.mu.Lock()
	defer w.mu.Unlock()
	_, ok := w.procs[pid]
	return ok
}

func (w *fakeSess) count(prefix string) int {
	n := 0
	for _, l := range w.log {
		if len(l) >= len(prefix) && l[:len(prefix)] == prefix {
			n++
		}
	}
	return n
}

// ----- a fake ledger -----

type fakeLedger struct {
	mu      sync.Mutex
	by      map[uint64][]Leader
	readErr error
	addErr  error
	adds    int
	ended   map[uint64]bool // unit ids whose end is recorded
	retired map[string]bool // "<unit>/<pid>/<stamp>" of sessions seen gone
	retErr  error
	retires int
}

func leaderKey(l Leader) string { return fmt.Sprintf("%d/%d/%s", l.Unit, l.PID, l.Stamp) }

func newFakeLedger() *fakeLedger {
	return &fakeLedger{by: map[uint64][]Leader{}, ended: map[uint64]bool{}, retired: map[string]bool{}}
}

func (l *fakeLedger) Leaders(gen uint64) ([]Leader, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.readErr != nil {
		return nil, l.readErr
	}
	out := append([]Leader(nil), l.by[gen]...)
	for i := range out {
		out[i].Empty = l.retired[leaderKey(out[i])]
	}
	return out, nil
}

func (l *fakeLedger) RetireLeader(gen uint64, x Leader) error {
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.retErr != nil {
		return l.retErr
	}
	if !l.retired[leaderKey(x)] {
		l.retires++
	}
	l.retired[leaderKey(x)] = true
	return nil
}

func (l *fakeLedger) AddLeader(gen uint64, x Leader) error {
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.addErr != nil {
		return l.addErr
	}
	if l.ended[x.Unit] {
		return ErrUnitEnded
	}
	l.adds++
	l.by[gen] = append(l.by[gen], x)
	return nil
}

// ----- a rig: a created unit, the fake OS, the ledger -----

type sessRig struct {
	t   *testing.T
	w   *fakeSess
	led *fakeLedger
	rec UnitRecord
	dir RunDir
	j   *jlog
	c   sessContainment
}

func hostStepsOK() []ProbeStep { return nil }

func newSessRig(t *testing.T) *sessRig {
	t.Helper()
	w := newFakeSess()
	led := newFakeLedger()
	r := &sessRig{t: t, w: w, led: led, dir: newRunDir(t, nil), j: &jlog{}, c: sessContainment{os: w, led: led, hostSteps: hostStepsOK}}
	rec, err := r.c.Create(bg, CreateArgs{Dir: r.dir, Journal: r.j}, 7)
	if err != nil {
		t.Fatal(err)
	}
	r.rec = rec
	return r
}

// record puts a leader in the ledger (what Adopt writes).
func (r *sessRig) record(p *fakeSessProc) {
	r.t.Helper()
	if err := r.led.AddLeader(r.rec.Generation, Leader{PID: p.PID, Stamp: p.Stamp, Unit: r.rec.CgroupID}); err != nil {
		r.t.Fatal(err)
	}
}

// useSess makes the fake the unit mechanism of a darwin host, as production would (the dispatch reaches it).
func (r *sessRig) useSess() {
	r.t.Helper()
	hostAs(r.t, "darwin")
	platformKindsAs(r.t, "darwin", KindSession)
	swapContainment(r.t, KindSession, r.c)
}

// end ends the unit as the production wiring does: the record is the witness's authenticated one as well.
func (r *sessRig) end(exclude []uint64) UnitOutcome {
	r.t.Helper()
	r.useSess()
	u := UnitEnder{K: newFakeKernel(), Dir: r.dir, Witness: func(uint64) []UnitRecord { return []UnitRecord{r.rec} },
		Cfg: EndConfig{Clock: newStepClock(), Known: func(string, int, string) bool { return true }}}
	return u.End(bg, r.rec.Generation, exclude)
}

// platformKindsAs makes goos start kind for the rest of the test (the session kind is named by no GOOS yet).
func platformKindsAs(t *testing.T, goos, kind string) {
	t.Helper()
	old, had := platformKinds[goos]
	platformKinds[goos] = kind
	t.Cleanup(func() {
		if had {
			platformKinds[goos] = old
		} else {
			delete(platformKinds, goos)
		}
	})
}
