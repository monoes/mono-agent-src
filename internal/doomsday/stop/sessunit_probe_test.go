package stop

import (
	"os/exec"
	"strings"
	"syscall"
	"testing"
)

func TestFmtSessStampIsTheFormatParseSessStampReads(t *testing.T) {
	for _, c := range []struct {
		sec, us int64
		want    string
	}{{1_800_000_000, 5, "1800000000.000005"}, {1, 0, "1.000000"}, {1_800_000_000, 999_999, "1800000000.999999"}} {
		got := fmtSessStamp(c.sec, c.us)
		if got != c.want {
			t.Fatalf("%d.%d -> %q, want %q", c.sec, c.us, got, c.want)
		}
		if v, ok := parseSessStamp(got); !ok || v != c.sec*1_000_000+c.us {
			t.Fatalf("%q does not parse back: %d %v", got, v, ok)
		}
	}
}

func TestCheckSessCmdRequiresASessionLeaderThatJoinsNoGroup(t *testing.T) {
	mk := func(a *syscall.SysProcAttr) *exec.Cmd { c := exec.Command("true"); c.SysProcAttr = a; return c }
	if err := checkSessCmd(mk(&syscall.SysProcAttr{Setsid: true})); err != nil {
		t.Fatalf("a Setsid command was refused: %v", err)
	}
	for name, c := range map[string]*exec.Cmd{
		"nil":          nil,
		"no attr":      mk(nil),
		"no setsid":    mk(&syscall.SysProcAttr{}),
		"setpgid":      mk(&syscall.SysProcAttr{Setsid: true, Setpgid: true}),
		"pgid":         mk(&syscall.SysProcAttr{Setsid: true, Pgid: 77}),
		"setpgid only": mk(&syscall.SysProcAttr{Setpgid: true, Pgid: 77}),
	} {
		if err := checkSessCmd(c); err == nil {
			t.Fatalf("%s: a command that would not lead its own session was accepted", name)
		}
	}
}

func probeProcs() (self int, leader, plain, grp, own SessProc) {
	mk := func(pid, ppid, pgid, sid int) SessProc {
		return SessProc{PID: pid, PPID: ppid, PGID: pgid, SID: sid, Stamp: fmtSessStamp(1_800_000_000, int64(pid)), UID: 501}
	}
	return 10, mk(100, 10, 100, 100), mk(101, 100, 100, 100), mk(102, 100, 102, 100), mk(103, 100, 103, 103)
}

func TestSessProbeShapeAcceptsTheAssumedBSDRules(t *testing.T) {
	self, l, p, g, o := probeProcs()
	if err := sessProbeShape(self, l, p, g, o); err != nil {
		t.Fatal(err)
	}
}

// Each assumption has its own refusal: a machine where one fails must say which.
func TestSessProbeShapeRefusesEachBrokenAssumption(t *testing.T) {
	for name, c := range map[string]struct {
		mut  func(l, p, g, o *SessProc)
		want string
	}{
		"session is not the leader pid":  {func(l, _, _, _ *SessProc) { l.SID = 7 }, "not its own number"},
		"leader group differs":           {func(l, _, _, _ *SessProc) { l.PGID = 7 }, "process group"},
		"leader parent is not us":        {func(l, _, _, _ *SessProc) { l.PPID = 1 }, "parent"},
		"plain child left the session":   {func(_, p, _, _ *SessProc) { p.SID = 5 }, "plain child"},
		"setpgid child left the session": {func(_, _, g, _ *SessProc) { g.SID = g.PID }, "outlive job control"},
		"setpgid child kept the group":   {func(_, _, g, _ *SessProc) { g.PGID = 100 }, "outlive job control"},
		"setsid child kept the session":  {func(_, _, _, o *SessProc) { o.SID = 100 }, "own session"},
		"unusable stamp":                 {func(_, p, _, _ *SessProc) { p.Stamp = "x" }, "start time"},
		"another uid":                    {func(_, _, _, o *SessProc) { o.UID = 0 }, "uid"},
	} {
		self, l, p, g, o := probeProcs()
		c.mut(&l, &p, &g, &o)
		err := sessProbeShape(self, l, p, g, o)
		if err == nil || !strings.Contains(err.Error(), c.want) {
			t.Fatalf("%s: %v (want %q)", name, err, c.want)
		}
	}
}

func TestSessProbeFindsTiesEveryProbeProcessToTheLeader(t *testing.T) {
	_, l, p, g, o := probeProcs()
	scan := []SessProc{{PID: 1, PPID: 0, PGID: 1, SID: 1, Stamp: "1.000001"}, l, p, g, o}
	if err := sessProbeFinds(scan, l, l, p, g, o); err != nil {
		t.Fatal(err)
	}
	if err := sessProbeFinds([]SessProc{l, p, g}, l, l, p, g, o); err == nil || !strings.Contains(err.Error(), "does not list") {
		t.Fatalf("a process the scan lacks was accepted: %v", err)
	}
	other := p
	other.Stamp = fmtSessStamp(1_800_000_000, 999)
	if err := sessProbeFinds([]SessProc{l, other, g, o}, l, l, p, g, o); err == nil || !strings.Contains(err.Error(), "start") {
		t.Fatalf("a stamp that differs between the scan and a single read was accepted: %v", err)
	}
	// a setsid child that is no child of the leader is not found: the closure is the parent chain
	lost := o
	lost.PPID = 1
	if err := sessProbeFinds([]SessProc{l, p, g, lost}, l, l, p, g, lost); err == nil || !strings.Contains(err.Error(), "not found as a member") {
		t.Fatalf("an orphaned own-session process was found: %v", err)
	}
}

// Today macOS still starts the process-group unit, so no darwin end reads or writes the witness for a seal; the day
// platformKinds["darwin"] names the session unit (S3/S4) the seal is live on darwin with nothing else to change. Both halves.
func TestDarwinSealsOnlyOnceItsUnitKindIsTheSessionUnit(t *testing.T) {
	endAs := func(t *testing.T) int {
		hostAs(t, "darwin")
		r := newSeqRig(t)
		mustNoErr(t, r.seq.Bound(UnitRecord{Kind: KindSession, Generation: 3, CgroupID: 42, Name: "u"}))
		NewWitnessEnder(endStub{func() UnitOutcome { return UnitOutcome{Kind: UnitBestEffort} }}, r.seq).End(bg, 3, nil)
		recs, _ := r.seq.W.Records()
		n := 0
		for _, rec := range recs {
			if rec.T == RecUnitSealed {
				n++
			}
		}
		return n
	}
	t.Run("today darwin names the process-group unit: no seal", func(t *testing.T) {
		if platformKinds["darwin"] != KindPGroup {
			t.Fatalf("platformKinds[darwin] = %q: S2 flips nothing; whoever flips it updates this test", platformKinds["darwin"])
		}
		if n := endAs(t); n != 0 {
			t.Fatalf("%d seal lines on a host that runs no session unit", n)
		}
	})
	t.Run("darwin naming the session unit: sealed once", func(t *testing.T) {
		platformKindsAs(t, "darwin", KindSession)
		if n := endAs(t); n != 1 {
			t.Fatalf("%d seal lines on a darwin session host", n)
		}
	})
}
