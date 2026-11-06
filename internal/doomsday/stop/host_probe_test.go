package stop

import (
	"context"
	"errors"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func withHostChecks(t *testing.T, h hostChecks) {
	t.Helper()
	old := hostChecksNow
	hostChecksNow = func() hostChecks { return h }
	t.Cleanup(func() { hostChecksNow = old })
}

func okCheck() error { return nil }

var hostStepNames = []string{"host and witness lock", "peer credentials of the emergency channel", "pinned database binding"}

// The portable guard that turns red when a host step is deleted, renamed, or left without an implementation: the
// mode may start on Windows and macOS only while all three exist and pass.
func TestHostStepsExistUnderTheirNamesAndFailWithoutAnImplementation(t *testing.T) {
	steps := hostProbeSteps(hostChecks{})
	if len(steps) != 3 {
		t.Fatalf("host steps: %d", len(steps))
	}
	for i, s := range steps {
		if s.Name != hostStepNames[i] {
			t.Errorf("step %d is %q", i, s.Name)
		}
		if s.Check() == nil {
			t.Errorf("step %q passes without an implementation", s.Name)
		}
	}
	for _, steps := range [][]ProbeStep{windowsHostSteps(), macosHostSteps()} {
		if len(steps) != 3 {
			t.Fatalf("a platform lists %d host steps", len(steps))
		}
	}
}

func TestDefaultHostChecksAreAllImplemented(t *testing.T) {
	h := hostChecksNow()
	if h.Lock == nil || h.Peer == nil || h.Pin == nil {
		t.Fatalf("a host check is missing: %+v", []bool{h.Lock != nil, h.Peer != nil, h.Pin != nil})
	}
}

func TestWindowsProbeNeedsEveryHostStep(t *testing.T) {
	for _, missing := range hostStepNames {
		w := newFakeWin()
		h := hostChecks{Lock: okCheck, Peer: okCheck, Pin: okCheck}
		switch missing {
		case hostStepNames[0]:
			h.Lock = nil
		case hostStepNames[1]:
			h.Peer = nil
		default:
			h.Pin = nil
		}
		withHostChecks(t, h)
		err := jobContainment{os: w}.Probe()
		if !errors.Is(err, ErrContainmentUnavailable) || !strings.Contains(err.Error(), missing) {
			t.Errorf("missing %q: %v", missing, err)
		}
		for _, other := range hostStepNames {
			if other != missing && strings.Contains(err.Error(), other) {
				t.Errorf("missing %q: the refusal also names %q", missing, other)
			}
		}
	}
	withHostChecks(t, hostChecks{Lock: okCheck, Peer: okCheck, Pin: okCheck})
	if err := (jobContainment{os: newFakeWin()}).Probe(); err != nil {
		t.Fatalf("a Windows probe with every host step passing refuses: %v", err)
	}
	hostAs(t, "windows")
	swapContainment(t, KindJobObject, jobContainment{os: newFakeWin()})
	if err := probe("windows", nil); err != nil {
		t.Fatalf("Probe on a Windows host with every step passing: %v", err)
	}
	withHostChecks(t, hostChecks{Lock: okCheck, Peer: okCheck})
	if err := probe("windows", nil); !errors.Is(err, ErrContainmentUnavailable) {
		t.Fatalf("Probe on a Windows host without the pin passes: %v", err)
	}
}

func TestMacOSProbeNeedsEveryHostStep(t *testing.T) {
	for _, missing := range hostStepNames {
		w := newFakePG()
		h := hostChecks{Lock: okCheck, Peer: okCheck, Pin: okCheck}
		switch missing {
		case hostStepNames[0]:
			h.Lock = nil
		case hostStepNames[1]:
			h.Peer = nil
		default:
			h.Pin = nil
		}
		withHostChecks(t, h)
		err := pgContainment{os: w}.Probe()
		if !errors.Is(err, ErrContainmentUnavailable) || !strings.Contains(err.Error(), missing) {
			t.Errorf("missing %q: %v", missing, err)
		}
		if len(w.procs) != 1 {
			t.Errorf("the probe left a process behind: %d", len(w.procs))
		}
	}
	withHostChecks(t, hostChecks{Lock: okCheck, Peer: okCheck, Pin: okCheck})
	if err := (pgContainment{os: newFakePG()}).Probe(); err != nil {
		t.Fatalf("a macOS probe with every host step passing refuses: %v", err)
	}
	hostAs(t, "darwin")
	swapContainment(t, KindPGroup, pgContainment{os: newFakePG()})
	withHostChecks(t, hostChecks{Lock: okCheck, Peer: okCheck})
	if err := probe("darwin", nil); !errors.Is(err, ErrContainmentUnavailable) {
		t.Fatalf("Probe on a macOS host without the pin passes: %v", err)
	}
}

// ----- the self-tests themselves (run for real on this OS, and against fakes that must make them fail) -----

func TestRealHostSelfTestsPassHere(t *testing.T) {
	h := hostChecksNow()
	for name, f := range map[string]func() error{"lock": h.Lock, "peer": h.Peer, "pin": h.Pin} {
		if err := f(); err != nil {
			t.Errorf("%s: %v", name, err)
		}
	}
}

func TestLockSelfTestFailsWhenASecondHostIsNotRefused(t *testing.T) {
	if err := lockSelfTest("", func(RunDir) (func(), error) { return func() {}, nil }); err == nil {
		t.Fatal("a lock that never refuses passed")
	}
	if err := lockSelfTest("", func(RunDir) (func(), error) { return nil, errors.New("denied") }); err == nil {
		t.Fatal("a lock that cannot be taken passed")
	}
}

func TestPeerSelfTestFailsOnAnotherUserOrAnError(t *testing.T) {
	if err := peerSelfTest("", func(net.Conn) (uint32, error) { return 0xfffffff0, nil }); err == nil {
		t.Fatal("a peer identified as someone else passed")
	}
	if err := peerSelfTest("", func(net.Conn) (uint32, error) { return 0, errors.New("no creds") }); err == nil {
		t.Fatal("a peer that cannot be identified passed")
	}
}

// blindPinner pins for real but reports nothing: a binding that does not detect a swap.
type blindPinner struct{ pinner }

func (blindPinner) Check() error { return nil }

func mkReal(ctx context.Context, o PinOptions) (pinner, error) { return realPinner(ctx, o) }

func never(error) bool { return false }

func emptyDir(t *testing.T, dir string) {
	t.Helper()
	if es, err := os.ReadDir(dir); err != nil || len(es) != 0 {
		t.Fatalf("scratch left behind in %s: %v %v", dir, es, err)
	}
}

func TestPinSelfTestPassesHereAndLeavesNothingNextToTheDatabase(t *testing.T) {
	parent := t.TempDir()
	mustNoErr(t, pinSelfTest(parent, mkReal, defaultPinScenarios, pinRefusalExpected))
	emptyDir(t, parent)
}

// refused is never true here, so the step fails on a Windows runner too (where the system itself refuses every change):
// a blind pin cannot pass by way of "the system refused".
func TestPinSelfTestFailsWhenTheBindingDoesNotNoticeASwap(t *testing.T) {
	parent := t.TempDir()
	err := pinSelfTest(parent, func(ctx context.Context, o PinOptions) (pinner, error) {
		p, err := realPinner(ctx, o)
		if err != nil {
			return nil, err
		}
		return blindPinner{p}, nil
	}, defaultPinScenarios, never)
	if err == nil {
		t.Fatal("a pin that never reports a swap passed")
	}
	emptyDir(t, parent) // also nothing left after a failure
	if err := pinSelfTest(parent, func(context.Context, PinOptions) (pinner, error) { return nil, errors.New("no pin") }, defaultPinScenarios, never); err == nil {
		t.Fatal("a pin that cannot be made passed")
	}
	emptyDir(t, parent)
}

// memorylessPinner reports a swap only while a path leads elsewhere: a comparison of names with no event memory.
type memorylessPinner struct {
	pinner
	ids map[string]FileID
}

func (m memorylessPinner) Check() error {
	for path, id := range m.ids {
		if now, err := statID(path); err != nil || now != id {
			return fmt.Errorf("%w: path comparison", ErrPinSwapped)
		}
	}
	return nil
}

func TestPinSelfTestNeedsTheMemoryOfAnUndoneSwap(t *testing.T) {
	err := pinSelfTest(t.TempDir(), func(ctx context.Context, o PinOptions) (pinner, error) {
		p, err := realPinner(ctx, o)
		if err != nil {
			return nil, err
		}
		ids := map[string]FileID{}
		for _, n := range []string{o.DBPath, o.DBPath + "-wal", o.DBPath + "-shm"} {
			ids[n], _ = statID(n)
		}
		return memorylessPinner{p, ids}, nil
	}, defaultPinScenarios, never)
	if err == nil || !strings.Contains(err.Error(), "away and back") {
		t.Fatalf("a binding with no event memory passed the undone-swap scenario: %v", err)
	}
}

func TestPinSelfTestAcceptsARefusalOnlyOfTheExpectedClass(t *testing.T) {
	sharing := errors.New("sharing violation")
	sc := []pinScenarioDef{{Name: "refused", Op: func(string, string, string) error { return sharing }}}
	is := func(e error) bool { return errors.Is(e, sharing) }
	mustNoErr(t, pinSelfTest(t.TempDir(), mkReal, sc, is))
	if err := pinSelfTest(t.TempDir(), mkReal, sc, never); err == nil {
		t.Fatal("an error of another class counted as the system refusing")
	}
	other := []pinScenarioDef{{Name: "not found", Op: func(string, string, string) error { return os.ErrNotExist }}}
	if err := pinSelfTest(t.TempDir(), mkReal, other, is); err == nil {
		t.Fatal("a missing file counted as the system refusing")
	}
	// a refusal that nevertheless removed the database is not a refusal
	gone := []pinScenarioDef{{Name: "gone", Op: func(db, _, _ string) error { _ = os.Remove(db); return sharing }}}
	if err := pinSelfTest(t.TempDir(), mkReal, gone, is); err == nil {
		t.Fatal("a refusal that left the database missing passed")
	}
}

func TestVolumeRuleRefusesWhatShareModesAndKqueueCannotBeTrustedOn(t *testing.T) {
	cases := []struct {
		goos, fs string
		remote   bool
		ok       bool
	}{
		{"windows", "NTFS", false, true}, {"windows", "ntfs", false, true},
		{"windows", "NTFS", true, false}, {"windows", "ReFS", false, false}, {"windows", "FAT32", false, false},
		{"windows", "exFAT", false, false}, {"windows", "", false, false},
		{"darwin", "apfs", false, true}, {"darwin", "hfs", false, true},
		{"darwin", "apfs", true, false}, {"darwin", "smbfs", true, false}, {"darwin", "nfs", true, false},
		{"darwin", "msdos", false, false}, {"darwin", "exfat", false, false}, {"darwin", "", false, false},
		{"linux", "nfs", true, true},
	}
	for _, c := range cases {
		if err := volumeAllowed(c.goos, c.fs, c.remote); (err == nil) != c.ok {
			t.Errorf("%s %q remote=%v: %v", c.goos, c.fs, c.remote, err)
		}
	}
}

func TestScratchDirAcceptsAnOrdinaryTempDir(t *testing.T) {
	// the real check on this OS passes for an ordinary temp dir
	if _, done, err := scratchDir(t.TempDir(), ".x-"); err != nil {
		t.Fatal(err)
	} else {
		done()
	}
}

type dirsContainment struct {
	recContainment
	seen probeDirs
}

func (d *dirsContainment) Probe() error { d.seen = currentProbeDirs(); return nil }

func TestProbeAtRunsTheSelfTestsInTheGivenDirectoriesAndOnlyThere(t *testing.T) {
	d := &dirsContainment{}
	swapContainment(t, KindCgroup2, d)
	mustNoErr(t, ProbeAt("/db/dir", "/run/dir"))
	if d.seen.DB != "/db/dir" || d.seen.Run != "/run/dir" {
		t.Fatalf("the self-tests would run in %+v", d.seen)
	}
	if cur := currentProbeDirs(); cur != (probeDirs{}) {
		t.Fatalf("the directories outlived the probe: %+v", cur)
	}
}

// ----- the guards that used to pass when reverted -----

func withVolume(t *testing.T, goos string, f func(string) (string, bool, error)) {
	t.Helper()
	oc, og := volumeClassFn, volumeGOOS
	volumeClassFn, volumeGOOS = f, goos
	t.Cleanup(func() { volumeClassFn, volumeGOOS = oc, og })
}

func TestScratchDirIsRefusedOnAVolumeTheRuleRefusesAndOnAnError(t *testing.T) {
	cases := map[string]struct {
		fs     string
		remote bool
		err    error
		ok     bool
	}{
		"local ntfs":  {"NTFS", false, nil, true},
		"network":     {"NTFS", true, nil, false},
		"fat":         {"FAT32", false, nil, false},
		"unknown":     {"", false, nil, false},
		"call failed": {"NTFS", false, errors.New("access denied"), false},
	}
	for name, c := range cases {
		c := c
		withVolume(t, "windows", func(string) (string, bool, error) { return c.fs, c.remote, c.err })
		parent := t.TempDir()
		_, done, err := scratchDir(parent, ".ma-pin-")
		if (err == nil) != c.ok {
			t.Errorf("%s: %v", name, err)
		}
		if err == nil {
			done()
		}
		if !c.ok {
			emptyDir(t, parent) // refused before anything was made
		}
	}
}

func TestBareProbeRefusesOnWindowsAndMacOSAndLinuxIsUnchanged(t *testing.T) {
	for _, goos := range []string{"windows", "darwin"} {
		hostAs(t, goos)
		h := hostChecksNow()
		for name, f := range map[string]func() error{"lock": h.Lock, "peer": h.Peer, "pin": h.Pin} {
			if err := f(); !errors.Is(err, errNoProbeDirs) {
				t.Errorf("%s %s: %v", goos, name, err)
			}
		}
	}
	hostAs(t, "linux")
	if h := hostChecksNow(); h.Lock == nil || h.Pin == nil || h.Peer == nil {
		t.Fatal("linux checks missing")
	}
}

func withDirs(t *testing.T, d probeDirs) {
	t.Helper()
	probeCurM.Lock()
	old := probeCur
	probeCur = d
	probeCurM.Unlock()
	t.Cleanup(func() { probeCurM.Lock(); probeCur = old; probeCurM.Unlock() })
}

// The self-tests run where they were told to: lock and peer in the run directory, the pin next to the database, and
// never in the system temp directory.
func TestSelfTestsRunNextToTheGivenDirectories(t *testing.T) {
	db, run := t.TempDir(), t.TempDir()
	withDirs(t, probeDirs{DB: db, Run: run})
	var seen []string
	withVolume(t, "linux", func(p string) (string, bool, error) { seen = append(seen, p); return "", false, nil })
	h := hostChecksNow()
	mustNoErr(t, h.Lock())
	mustNoErr(t, h.Peer())
	mustNoErr(t, h.Pin())
	// lock, peer: run; pin: db (one look per scenario)
	if seen[0] != run || seen[1] != run {
		t.Fatalf("lock and peer ran in %v, not in the run directory %s", seen[:2], run)
	}
	for _, p := range seen[2:] {
		if p != db {
			t.Fatalf("the pin ran in %s, not next to the database %s", p, db)
		}
	}
	emptyDir(t, db)
	emptyDir(t, run)
}

func TestPinScenariosAreTheExactSet(t *testing.T) {
	var names []string
	for _, s := range defaultPinScenarios {
		names = append(names, s.Name)
	}
	want := []string{"moving the database away", "replacing the database by another file", "removing the database",
		"moving the -wal file away", "removing the -shm file", "renaming the database directory", "moving the database away and back"}
	if strings.Join(names, "|") != strings.Join(want, "|") {
		t.Fatalf("scenarios: %v", names)
	}
}

func TestShowProbeDoesNotCreateTheRunDirectory(t *testing.T) {
	base := t.TempDir()
	run := filepath.Join(base, "doomsday", "p1")
	withDirs(t, probeDirs{DB: base, Run: run})
	h := hostChecksNow()
	mustNoErr(t, h.Lock())
	mustNoErr(t, h.Peer())
	if _, err := os.Stat(filepath.Join(base, "doomsday")); err == nil {
		t.Fatal("the probe created the run directory")
	}
	emptyDir(t, base)
}

func TestLeftoversOfAKilledSelfTestAreReapedOnlyWhenOld(t *testing.T) {
	parent := t.TempDir()
	old := time.Now().Add(-time.Hour)
	mk := func(name string, age time.Time, dir bool) {
		p := filepath.Join(parent, name)
		if dir {
			mustNoErr(t, os.Mkdir(p, 0o700))
			mustNoErr(t, os.WriteFile(filepath.Join(p, "p.db"), nil, 0o600))
		} else {
			mustNoErr(t, os.WriteFile(p, nil, 0o600))
		}
		mustNoErr(t, os.Chtimes(p, age, age))
	}
	mk(".ma-pin-111", old, true)
	mk(".ma-pin-111.moved", old, true)
	mk(".ma-lock-222", old, true)
	mk(".sabc", old, false)
	mk(".ma-pin-fresh", time.Now(), true)
	mk(".sfff", time.Now(), false)
	mk("keep", old, true)
	mk(".ma-pinx", old, false) // a file with a similar name: not ours
	_, err := probeParent(parent)
	mustNoErr(t, err)
	left := map[string]bool{}
	es, _ := os.ReadDir(parent)
	for _, e := range es {
		left[e.Name()] = true
	}
	if len(left) != 4 || !left[".ma-pin-fresh"] || !left[".sfff"] || !left["keep"] || !left[".ma-pinx"] {
		t.Fatalf("left: %v", left)
	}
}

func TestPeerProbeSocketPathIsNoLongerThanTheRealOne(t *testing.T) {
	p := t.TempDir()
	for i := 0; i < 50; i++ {
		if got, real := peerSockPath(p), (RunDir{Dir: p}).SockPath(1); len(got) > len(real) {
			t.Fatalf("%s (%d) is longer than the real %s (%d)", got, len(got), real, len(real))
		}
	}
}

func TestPinSelfTestRunsAControlOperationAndRefusesWhenItFails(t *testing.T) {
	old := pinControl
	t.Cleanup(func() { pinControl = old })
	ran := 0
	pinControl = func(dir string) error { ran++; return old(dir) }
	parent := t.TempDir()
	mustNoErr(t, pinSelfTest(parent, mkReal, defaultPinScenarios, pinRefusalExpected))
	if ran != len(defaultPinScenarios) {
		t.Fatalf("control ran %d times", ran)
	}
	pinControl = func(string) error { return errors.New("sharing violation") }
	if err := pinSelfTest(parent, mkReal, defaultPinScenarios, pinRefusalExpected); err == nil || !strings.Contains(err.Error(), "control") {
		t.Fatalf("a failing control passed: %v", err)
	}
	emptyDir(t, parent)
}

func TestPinVariantsAddTheHostOnlyRunOnWindowsOnly(t *testing.T) {
	if v := pinVariants("windows"); len(v) != 2 || v[0] || !v[1] {
		t.Fatalf("windows: %v", v)
	}
	for _, g := range []string{"linux", "darwin"} {
		if v := pinVariants(g); len(v) != 1 || v[0] {
			t.Fatalf("%s: %v", g, v)
		}
	}
}

// closeRecorder reports the swap checks as quiet and records that SQLite was closed in the host-only variant.
type closeRecorder struct {
	pinner
	closed *int
}

func (c closeRecorder) closeSQLite() { *c.closed++ }
func (c closeRecorder) Check() error { return nil }

func TestPinSelfTestHostOnlyVariantClosesSQLiteBeforeTheChange(t *testing.T) {
	old := pinVariantsNow
	t.Cleanup(func() { pinVariantsNow = old })
	pinVariantsNow = func() []bool { return []bool{false, true} }
	closed := 0
	sharing := errors.New("sharing violation")
	sc := []pinScenarioDef{{Name: "refused", Op: func(string, string, string) error { return sharing }}}
	mustNoErr(t, pinSelfTest(t.TempDir(), func(ctx context.Context, o PinOptions) (pinner, error) {
		p, err := realPinner(ctx, o)
		if err != nil {
			return nil, err
		}
		return closeRecorder{p, &closed}, nil
	}, sc, func(e error) bool { return errors.Is(e, sharing) }))
	if closed != 1 {
		t.Fatalf("SQLite was closed %d times", closed)
	}
}
