package stop

import (
	"errors"
	"fmt"
	"os"
	"os/exec"
	"strings"
	"sync"
	"testing"
)

// ----- placement and adoption -----

func (r *sessRig) placer() *sessPlacer {
	r.t.Helper()
	p, err := r.c.Place(r.rec)
	if err != nil {
		r.t.Fatal(err)
	}
	return p.(*sessPlacer)
}

func TestSessPlaceChecksAndChangesNothing(t *testing.T) {
	r := newSessRig(t)
	cmd := exec.Command("/bin/true")
	cmd.Env = []string{"A=1"}
	path, args, env := cmd.Path, strings.Join(cmd.Args, " "), strings.Join(cmd.Env, " ")
	release, err := r.placer().Place(cmd)
	if err != nil || release == nil {
		t.Fatalf("an empty unit is placed into (the first turn has no leader yet): %v", err)
	}
	if cmd.SysProcAttr != nil || cmd.Path != path || strings.Join(cmd.Args, " ") != args || strings.Join(cmd.Env, " ") != env || cmd.Process != nil {
		t.Fatalf("placement changed the command (the turn is a session leader by the launcher's own attributes): %+v", cmd)
	}
	// only a started turn leader that is then adopted is in the unit: a placement that cannot be adopted is refused
	if _, err := r.placer().PlaceTicket(5, exec.Command("/bin/true")); !errors.Is(err, ErrNotPlaced) {
		t.Fatalf("PlaceTicket: %v", err)
	}
	if err := r.placer().PlaceLeased(exec.Command("/bin/true")); !errors.Is(err, ErrNotPlaced) {
		t.Fatalf("PlaceLeased: %v", err)
	}
}

func TestSessPlaceRefusesWhatItCannotPlace(t *testing.T) {
	for name, set := range map[string]func(r *sessRig) *exec.Cmd{
		"nil command":     func(r *sessRig) *exec.Cmd { return nil },
		"already started": func(r *sessRig) *exec.Cmd { c := exec.Command("/bin/true"); c.Process = &os.Process{Pid: 3}; return c },
		"command cannot lead": func(r *sessRig) *exec.Cmd {
			r.w.errs["CheckCmd"] = errors.New("not a session leader")
			return exec.Command("/bin/true")
		},
		"ledger unreadable": func(r *sessRig) *exec.Cmd { r.led.readErr = errors.New("x"); return exec.Command("/bin/true") },
		"unit not proven":   func(r *sessRig) *exec.Cmd { r.rec.Handle = ""; return exec.Command("/bin/true") },
		"boot time differs": func(r *sessRig) *exec.Cmd { r.w.boot = "boot-other"; return exec.Command("/bin/true") },
		"signaller in a member": func(r *sessRig) *exec.Cmd {
			r.record(r.w.leader(500))
			r.w.put(501, 500, 501, 501, "turn")
			r.w.self = 501
			return exec.Command("/bin/true")
		},
	} {
		r := newSessRig(t)
		cmd := set(r)
		p := &sessPlacer{c: r.c, rec: r.rec}
		if _, err := p.Place(cmd); !errors.Is(err, ErrNotPlaced) {
			t.Errorf("%s: %v", name, err)
		}
		if err := p.PlaceLeased(cmd); !errors.Is(err, ErrNotPlaced) {
			t.Errorf("%s leased: %v", name, err)
		}
	}
	if _, err := (sessContainment{}).Place(UnitRecord{}); !errors.Is(err, ErrNotPlaced) {
		t.Fatalf("no OS: %v", err)
	}
}

func TestSessOnlyTheSessionUnitAdopts(t *testing.T) {
	var _ Adopter = &sessPlacer{}
	for name, p := range map[string]Placement{"cgroup": &Placer{}, "group": &pgPlacer{}, "job": &jobPlacer{}} {
		if _, ok := p.(Adopter); ok {
			t.Errorf("the %s placement adopts: its commands are born inside the unit", name)
		}
	}
}

func TestSessNewPlacerReachesTheSessionPlacementThroughTheSeam(t *testing.T) {
	r := newSessRig(t)
	r.useSess()
	pl, err := NewPlacer(newFakeKernel(), r.dir, 7)
	if err != nil {
		t.Fatal(err)
	}
	ad, ok := pl.(Adopter)
	if !ok {
		t.Fatalf("%T does not adopt", pl)
	}
	l := r.w.leader(500)
	if err := ad.Adopt(l.PID, l.Stamp); err != nil {
		t.Fatal(err)
	}
}

func TestSessAdoptRecordsTheLeaderAndOnlyThenItsSessionIsTheUnits(t *testing.T) {
	r := newSessRig(t)
	l := r.w.leader(500)
	r.w.put(501, 500, 501, 501, "child")
	if v, _ := r.c.view(r.rec); len(v.members) != 0 {
		t.Fatalf("an unrecorded leader's session is in the unit: %+v", v.members)
	}
	if err := r.placer().Adopt(l.PID, l.Stamp); err != nil {
		t.Fatal(err)
	}
	got, _ := r.led.Leaders(7)
	if len(got) != 1 || got[0].PID != 500 || got[0].Stamp != l.Stamp || got[0].At == 0 || got[0].Unit != r.rec.CgroupID {
		t.Fatalf("%+v", got)
	}
	if v, _ := r.c.view(r.rec); len(v.members) != 2 {
		t.Fatalf("%+v", v.members)
	}
	if err := r.placer().Adopt(l.PID, l.Stamp); err != nil || r.led.adds != 1 {
		t.Fatalf("adopting the same leader again must record nothing new: %v, %d lines", err, r.led.adds)
	}
}

func TestSessAdoptRefusesWhatItCannotTie(t *testing.T) {
	type tc struct {
		set  func(r *sessRig) (pid int, stamp string)
		want string
	}
	cases := map[string]tc{
		"not a session leader": {func(r *sessRig) (int, string) { p := r.w.put(500, 10, 10, 10, "claude"); return 500, p.Stamp }, "session of its own"},
		"not our child":        {func(r *sessRig) (int, string) { p := r.w.put(500, 1, 500, 500, "claude"); return 500, p.Stamp }, "not a child"},
		"another start stamp (the pid was reused)": {func(r *sessRig) (int, string) {
			r.w.leader(500)
			return 500, "1700000000.000001"
		}, "start stamp differs"},
		"not running":        {func(r *sessRig) (int, string) { return 500, "1800000500.000500" }, "not running"},
		"pid 1":              {func(r *sessRig) (int, string) { return 1, r.w.procs[1].Stamp }, "cannot be checked"},
		"malformed stamp":    {func(r *sessRig) (int, string) { r.w.leader(500); return 500, "later" }, "cannot be checked"},
		"another user":       {func(r *sessRig) (int, string) { p := r.w.leader(500); p.UID = 0; return 500, p.Stamp }, "another user"},
		"the adopter itself": {func(r *sessRig) (int, string) { return 10, r.w.procs[10].Stamp }, "not a child"},
	}
	for name, c := range cases {
		r := newSessRig(t)
		pid, stamp := c.set(r)
		err := r.placer().Adopt(pid, stamp)
		if !errors.Is(err, ErrNotPlaced) || !strings.Contains(err.Error(), c.want) {
			t.Errorf("%s: %v", name, err)
		}
		if r.led.adds != 0 {
			t.Errorf("%s: a refused adoption wrote a ledger line", name)
		}
	}
}

func TestSessAdoptFailsWhenTheWitnessOrTheUnitCannotBeTrusted(t *testing.T) {
	for name, set := range map[string]func(r *sessRig){
		"the witness refuses the line": func(r *sessRig) { r.led.addErr = errors.New("witness full") },
		"the ledger cannot be read":    func(r *sessRig) { r.led.readErr = errors.New("x") },
		"no ledger wired":              func(r *sessRig) { r.c.led = nil },
		"the unit is not proven":       func(r *sessRig) { r.rec.Handle = "" },
		"the unit's end is recorded":   func(r *sessRig) { r.led.ended[r.rec.CgroupID] = true },
		"the boot time changed":        func(r *sessRig) { r.w.boot = "boot-other" },
	} {
		r := newSessRig(t)
		l := r.w.leader(500)
		set(r)
		if err := r.placer().Adopt(l.PID, l.Stamp); !errors.Is(err, ErrNotPlaced) {
			t.Errorf("%s: %v", name, err)
		}
		if r.led.adds != 0 {
			t.Errorf("%s: a line was written", name)
		}
	}
}

// ----- races -----

func TestSessConcurrentAdoptionsAreAllRecorded(t *testing.T) {
	r := newSessRig(t)
	const n = 24
	var leaders []*fakeSessProc
	for i := 0; i < n; i++ {
		leaders = append(leaders, r.w.leader(600+i))
	}
	var wg sync.WaitGroup
	errs := make(chan error, n)
	for _, l := range leaders {
		wg.Add(1)
		go func(l *fakeSessProc) {
			defer wg.Done()
			errs <- r.placer().Adopt(l.PID, l.Stamp)
		}(l)
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		if err != nil {
			t.Fatal(err)
		}
	}
	got, _ := r.led.Leaders(7)
	seen := map[int]bool{}
	for _, l := range got {
		seen[l.PID] = true
	}
	if len(seen) != n {
		t.Fatalf("%d of %d leaders were recorded", len(seen), n)
	}
	if v, _ := r.c.view(r.rec); len(v.members) != n {
		t.Fatalf("members %d", len(v.members))
	}
}

// Goroutine safety only (run under -race): the ordering of an Adopt against an end is the witness lock's, tested with the real
// sequencer in witness_leaders_test.go.
func TestSessKillRacingAdoptionsIsRaceFree(t *testing.T) {
	r := newSessRig(t)
	var leaders []*fakeSessProc
	for i := 0; i < 16; i++ {
		leaders = append(leaders, r.w.leader(600+i))
	}
	var wg sync.WaitGroup
	for _, l := range leaders {
		wg.Add(1)
		go func(l *fakeSessProc) {
			defer wg.Done()
			_ = r.placer().Adopt(l.PID, l.Stamp)
		}(l)
	}
	g := &sessGroup{c: r.c, rec: r.rec}
	wg.Add(1)
	go func() { defer wg.Done(); _ = g.Kill("") }()
	wg.Wait()
	// a final end after the race: every leader that made it into the ledger is gone, none is left behind
	for i := 0; i < 3; i++ {
		_ = (&sessGroup{c: r.c, rec: r.rec}).Kill("")
	}
	got, _ := r.led.Leaders(7)
	for _, l := range got {
		if r.w.has(l.PID) {
			t.Fatalf("recorded leader %d survived", l.PID)
		}
	}
}

// A member forks while the unit is ended: each freeze pass finds the child the pass before it could not see, until a
// pass finds nothing new, and every process is stopped before the first kill.
func TestSessAProcessThatForksWhileTheUnitIsFrozenIsStillCaught(t *testing.T) {
	r := newSessRig(t)
	l := r.w.leader(500)
	l.noTerm = true
	r.record(l)
	base := r.w.scans
	r.w.afterScan = func(w *fakeSess, n int) { // two forks, each after the snapshot that did not yet show the new process
		switch n - base {
		case 1:
			w.put(700, 500, 700, 500, "forked").noTerm = true
		case 2:
			w.put(701, 700, 701, 500, "forked-again").noTerm = true
		}
	}
	g := &sessGroup{c: r.c, rec: r.rec}
	if err := g.Kill(""); err != nil {
		t.Fatal(err)
	}
	for _, pid := range []int{500, 700, 701} {
		if r.w.has(pid) {
			t.Fatalf("process %d survived (log %v)", pid, r.w.log)
		}
	}
	firstKill := -1
	for i, e := range r.w.log {
		if strings.HasPrefix(e, "kill") && firstKill < 0 {
			firstKill = i
		}
	}
	for _, pid := range []int{500, 700, 701} {
		stopped := -1
		for i, e := range r.w.log {
			if e == fmt.Sprintf("stop:%d", pid) {
				stopped = i
			}
		}
		if stopped < 0 || stopped > firstKill {
			t.Fatalf("process %d was not frozen before the kill began: %v", pid, r.w.log)
		}
	}
}

func TestSessAForkBombThatOutrunsTheFreezeIsReportedNotHidden(t *testing.T) {
	old := sessKillRounds
	sessKillRounds = 3
	t.Cleanup(func() { sessKillRounds = old })
	r := newSessRig(t)
	l := r.w.leader(500)
	r.record(l)
	next := 700
	r.w.onScan = func(w *fakeSess, n int) { // a new child in every scan, whatever is stopped
		w.put(next, 500, next, 500, "bomb").noTerm = true
		next++
	}
	g := &sessGroup{c: r.c, rec: r.rec}
	err := g.Kill("")
	if err == nil || !strings.Contains(err.Error(), "still appearing") {
		t.Fatalf("a bound that was hit must be an error: %v", err)
	}
	if r.w.has(500) {
		t.Fatal("what the freeze passes did know was not killed")
	}
}

// A read that fails after the freeze began must not leave the unit stopped.
func TestSessAReadThatFailsAfterTheFreezeStillKillsWhatWasFrozen(t *testing.T) {
	r := newSessRig(t)
	l := r.w.leader(500)
	l.noTerm = true
	r.record(l)
	r.w.put(501, 500, 501, 500, "worker").noTerm = true
	base := r.w.scans
	r.w.afterScan = func(w *fakeSess, n int) {
		if n == base+1 {
			w.errs["Scan"] = errors.New("the process table cannot be read") // the freeze pass ran on scan 1; scan 2 fails
		}
	}
	g := &sessGroup{c: r.c, rec: r.rec}
	if err := g.Kill(""); err == nil {
		t.Fatal("the failed read was not reported")
	}
	if r.w.has(500) || r.w.has(501) {
		t.Fatalf("frozen processes were left stopped: %v (log %v)", r.w.procs, r.w.log)
	}
}

// The signaller and its ancestors are never signalled, even when a scan lists them as members.
func TestSessKillNeverSignalsTheSignallerOrItsAncestors(t *testing.T) {
	r := newSessRig(t)
	l := r.w.leader(500)
	r.record(l)
	r.w.put(501, 500, 501, 501, "stop-called-from-a-turn")
	r.w.put(502, 501, 502, 502, "the-signaller")
	r.w.self = 502
	g := &sessGroup{c: r.c, rec: r.rec} // built directly: Prove would refuse this unit, and this is the second line of defence
	_ = g.Kill("")
	for _, pid := range []int{500, 501, 502} {
		if !r.w.has(pid) {
			t.Fatalf("process %d (the signaller or its ancestor) was killed: %v", pid, r.w.log)
		}
	}
	if ps, _ := g.Procs(""); len(ps) != 0 {
		t.Fatalf("the signaller's own chain is listed as holdable: %v", ps)
	}
}

// The number of a member is handed to a stranger after the scan that listed it, in the freeze pass (phase 1) or after the
// last scan, just before the kill (phase 2): the stranger must not be signalled, neither by pid nor by group number.
func TestSessAPidThatIsReusedBetweenTheScanAndTheSignalIsNotSignalled(t *testing.T) {
	for phase := 1; phase <= 2; phase++ {
		r := newSessRig(t)
		l := r.w.leader(500)
		l.noTerm = true
		r.record(l)
		r.w.put(501, 500, 501, 500, "worker").noTerm = true
		base, swapped, mark := r.w.scans, false, 0
		r.w.afterScan = func(w *fakeSess, n int) {
			if !swapped && n == base+phase { // that scan has returned: 501 exits and its number (and group number) is reused
				swapped, mark = true, len(w.log)
				delete(w.procs, 501)
				w.put(501, 1, 501, 501, "stranger").Stamp = "1900000000.000009"
			}
		}
		g := &sessGroup{c: r.c, rec: r.rec}
		_ = g.Kill("")
		if !swapped {
			t.Fatalf("phase %d: the scan hook never ran", phase)
		}
		if !r.w.has(501) {
			t.Fatalf("phase %d: the process that took over the number was killed (log %v)", phase, r.w.log)
		}
		for _, e := range r.w.log[mark:] { // what was sent to the real 501 before the number moved is correct
			if e == "stop:501" || e == "kill:501" || e == "killgrp:501" {
				t.Fatalf("phase %d: the reused number was signalled: %v", phase, r.w.log)
			}
		}
	}
}

// A member whose parent exited (it was re-parented) between the scan and the signal is still the process that was seen as
// a member: it is ended in the same pass. It shares its group number with an outsider, so the group kill is not the path.
func TestSessAMemberThatWasReparentedBeforeTheSignalIsStillEnded(t *testing.T) {
	r := newSessRig(t)
	l := r.w.leader(500)
	r.record(l)
	r.w.put(501, 500, 900, 501, "child").noTerm = true
	r.w.put(900, 1, 900, 900, "outsider")
	base, done := r.w.scans, false
	r.w.afterScan = func(w *fakeSess, n int) {
		if !done && n == base+2 {
			done = true
			w.procs[501].PPID = 1 // its parent exited
		}
	}
	g := &sessGroup{c: r.c, rec: r.rec}
	_ = g.Kill("")
	if r.w.has(501) {
		t.Fatalf("a member that was re-parented was not ended (log %v)", r.w.log)
	}
	if !r.w.has(900) {
		t.Fatal("the outsider that shares its group number was killed")
	}
}

// A member that is re-parented stays a member for the rest of the end, and so do its children: a child forked after its parent
// died is not reached through the parent chain any more, but it is the child of a process the unit has seen.
func TestSessAChildOfAMemberThatLostItsParentIsStillCaught(t *testing.T) {
	r := newSessRig(t)
	l := r.w.leader(500)
	r.record(l)
	c := r.w.put(501, 500, 501, 501, "mid") // a member through its parent
	c.noTerm = true
	g := &sessGroup{c: r.c, rec: r.rec}
	if ps, _ := g.Procs(""); len(ps) != 2 {
		t.Fatalf("%v", ps)
	}
	delete(r.w.procs, 500) // its parent is gone: 501 is re-parented to init and has a session of its own
	c.PPID = 1
	r.w.put(502, 501, 502, 502, "forked-later").noTerm = true // forked after that
	if ps, err := g.Procs(""); err != nil || len(ps) != 2 {
		t.Fatalf("a seen member and its new child must both be listed: %v %v", ps, err)
	}
	_ = g.Kill("")
	if r.w.has(501) || r.w.has(502) {
		t.Fatalf("survivors: %v (log %v)", r.w.procs, r.w.log)
	}
}
