package stop

import (
	"errors"
	"os"
	"strings"
	"syscall"
	"testing"
)

func TestProveUnitRefusals(t *testing.T) {
	mut := map[string]func(k *fakeKernel, r *UnitRecord){
		"path outside the delegated root": func(k *fakeKernel, r *UnitRecord) { r.CgroupPath = "/sys/fs/cgroup/system.slice/" + r.Name },
		"dotdot":                          func(k *fakeKernel, r *UnitRecord) { r.CgroupPath = k.root + "/../" + r.Name },
		"a session scope, not a run unit": func(k *fakeKernel, r *UnitRecord) {
			r.Name = "session-3.scope"
			r.CgroupPath = k.root + "/session-3.scope"
		},
		"nonce differs from the name": func(k *fakeKernel, r *UnitRecord) { r.Nonce = strings.Repeat("a", 32) },
		"generation differs":          func(k *fakeKernel, r *UnitRecord) { r.Generation = 8 },
		"reused cgroup id":            func(k *fakeKernel, r *UnitRecord) { r.CgroupID = 41 },
		"owner differs":               func(k *fakeKernel, r *UnitRecord) { r.OwnerUID = 0 },
		"not cgroup2":                 func(k *fakeKernel, r *UnitRecord) { k.cg.id.IsCgroup2 = false },
		"foreign owner on disk":       func(k *fakeKernel, r *UnitRecord) { k.cg.id.UID = 0; r.OwnerUID = 0 },
		"signaller inside the unit":   func(k *fakeKernel, r *UnitRecord) { k.self = r.CgroupPath + "/t-1" },
		"signaller is the unit":       func(k *fakeKernel, r *UnitRecord) { k.self = r.CgroupPath },
		"manager reports another cg": func(k *fakeKernel, r *UnitRecord) {
			k.mgr, k.mgrAnsw = "/user.slice/other.service", true
		},
		"unknown kind": func(k *fakeKernel, r *UnitRecord) { r.Kind = "pgid" },
	}
	for name, f := range mut {
		t.Run(name, func(t *testing.T) {
			k, rec := newFakeKernel(), testRecord()
			f(k, &rec)
			if _, err := ProveUnit(k, rec); !errors.Is(err, ErrNotProven) {
				t.Fatalf("err %v, want ErrNotProven", err)
			}
		})
	}
}

func TestProveUnitGoodAndGone(t *testing.T) {
	k := newFakeKernel()
	p, err := ProveUnit(k, testRecord())
	if err != nil || p.Gone || p.Cg == nil || !p.ManagerUnverified {
		t.Fatalf("%+v %v", p, err)
	}
	// P5: a manager that answers consistently
	k.mgr, k.mgrAnsw = "user@1000.service/"+testRecord().Name, true
	if p, err = ProveUnit(k, testRecord()); err != nil || p.ManagerUnverified {
		t.Fatalf("%+v %v", p, err)
	}
	// P1: a different boot is proven gone without opening anything
	k2 := newFakeKernel()
	k2.boot = "boot-2"
	if p, err = ProveUnit(k2, testRecord()); err != nil || !p.Gone || k2.opened != 0 {
		t.Fatalf("%+v %v opened=%d", p, err, k2.opened)
	}
	// logout: the cgroup is absent
	k3 := newFakeKernel()
	k3.absent = true
	if p, err = ProveUnit(k3, testRecord()); err != nil || !p.Gone {
		t.Fatalf("%+v %v", p, err)
	}
}

func endCfg(known func(string, int, string) bool) EndConfig {
	return EndConfig{Clock: newStepClock(), Known: known}
}

func allKnown(string, int, string) bool { return true }

func TestEndUnitEscalation(t *testing.T) {
	cg := newFakeCg()
	cg.subs["t-1"] = []*fakeProc{{pid: 10, dieOnTerm: true}, {pid: 11, dieOnKill: true}} // one ignores SIGTERM
	out := EndUnit(cg, endCfg(allKnown))
	if out.Kind != UnitEnded || !cg.removed["t-1"] || len(cg.kills) == 0 {
		t.Fatalf("%+v kills=%v removed=%v", out, cg.kills, cg.removed)
	}
}

func TestEndUnitStragglerKeepsListAndIsNeverEnded(t *testing.T) {
	cg := newFakeCg()
	cg.subs["t-1"] = []*fakeProc{{pid: 12, stamp: "99"}} // survives SIGTERM and cgroup.kill (D state)
	out := EndUnit(cg, endCfg(allKnown))
	if out.Kind != UnitStragglers || len(out.Survivors) != 1 || out.Survivors[0].PID != 12 {
		t.Fatalf("%+v", out)
	}
}

func TestEndUnitUnknownMemberIsKilledButNotEnded(t *testing.T) {
	cg := newFakeCg()
	cg.subs["t-1"] = []*fakeProc{{pid: 13, dieOnTerm: true}}
	out := EndUnit(cg, endCfg(func(string, int, string) bool { return false }))
	if out.Kind != UnitEndedWithUnknown || len(out.Unknown) != 1 || !cg.removed["t-1"] {
		t.Fatalf("%+v", out)
	}
}

func TestEndUnitLateCreatorMakesRmdirRetry(t *testing.T) {
	cg := newFakeCg()
	cg.subs["t-1"] = []*fakeProc{{pid: 14, dieOnTerm: true}}
	cg.rmdirBusy["t-1"] = 2 // a late starter keeps making the cgroup busy
	out := EndUnit(cg, endCfg(allKnown))
	if out.Kind != UnitEnded || !cg.removed["t-1"] {
		t.Fatalf("%+v", out)
	}
}

func TestEndUnitExcludesBegunOperation(t *testing.T) {
	cg := newFakeCg()
	cg.subs["t-1"] = []*fakeProc{{pid: 20}}
	cg.subs["t-2"] = []*fakeProc{{pid: 21, dieOnTerm: true}}
	cfg := endCfg(allKnown)
	cfg.Exclude = map[string]bool{"t-1": true}
	out := EndUnit(cg, cfg)
	if out.Kind != UnitEnded || cg.removed["t-1"] || !cg.removed["t-2"] || len(cg.subs["t-1"]) != 1 {
		t.Fatalf("%+v", out)
	}
	for _, s := range cg.signals {
		if s == "t-1" {
			t.Fatal("an excluded sub-unit was signalled")
		}
	}
}

func newEnder(t *testing.T, k *fakeKernel, recs ...UnitRecord) UnitEnder {
	dir := newRunDir(t, nil)
	return UnitEnder{K: k, Dir: dir, Witness: func(uint64) []UnitRecord { return recs }, Cfg: endCfg(allKnown)}
}

// 13.2 P1: a unit present by name and UID but with no durable binding is reported, never signalled.
func TestSweepNeverSignalsAUnitBoundOnlyByNameAndUID(t *testing.T) {
	k := newFakeKernel()
	k.cg.subs["t-1"] = []*fakeProc{{pid: 77, dieOnTerm: true}}
	k.names = []string{testRecord().Name}
	u := newEnder(t, k) // no witness record, no run directory file
	out := u.End(bg, 7, nil)
	if out.Kind != UnitNotProven || !strings.Contains(out.Note, "no durable binding") {
		t.Fatalf("%+v", out)
	}
	if len(k.cg.signals)+len(k.cg.kills) != 0 || k.opened != 0 {
		t.Fatalf("a stranger was signalled: signals=%v kills=%v opened=%d", k.cg.signals, k.cg.kills, k.opened)
	}
	// the same after a once-owned name was removed and reused: a record exists but the id differs
	k2 := newFakeKernel()
	k2.cg.id.ID = 4242
	k2.cg.subs["t-1"] = []*fakeProc{{pid: 78, dieOnTerm: true}}
	k2.names = []string{testRecord().Name}
	out = newEnder(t, k2, testRecord()).End(bg, 7, nil)
	if out.Kind != UnitNotProven || len(k2.cg.signals)+len(k2.cg.kills) != 0 {
		t.Fatalf("name reuse: %+v signals=%v", out, k2.cg.signals)
	}
	// the unbound report used by recovery
	if got := UnboundUnits(k, nil); len(got) != 1 {
		t.Fatalf("%v", got)
	}
	if got := UnboundUnits(k, []UnitRecord{testRecord()}); len(got) != 0 {
		t.Fatalf("%v", got)
	}
}

func TestUnitEnderEndsBoundUnitAndReportsDisagreement(t *testing.T) {
	k := newFakeKernel()
	k.cg.subs["t-1"] = []*fakeProc{{pid: 30, dieOnTerm: true}}
	k.names = []string{testRecord().Name}
	u := newEnder(t, k, testRecord())
	// key-free copy that disagrees (another nonce) is also considered: it cannot be proven, the witness one is
	rec2 := testRecord()
	rec2.CgroupID = 1
	mustNoErr(t, u.Dir.WriteUnitFile(rec2))
	out := u.End(bg, 7, nil)
	if !strings.Contains(out.Note, "disagree") || !k.cg.removed["t-1"] {
		t.Fatalf("%+v removed=%v", out, k.cg.removed)
	}
	if out.Kind != UnitNotProven {
		t.Fatalf("a disagreeing unproven candidate keeps the run cleanup-pending: %v", out.Kind)
	}
}

func TestUnitEnderCLIUsesOnlyTheRunDirectoryFile(t *testing.T) {
	k := newFakeKernel()
	k.cg.subs["t-3"] = []*fakeProc{{pid: 31, dieOnTerm: true}}
	k.names = []string{testRecord().Name}
	dir := newRunDir(t, nil)
	mustNoErr(t, dir.WriteUnitFile(testRecord()))
	if _, err := os.Stat(dir.UnitPath(7)); err != nil {
		t.Fatal(err)
	}
	u := UnitEnder{K: k, Dir: dir, Cfg: endCfg(allKnown)} // no witness: no database, no key
	if out := u.End(bg, 7, nil); out.Kind != UnitEnded || !k.cg.removed["t-3"] {
		t.Fatalf("%+v", out)
	}
	// create-exclusive: a second write must not replace it
	if err := dir.WriteUnitFile(testRecord()); err == nil {
		t.Fatal("the unit file must be create-exclusive")
	}
}

func TestUnitEnderExcludeNamesTicketSubgroups(t *testing.T) {
	k := newFakeKernel()
	k.cg.subs["t-5"] = []*fakeProc{{pid: 40}}
	k.names = []string{testRecord().Name}
	u := newEnder(t, k, testRecord())
	out := u.End(bg, 7, []uint64{5})
	if out.Kind != UnitEnded || len(k.cg.subs["t-5"]) != 1 {
		t.Fatalf("%+v", out)
	}
}

func TestUnitEnderNoUnitIsNone(t *testing.T) {
	k := newFakeKernel()
	if out := newEnder(t, k).End(bg, 7, nil); out.Kind != UnitNone {
		t.Fatalf("%+v", out)
	}
}

// reboot: proven gone by P1, no signal.
func TestUnitEnderRebootIsGoneWithoutSignals(t *testing.T) {
	k := newFakeKernel()
	k.boot = "boot-2"
	k.cg.subs["t-1"] = []*fakeProc{{pid: 50}}
	out := newEnder(t, k, testRecord()).End(bg, 7, nil)
	if out.Kind != UnitEnded || len(k.cg.signals) != 0 {
		t.Fatalf("%+v", out)
	}
	_ = syscall.SIGTERM
}

// ----- the refusal to start where containment is unavailable -----

func TestProbeRefusesOffLinux(t *testing.T) {
	for _, goos := range []string{"freebsd", "openbsd"} {
		err := probe(goos, func() []ProbeStep { t.Fatal("linux steps consulted"); return nil })
		if !errors.Is(err, ErrContainmentUnavailable) || !strings.Contains(err.Error(), goos) {
			t.Fatalf("%s: %v", goos, err)
		}
	}
}

// macOS has a containment now (the session unit, KindSession), so it no longer belongs in the "no mechanism at all" list
// above. Here, off macOS, its mechanism has no API: the probe still refuses, now with the macOS wording instead of
// "darwin is not supported".
func TestProbeOfDarwinRefusesWithTheMacOSMechanismWording(t *testing.T) {
	// the mechanism with no API, whatever the host: on a real Mac the real one would answer instead
	swapContainment(t, KindSession, sessContainment{})
	err := probe("darwin", func() []ProbeStep { t.Fatal("linux steps consulted"); return nil })
	if !errors.Is(err, ErrContainmentUnavailable) || !strings.Contains(err.Error(), "macOS") {
		t.Fatalf("darwin: %v", err)
	}
}

func TestProbeNamesFirstFailingStepAndFailsClosed(t *testing.T) {
	ran := 0
	steps := []ProbeStep{
		{Name: "cgroup v2", Check: func() error { ran++; return nil }},
		{Name: "delegation", Check: func() error { ran++; return errors.New("not writable") }},
		{Name: "cgroup.kill", Check: func() error { ran++; return nil }},
	}
	err := probe("linux", func() []ProbeStep { return steps })
	if !errors.Is(err, ErrContainmentUnavailable) || !strings.Contains(err.Error(), "delegation") || ran != 2 {
		t.Fatalf("err %v ran %d", err, ran)
	}
	if err := probe("linux", func() []ProbeStep { return steps[:1] }); err != nil {
		t.Fatalf("all steps pass: %v", err)
	}
}

// On this machine the real probe either passes or refuses with the sentinel; it never returns another error.
func TestRealProbeIsAllOrRefusal(t *testing.T) {
	if err := Probe(); err != nil && !errors.Is(err, ErrContainmentUnavailable) {
		t.Fatalf("%v", err)
	}
}
