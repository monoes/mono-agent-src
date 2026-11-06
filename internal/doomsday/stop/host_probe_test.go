package stop

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"runtime"
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
	parent := shortTempDir(t)
	mustNoErr(t, pinSelfTest(parent, mkReal, defaultPinScenarios, pinRefusalExpected))
	emptyDir(t, parent)
}

// refused is never true here, so the step fails on a Windows runner too (where the system itself refuses every change):
// a blind pin cannot pass by way of "the system refused".
func TestPinSelfTestFailsWhenTheBindingDoesNotNoticeASwap(t *testing.T) {
	parent := shortTempDir(t)
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

// pathPinner is a binding with no event memory and no holds: it reports a swap only while a name leads elsewhere. It holds
// nothing, so on any host OS (Windows included, where a real pin's handles make the system refuse the change) the self-test's
// changes happen and only the binding's own reports decide.
type pathPinner struct{ ids map[string]FileID }

func (m pathPinner) Check() error {
	for path, id := range m.ids {
		if now, err := statID(path); err != nil || now != id {
			return fmt.Errorf("%w: path comparison", ErrPinSwapped)
		}
	}
	return nil
}

func (pathPinner) Do(context.Context, func(*sql.Conn, func() error) error) error { return nil }
func (pathPinner) discard()                                                      {}
func (pathPinner) closeSQLite()                                                  {}

func mkFake(_ context.Context, o PinOptions) (pinner, error) {
	ids := map[string]FileID{}
	for _, n := range []string{o.DBPath, o.DBPath + "-wal", o.DBPath + "-shm"} {
		if _, err := os.Stat(n); err != nil {
			if werr := os.WriteFile(n, nil, 0o600); werr != nil {
				return nil, werr
			}
		}
		id, err := statID(n)
		if err != nil {
			return nil, err
		}
		ids[n] = id
	}
	return pathPinner{ids}, nil
}

func TestPinSelfTestNeedsTheMemoryOfAnUndoneSwap(t *testing.T) {
	err := pinSelfTest(shortTempDir(t), mkFake, defaultPinScenarios, never)
	if err == nil || !strings.Contains(err.Error(), "away and back") {
		t.Fatalf("a binding with no event memory passed the undone-swap scenario: %v", err)
	}
}

func TestPinSelfTestAcceptsARefusalOnlyOfTheExpectedClass(t *testing.T) {
	sharing := errors.New("sharing violation")
	sc := []pinScenarioDef{{Name: "refused", Op: func(string, string, string) error { return sharing }}}
	is := func(e error) bool { return errors.Is(e, sharing) }
	mustNoErr(t, pinSelfTest(shortTempDir(t), mkFake, sc, is))
	if err := pinSelfTest(shortTempDir(t), mkFake, sc, never); err == nil {
		t.Fatal("an error of another class counted as the system refusing")
	}
	other := []pinScenarioDef{{Name: "not found", Op: func(string, string, string) error { return os.ErrNotExist }}}
	if err := pinSelfTest(shortTempDir(t), mkFake, other, is); err == nil {
		t.Fatal("a missing file counted as the system refusing")
	}
	// a refusal that nevertheless removed the database is not a refusal
	gone := []pinScenarioDef{{Name: "gone", Op: func(db, _, _ string) error { _ = os.Remove(db); return sharing }}}
	if err := pinSelfTest(shortTempDir(t), mkFake, gone, is); err == nil {
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
	if _, done, err := scratchDir(shortTempDir(t), ".x-"); err != nil {
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
		parent := shortTempDir(t)
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
	db, run := shortTempDir(t), shortTempDir(t)
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
	base := shortTempDir(t)
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

// ageSock ages a unix socket file. os.Chtimes on an AF_UNIX socket file fails on Windows ("The file cannot be accessed by the
// system"), so the tests that need an OLD socket cannot run there.
func ageSock(t *testing.T, path string, at time.Time) {
	t.Helper()
	if runtime.GOOS == "windows" {
		t.Skip("os.Chtimes fails on an AF_UNIX socket file on Windows")
	}
	mustNoErr(t, os.Chtimes(path, at, at))
}

// mkSock makes a real unix socket file at path that stays after the listener closes.
func mkSock(t *testing.T, path string) {
	t.Helper()
	ln, err := net.Listen("unix", path)
	mustNoErr(t, err)
	ln.(*net.UnixListener).SetUnlinkOnClose(false)
	mustNoErr(t, ln.Close())
}

func TestLeftoversOfAKilledSelfTestAreReapedOnlyWhenOurs(t *testing.T) {
	parent := shortTempDir(t)
	old := time.Now().Add(-time.Hour)
	age := func(name string, at time.Time) { ageSock(t, filepath.Join(parent, name), at) }
	dir := func(name string, at time.Time) {
		mustNoErr(t, os.Mkdir(filepath.Join(parent, name), 0o700))
		mustNoErr(t, os.WriteFile(filepath.Join(parent, name, "p.db"), nil, 0o600))
		age(name, at)
	}
	file := func(name string) {
		mustNoErr(t, os.WriteFile(filepath.Join(parent, name), nil, 0o600))
		age(name, old)
	}
	// ours and old: removed
	dir(".ma-pin-111", old)
	dir(".ma-pin-111.moved", old)
	dir(".ma-lock-222", old)
	mkSock(t, filepath.Join(parent, ".s123"))
	age(".s123", old)
	// ours but fresh: kept
	dir(".ma-pin-333", time.Now())
	mkSock(t, filepath.Join(parent, ".s456"))
	// not ours, whatever the age: kept
	dir("keep", old)
	dir(".ma-pin-abc", old) // not digits
	file(".ma-pinx")
	file(".s789") // a regular file with our name
	// sockets, old and owned by us, but not the name we generate
	mkSock(t, filepath.Join(parent, ".sabc"))
	mkSock(t, filepath.Join(parent, ".s1234"))
	age(".sabc", old)
	age(".s1234", old)
	mustNoErr(t, os.Symlink(filepath.Join(parent, "keep"), filepath.Join(parent, ".s111"))) // a link with our name
	_, err := probeParent(parent)
	mustNoErr(t, err)
	left := map[string]bool{}
	es, _ := os.ReadDir(parent)
	for _, e := range es {
		left[e.Name()] = true
	}
	want := []string{".ma-pin-333", ".s456", "keep", ".ma-pin-abc", ".ma-pinx", ".s789", ".sabc", ".s1234", ".s111"}
	if len(left) != len(want) {
		t.Fatalf("left: %v", left)
	}
	for _, w := range want {
		if !left[w] {
			t.Errorf("%s was removed", w)
		}
	}
}

func TestReaperLeavesWhatAnotherUserOwns(t *testing.T) {
	parent := shortTempDir(t)
	mkSock(t, filepath.Join(parent, ".s123"))
	mustNoErr(t, os.Mkdir(filepath.Join(parent, ".ma-pin-5"), 0o700))
	old := time.Now().Add(-time.Hour)
	for _, n := range []string{".s123", ".ma-pin-5"} {
		ageSock(t, filepath.Join(parent, n), old)
	}
	oldOwn := fileOwnedByMe
	t.Cleanup(func() { fileOwnedByMe = oldOwn })
	fileOwnedByMe = func(os.FileInfo) bool { return false }
	_, err := probeParent(parent)
	mustNoErr(t, err)
	if es, _ := os.ReadDir(parent); len(es) != 2 {
		t.Fatalf("a leftover of another owner was removed: %v", es)
	}
}

// Only the two directories the probe was given are swept, never an ancestor it fell back to.
func TestReaperDoesNotSweepAnAncestorTheProbeFellBackTo(t *testing.T) {
	base := shortTempDir(t)
	mkSock(t, filepath.Join(base, ".s123"))
	old := time.Now().Add(-time.Hour)
	ageSock(t, filepath.Join(base, ".s123"), old)
	got, err := probeParent(filepath.Join(base, "doomsday", "p1"))
	mustNoErr(t, err)
	if got != base {
		t.Fatalf("fell back to %s", got)
	}
	if _, err := os.Lstat(filepath.Join(base, ".s123")); err != nil {
		t.Fatal("the ancestor was swept")
	}
}

func TestPeerProbeSocketPathIsNoLongerThanTheRealOne(t *testing.T) {
	p := shortTempDir(t)
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
	parent := shortTempDir(t)
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
	mustNoErr(t, pinSelfTest(shortTempDir(t), func(ctx context.Context, o PinOptions) (pinner, error) {
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

// With age out of the way, a link that has our name and points at a socket is looked at as a link, never followed.
func TestReaperDoesNotFollowALinkWithOurName(t *testing.T) {
	parent, other := shortTempDir(t), shortTempDir(t)
	mkSock(t, filepath.Join(other, "real"))
	mustNoErr(t, os.Symlink(filepath.Join(other, "real"), filepath.Join(parent, ".s111")))
	oldAge := reapAge
	t.Cleanup(func() { reapAge = oldAge })
	reapAge = -time.Hour
	_, err := probeParent(parent)
	mustNoErr(t, err)
	if _, err := os.Lstat(filepath.Join(parent, ".s111")); err != nil {
		t.Fatalf("the link was removed: %v", err)
	}
	if _, err := os.Lstat(filepath.Join(other, "real")); err != nil {
		t.Fatalf("the target was removed: %v", err)
	}
}

// The self-test's listener (a transient unix socket in the run directory, listed in internal/accountdoor) serves nothing: it
// accepts the probe's own connection once, never reads or writes on it, and a second client that sends a request gets no
// answer. When the self-test ends the socket is gone.
func TestPeerSelfTestListenerServesNothingAndIsGoneAfterwards(t *testing.T) {
	parent := shortTempDir(t)
	var served int
	err := peerSelfTest(parent, func(c net.Conn) (uint32, error) {
		// the probe's client sent nothing
		_ = c.SetReadDeadline(time.Now().Add(100 * time.Millisecond))
		if n, _ := c.Read(make([]byte, 16)); n != 0 {
			served++
		}
		// a second client with a request line: accepted by the kernel's backlog at most, never served
		path := c.LocalAddr().String()
		o, derr := net.DialTimeout("unix", path, time.Second)
		if derr == nil {
			defer o.Close()
			_, _ = o.Write([]byte("STOP 1 deadbeef\n"))
			_ = o.SetReadDeadline(time.Now().Add(150 * time.Millisecond))
			if n, _ := o.Read(make([]byte, 16)); n != 0 {
				served++
			}
		}
		return uint32(os.Getuid()), nil
	})
	mustNoErr(t, err)
	if served != 0 {
		t.Fatalf("the self-test listener answered %d request(s)", served)
	}
	emptyDir(t, parent)
	if c, err := net.DialTimeout("unix", filepath.Join(parent, ".s000"), 100*time.Millisecond); err == nil {
		c.Close()
		t.Fatal("a socket is still listening after the self-test")
	}
}
