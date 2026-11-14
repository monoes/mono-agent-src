package stop

import (
	"errors"
	"syscall"
	"testing"
)

// ----- S3 hardening of the OS layer's reads and signals -----

// A member whose Info read fails AND whose plain start stamp now differs is a number the kernel handed to someone else:
// it is skipped, never signalled (a failed read is not licence to signal whatever holds the number).
func TestSignalSkipsAMemberWhoseReadFailsAndWhoseStampDiffers(t *testing.T) {
	r := newSessRig(t)
	l := r.w.leader(500)
	r.record(l)
	m := l.SessProc
	r.w.infoErrs[m.PID] = errors.New("the process keeps changing")
	r.w.procs[m.PID].Stamp = "1.000001" // the number now names another process
	g := &sessGroup{c: r.c, rec: r.rec}
	if err := g.signal(m, syscall.SIGKILL); err != nil {
		t.Fatalf("a reused number is skipped without error: %v", err)
	}
	if r.w.count("kill:500") != 0 {
		t.Fatalf("a process that is not the member was killed (%v)", r.w.log)
	}
}

// A table entry that keeps answering EIO is read as gone only when the kernel itself says the process does not exist
// (kill(pid, 0) == ESRCH). A process that can still be signalled is not gone: that is an error (NOT PROVEN upstream), never
// "nothing there".
func TestSettleEIOReadsAsGoneOnlyWhenTheKernelSaysSo(t *testing.T) {
	always := func() (int, error) { return 0, syscall.EIO }
	noPause := func() {}
	cases := []struct {
		name     string
		probe    error
		wantGone bool
	}{
		{"kill says ESRCH", syscall.ESRCH, true},
		{"kill says the process exists", nil, false},
		{"kill says EPERM (exists, another user)", syscall.EPERM, false},
	}
	for _, c := range cases {
		probe := func(int) error { return c.probe }
		_, err := settleEIO(1234, always, probe, noPause)
		if c.wantGone != errors.Is(err, errSessGone) {
			t.Errorf("%s: gone=%v, err=%v", c.name, errors.Is(err, errSessGone), err)
		}
		if err == nil {
			t.Errorf("%s: a persistent EIO read succeeded", c.name)
		}
		if !c.wantGone && !errors.Is(err, syscall.EIO) {
			t.Errorf("%s: the unreadable entry's cause is lost: %v", c.name, err)
		}
	}
}

// An EIO that clears is just a read; any other error is returned at once, untouched, and never retried or probed.
func TestSettleEIORetriesOnlyEIO(t *testing.T) {
	calls := 0
	v, err := settleEIO(7, func() (int, error) {
		calls++
		if calls < 3 {
			return 0, syscall.EIO
		}
		return 42, nil
	}, func(int) error { t.Fatal("probed a read that recovered"); return nil }, func() {})
	if err != nil || v != 42 || calls != 3 {
		t.Fatalf("v=%d err=%v calls=%d", v, err, calls)
	}
	boom := errors.New("other")
	calls = 0
	if _, err := settleEIO(7, func() (int, error) { calls++; return 0, boom }, func(int) error { t.Fatal("probed"); return nil }, func() {}); !errors.Is(err, boom) || calls != 1 {
		t.Fatalf("err=%v calls=%d", err, calls)
	}
}

// ----- Adopt: who reaps the leader after a refused adoption -----

// A leader whose hold was taken and then dropped because the ledger refused is reaped by the unit's release (a goroutine), not
// by the launcher: Adopt says so, so the launcher never waits for a process the hold is reaping.
func TestAdoptSaysWhenTheUnitHasTakenOverReapingEvenIfTheLedgerRefused(t *testing.T) {
	r := newSessRig(t)
	l := r.w.leader(500)
	r.led.addErr = errors.New("witness full")
	err := r.adopt(l)
	if !errors.Is(err, ErrNotPlaced) {
		t.Fatalf("a refused line must be a refused adoption: %v", err)
	}
	if !errors.Is(err, ErrReapOwned) {
		t.Fatalf("the hold was taken (and its release reaps the leader), so the launcher must be told not to: %v", err)
	}
}

// Every refusal before the hold leaves the leader the launcher's own child to reap: ErrReapOwned is exactly "the hold took it".
func TestAdoptRefusalsBeforeTheHoldLeaveTheReapToTheLauncher(t *testing.T) {
	for name, set := range map[string]func(r *sessRig, l *fakeSessProc) (pid int, stamp string){
		"hold refused": func(r *sessRig, l *fakeSessProc) (int, string) {
			r.w.errs["Hold"] = errors.New("cannot keep it")
			return l.PID, l.Stamp
		},
		"ledger unread": func(r *sessRig, l *fakeSessProc) (int, string) {
			r.led.readErr = errors.New("x")
			return l.PID, l.Stamp
		},
		"stamp differs": func(r *sessRig, l *fakeSessProc) (int, string) { return l.PID, "9.000009" },
		"not its own session": func(r *sessRig, l *fakeSessProc) (int, string) {
			r.w.procs[l.PID].SID = 1
			return l.PID, l.Stamp
		},
	} {
		r := newSessRig(t)
		l := r.w.leader(500)
		pid, stamp := set(r, l)
		err := r.placer().Adopt(pid, stamp)
		if !errors.Is(err, ErrNotPlaced) || errors.Is(err, ErrReapOwned) {
			t.Errorf("%s: %v", name, err)
		}
	}
	// the cap refuses before the hold too
	old := sessMaxHolds
	sessMaxHolds = 1
	defer func() { sessMaxHolds = old }()
	r := newSessRig(t)
	if err := r.adopt(r.w.leader(500)); err != nil {
		t.Fatal(err)
	}
	if err := r.adopt(r.w.leader(520)); !errors.Is(err, ErrNotPlaced) || errors.Is(err, ErrReapOwned) {
		t.Errorf("at the cap: %v", err)
	}
}

// The placement names its unit, so the host can release the holds of exactly that unit when it ends.
func TestSessPlacerNamesItsUnit(t *testing.T) {
	r := newSessRig(t)
	u, ok := Placement(r.placer()).(interface{ UnitID() uint64 })
	if !ok || u.UnitID() != r.rec.CgroupID || r.rec.CgroupID == 0 {
		t.Fatalf("UnitID = %v (%v), want %d", u, ok, r.rec.CgroupID)
	}
}
