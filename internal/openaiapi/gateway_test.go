package openaiapi

import (
	"context"
	"errors"
	"github.com/monoes/mono-agent/internal/testhome"
	"os"
	"path/filepath"
	"reflect"
	"regexp"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/monoes/mono-agent/internal/monomind"
)

func TestNewRequiresItsDependencies(t *testing.T) {
	good := func(t *testing.T) (Deps, Config) {
		h := newHarness(t, okTurn("x"))
		return h.g.deps, Config{ScratchRoot: t.TempDir()}
	}
	for name, drop := range map[string]func(*Deps){
		"keys":           func(d *Deps) { d.Keys = nil },
		"exec":           func(d *Deps) { d.Exec = nil },
		"bin":            func(d *Deps) { d.Bin = nil },
		"catalog scan":   func(d *Deps) { d.Catalog.Scan = nil },
		"catalog models": func(d *Deps) { d.Catalog.Models = nil },
	} {
		d, c := good(t)
		drop(&d)
		if _, err := New(d, c); err == nil {
			t.Errorf("New accepted a gateway without %s", name)
		}
	}
	d, c := good(t)
	if _, err := New(d, c); err != nil {
		t.Fatalf("New with every dependency: %v", err)
	}
}

// A profile's folders are named by a hash of its id: the id is an arbitrary
// string and must never steer a path.
func TestProfileFolderIsStableDistinctAndSafe(t *testing.T) {
	alice, again, bob := profileFolder("alice"), profileFolder(strings.Clone("alice")), profileFolder("bob")
	if alice != again || alice == bob {
		t.Errorf("a profile must always get the same folder and two profiles different ones: %q %q %q", alice, again, bob)
	}
	shape := regexp.MustCompile(`^p-[0-9a-f]{16}$`)
	for _, id := range []string{"alice", "", "..", ".", "../../etc", "a/b", `a\b`, "with space", "ünï", strings.Repeat("x", 500)} {
		if f := profileFolder(id); !shape.MatchString(f) {
			t.Errorf("profileFolder(%q) = %q: a profile id must never reach a path as such", id, f)
		}
	}
}

func TestNewEmptiesLeftoverSlotFoldersAndNothingElse(t *testing.T) {
	root := t.TempDir()
	profile := filepath.Join(root, profileFolder("alice"))
	inSlot, aboveLimit := filepath.Join(profile, "slot-1"), filepath.Join(profile, "slot-9")
	notASlot, notAProfile := filepath.Join(profile, "keep-me"), filepath.Join(root, "keep-me")
	foreignSlot := filepath.Join(notAProfile, "slot-1") // a slot-looking folder in a folder that is not a profile's
	for _, d := range []string{inSlot, aboveLimit, notASlot, notAProfile, foreignSlot} {
		if err := os.MkdirAll(filepath.Join(d, "sub"), 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(d, "left-by-a-crash.txt"), []byte("x"), 0o600); err != nil {
			t.Fatal(err)
		}
	}

	_ = newHarness(t, okTurn("x"), func(_ *Deps, c *Config) { c.ScratchRoot, c.MaxConcurrent = root, 4 })
	if entries, err := os.ReadDir(inSlot); err != nil || len(entries) != 0 {
		t.Errorf("a slot folder must be emptied and kept: %d entries, %v", len(entries), err)
	}
	if _, err := os.Stat(aboveLimit); !os.IsNotExist(err) {
		t.Error("the folder of a slot above the limit must be removed")
	}
	for _, untouched := range []string{notASlot, foreignSlot} {
		if entries, _ := os.ReadDir(untouched); len(entries) != 2 {
			t.Errorf("only slot-* folders inside a profile folder may be touched: %s", untouched)
		}
	}
	if entries, _ := os.ReadDir(notAProfile); len(entries) != 3 { // sub, the file, slot-1
		t.Errorf("a folder that is not a profile's must be left alone: %d entries", len(entries))
	}
}

// emptyDir opens a directory a runtime made read-only before removing what is
// inside it, and never follows a symlink out of the folder it is emptying.
func TestEmptyDirOpensUpReadOnlyDirectoriesAndStaysInside(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("root can open any directory: nothing about read-only ones is tested")
	}
	root := t.TempDir()
	dir := filepath.Join(root, "slot")
	outside := filepath.Join(root, "outside")
	for _, d := range []string{filepath.Join(dir, "a", "b"), outside} {
		if err := os.MkdirAll(d, 0o700); err != nil {
			t.Fatal(err)
		}
	}
	precious := filepath.Join(outside, "precious.txt")
	for _, f := range []string{filepath.Join(dir, "a", "b", "f"), filepath.Join(dir, "top"), precious} {
		if err := os.WriteFile(f, []byte("x"), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.Symlink(outside, filepath.Join(dir, "link-out")); err != nil {
		t.Skipf("symlinks are not available here: %v", err)
	}
	for _, d := range []string{filepath.Join(dir, "a", "b"), filepath.Join(dir, "a"), dir} { // deepest first
		if err := os.Chmod(d, 0o500); err != nil {
			t.Fatal(err)
		}
	}
	t.Cleanup(func() {
		for _, d := range []string{dir, filepath.Join(dir, "a"), filepath.Join(dir, "a", "b")} {
			_ = os.Chmod(d, 0o700)
		}
	})

	if !emptyDir(dir) {
		t.Fatal("a folder with read-only directories must still be emptied")
	}
	if entries, _ := os.ReadDir(dir); len(entries) != 0 {
		t.Errorf("%d entries left", len(entries))
	}
	if _, err := os.Stat(precious); err != nil {
		t.Errorf("emptyDir followed a symlink out of the folder: %v", err)
	}
}

// monomind.ListModels answers a failed listing with a built-in list and no
// error, which would stay in the catalog for a whole TTL under ids the runtime
// does not list. The production catalog asks for the strict variant, which says
// when the list is only standing in.
func TestDefaultDepsListModelsStrictly(t *testing.T) {
	deps := DefaultDeps(nil, "test")
	if reflect.ValueOf(deps.Catalog.Models).Pointer() != reflect.ValueOf(monomind.ListModelsStrict).Pointer() {
		t.Error("the catalog must list models with monomind.ListModelsStrict")
	}
}

// A turn in a sandbox that only confines writes below its folder can still remove
// that folder and put a link in its place (macOS's sandbox allows it: checked with
// a Seatbelt profile). Emptying what the link points to would wipe a directory the
// turn has no right to touch, as the OS user: a link is not a folder to empty.
func TestEmptyDirRefusesALinkWhereTheFolderShouldBe(t *testing.T) {
	root := t.TempDir()
	outside := filepath.Join(root, "outside")
	if err := os.MkdirAll(outside, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(outside, 0o755); err != nil { // the mode a followed link would change
		t.Fatal(err)
	}
	precious := filepath.Join(outside, "precious.txt")
	if err := os.WriteFile(precious, []byte("keep"), 0o600); err != nil {
		t.Fatal(err)
	}
	slot := filepath.Join(root, "slot")
	if err := os.Symlink(outside, slot); err != nil {
		t.Skipf("symlinks are not available here: %v", err)
	}

	if emptyDir(slot) {
		t.Error("a link is not a folder that was emptied")
	}
	if fi, err := os.Stat(outside); err != nil || fi.Mode().Perm() != 0o755 {
		t.Errorf("the chmod followed the link: %v %v", fi, err)
	}
	if _, err := os.Stat(precious); err != nil {
		t.Errorf("what is behind the link was removed: %v", err)
	}
}

// A link between the check and the open: a root handle follows a link that stays
// inside its parent, so the folder opened could be another slot's, which a turn
// of the same profile may be running in. What was opened is compared with what
// was looked at.
func TestEmptyDirChecksThatWhatItOpenedIsWhatItLookedAt(t *testing.T) {
	parent := t.TempDir()
	slot0, slot1 := filepath.Join(parent, "slot-0"), filepath.Join(parent, "slot-1")
	for _, d := range []string{slot0, slot1} {
		if err := os.MkdirAll(d, 0o700); err != nil {
			t.Fatal(err)
		}
	}
	running := filepath.Join(slot1, "running.txt") // what a turn in the other slot is working with
	if err := os.WriteFile(running, []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	swapped := false
	afterLstatHook.set(func() {
		if swapped {
			return
		}
		swapped = true
		_ = os.RemoveAll(slot0)
		if err := os.Symlink("slot-1", slot0); err != nil { // a link inside the parent
			t.Error(err)
		}
	})
	t.Cleanup(func() { afterLstatHook.set(nil) })

	if emptyDir(slot0) {
		t.Error("the folder it opened was not the one it looked at: it must say it could not empty it")
	}
	if !swapped {
		t.Fatal("the hook never ran: the test does not exercise the race")
	}
	if _, err := os.Stat(running); err != nil {
		t.Errorf("the other slot's folder was emptied through the link: %v", err)
	}
}

// A process can outlive its turn and keep changing the tree while it is emptied.
// A directory swapped for a link between the listing and the chmod must not send
// the chmod, the listing or a removal outside the folder: the gateway acts on
// the folder's files through an open handle, not by path.
func TestEmptyDirSurvivesALinkPlantedWhileItWalks(t *testing.T) {
	root := t.TempDir()
	dir := filepath.Join(root, "slot")
	outside := filepath.Join(root, "outside")
	for _, d := range []string{filepath.Join(dir, "z", "inner"), outside} {
		if err := os.MkdirAll(d, 0o700); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.Chmod(outside, 0o755); err != nil { // the mode a followed link would change
		t.Fatal(err)
	}
	precious := filepath.Join(outside, "precious.txt")
	if err := os.WriteFile(precious, []byte("keep"), 0o600); err != nil {
		t.Fatal(err)
	}
	swapped := false
	afterListHook.set(func() {
		if swapped {
			return
		}
		swapped = true
		_ = os.RemoveAll(filepath.Join(dir, "z")) // listed as a directory a moment ago
		if err := os.Symlink(outside, filepath.Join(dir, "z")); err != nil {
			t.Error(err)
		}
	})
	t.Cleanup(func() { afterListHook.set(nil) })

	emptyDir(dir)
	if !swapped {
		t.Fatal("the hook never ran: the test does not exercise the race")
	}
	if fi, err := os.Stat(outside); err != nil || fi.Mode().Perm() != 0o755 {
		t.Errorf("a link planted while emptyDir walked sent its chmod outside the folder: %v %v", fi, err)
	}
	if _, err := os.Stat(precious); err != nil {
		t.Errorf("what is behind the link was touched: %v", err)
	}
}

// A walk holds a file descriptor per level, so a chain of directories deeper
// than anything a runtime makes by accident is a way to exhaust the process's
// descriptors. Such a tree is not walked: emptyDir says it cannot empty it, and
// the caller sets the folder aside.
func TestEmptyDirDoesNotWalkATreeDeeperThanItAllows(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "slot")
	deepest := dir
	for i := 0; i < maxCleanDepth+20; i++ {
		deepest = filepath.Join(deepest, "d")
	}
	if err := os.MkdirAll(deepest, 0o700); err != nil {
		t.Fatal(err)
	}
	if emptyDir(dir) {
		t.Fatal("a tree deeper than maxCleanDepth must not be reported emptied")
	}
	if _, err := os.Stat(deepest); err != nil {
		t.Errorf("a tree it does not walk must be left as it was: %v", err)
	}
}

// A run with a higher --max-concurrent leaves slot folders above today's limit.
// They are removed at start, even when a runtime left them read-only.
func TestNewRemovesAnOverLimitSlotFolderEvenWhenReadOnly(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("root can remove a read-only tree: nothing about it is tested")
	}
	root := t.TempDir()
	slot := filepath.Join(root, profileFolder("alice"), "slot-9")
	locked := filepath.Join(slot, "mod", "locked")
	if err := os.MkdirAll(locked, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(locked, "f"), []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	for _, d := range []string{locked, filepath.Join(slot, "mod"), slot} { // deepest first
		if err := os.Chmod(d, 0o500); err != nil {
			t.Fatal(err)
		}
	}
	t.Cleanup(func() {
		for _, d := range []string{slot, filepath.Join(slot, "mod"), locked} {
			_ = os.Chmod(d, 0o700)
		}
	})

	newHarness(t, okTurn("x"), func(_ *Deps, c *Config) { c.ScratchRoot, c.MaxConcurrent = root, 2 })
	if _, err := os.Stat(slot); !os.IsNotExist(err) {
		t.Errorf("a read-only slot folder above the limit must be removed at start: %v", err)
	}
}

// Two gateways over one scratch root would hand the same slot folder to two
// turns of one profile, and each would empty the other's files. The second
// refuses to start, before it touches anything.
func TestNewRefusesASecondGatewayOverTheSameFolders(t *testing.T) {
	root := t.TempDir()
	first := newHarness(t, okTurn("x"), func(_ *Deps, c *Config) { c.ScratchRoot = root })
	slot := filepath.Join(root, profileFolder("alice"), "slot-0")
	if err := os.MkdirAll(slot, 0o700); err != nil {
		t.Fatal(err)
	}
	inUse := filepath.Join(slot, "in-use.txt") // what a running turn of the first gateway is working with
	if err := os.WriteFile(inUse, []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}

	second := func() (*Gateway, error) { return New(first.g.deps, Config{ScratchRoot: root}) }
	if _, err := second(); !errors.Is(err, ErrScratchBusy) {
		t.Fatalf("a second gateway over the same folders: err = %v, want ErrScratchBusy", err)
	}
	if _, err := os.Stat(inUse); err != nil {
		t.Fatalf("the refused gateway emptied the first one's folder: %v", err)
	}

	// Once the first has stopped, the folders are free again, and a gateway
	// that starts then cleans what a crash left behind.
	if !first.g.Shutdown(5 * time.Second) {
		t.Fatal("Shutdown must report that nothing is running")
	}
	g2, err := second()
	if err != nil {
		t.Fatalf("a gateway must be able to start once the first has stopped: %v", err)
	}
	defer g2.Shutdown(time.Second)
	if _, err := os.Stat(inUse); !os.IsNotExist(err) {
		t.Error("the new gateway must empty what the stopped one left behind")
	}
}

// What a crash left in the private folders of the turns goes at start.
func TestNewRemovesPrivateFoldersACrashLeft(t *testing.T) {
	root := t.TempDir()
	left := filepath.Join(root, ".tmp", "turn-123")
	if err := os.MkdirAll(left, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(left, "monoagent-prompt.md"), []byte("a prompt"), 0o600); err != nil {
		t.Fatal(err)
	}
	_ = newHarness(t, okTurn("x"), func(_ *Deps, c *Config) { c.ScratchRoot = root })
	if _, err := os.Stat(left); !os.IsNotExist(err) {
		t.Error("a crashed turn's prompt files must not stay on disk")
	}
}

func TestDrainWaitsUntilEveryStartedTurnHasEnded(t *testing.T) {
	h := newHarness(t, okTurn("x"))
	if !h.g.Drain(time.Second) {
		t.Fatal("with nothing running, Drain returns at once")
	}
	h.g.turnStarted()
	if h.g.Drain(50 * time.Millisecond) {
		t.Fatal("Drain must not report done while a turn runs")
	}
	go func() {
		time.Sleep(50 * time.Millisecond)
		h.g.turnEnded()
	}()
	if !h.g.Drain(5 * time.Second) {
		t.Fatal("Drain must return once the turn has ended")
	}
}

func TestBinIsCachedButErrorsAreNot(t *testing.T) {
	var calls atomic.Int32
	fail := true
	h := newHarness(t, okTurn("x"), func(d *Deps, _ *Config) {
		d.Bin = func(context.Context) (string, error) {
			calls.Add(1)
			if fail {
				return "", errors.New("monomind not found")
			}
			return "/fake/monomind", nil
		}
	})
	ctx := context.Background()
	if _, err := h.g.bin.get(ctx); err == nil {
		t.Fatal("expected the lookup error")
	}
	fail = false
	for range 3 {
		if got, err := h.g.bin.get(ctx); err != nil || got != "/fake/monomind" {
			t.Fatalf("bin = %q, %v", got, err)
		}
	}
	if got := calls.Load(); got != 2 { // the failure, then one successful lookup
		t.Errorf("Bin was called %d times, want 2: errors must not be cached, successes must", got)
	}
}

func TestConfigDefaultsAndEnv(t *testing.T) {
	testhome.SetHome(t, t.TempDir())
	cfg, err := Config{}.withDefaults()
	if err != nil {
		t.Fatal(err)
	}
	if cfg.MaxConcurrent != 4 || cfg.TurnTimeout != 10*time.Minute || cfg.BodyLimit != 2<<20 || cfg.CatalogTTL != 5*time.Minute ||
		cfg.StreamCommitAfter != 5*time.Second || cfg.KeepAlive != 15*time.Second {
		t.Errorf("defaults: %+v", cfg)
	}
	if filepath.Base(cfg.ScratchRoot) != "api" || filepath.Base(filepath.Dir(cfg.ScratchRoot)) != "workspaces" {
		t.Errorf("ScratchRoot = %q, want ~/.monoagent/workspaces/api", cfg.ScratchRoot)
	}

	env := func(m map[string]string) func(string) string { return func(k string) string { return m[k] } }
	got, err := ConfigFromEnv(env(map[string]string{"MONOAGENT_API_MAX_CONCURRENT": "8", "MONOAGENT_API_TURN_TIMEOUT": "15m"}))
	if err != nil || got.MaxConcurrent != 8 || got.TurnTimeout != 15*time.Minute {
		t.Errorf("ConfigFromEnv = %+v, %v", got, err)
	}
	if got, err := ConfigFromEnv(env(nil)); err != nil || got.MaxConcurrent != 0 || got.TurnTimeout != 0 {
		t.Errorf("unset variables must leave the defaults to withDefaults: %+v, %v", got, err)
	}
	for k, v := range map[string]string{
		"MONOAGENT_API_MAX_CONCURRENT": "0",
		"MONOAGENT_API_TURN_TIMEOUT":   "5s",
	} {
		if _, err := ConfigFromEnv(env(map[string]string{k: v})); err == nil {
			t.Errorf("%s=%s was accepted", k, v)
		}
	}
	for _, v := range []string{"many", "-2", "1.5"} {
		if _, err := ConfigFromEnv(env(map[string]string{"MONOAGENT_API_MAX_CONCURRENT": v})); err == nil {
			t.Errorf("MONOAGENT_API_MAX_CONCURRENT=%s was accepted", v)
		}
	}
	if _, err := ConfigFromEnv(env(map[string]string{"MONOAGENT_API_TURN_TIMEOUT": "soon"})); err == nil {
		t.Error("MONOAGENT_API_TURN_TIMEOUT=soon was accepted")
	}
}
