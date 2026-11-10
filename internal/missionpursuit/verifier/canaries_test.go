//go:build !windows

package verifier

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/monoes/mono-agent/internal/doomsday/finish"
)

var markerRE = regexp.MustCompile(`canary-ran:(C[0-9]+b?)`)

// testWorld is a scratch area in a short directory (a unix socket path is limited to ~104 bytes).
type testWorld struct {
	parent, base, home, runtime, tmp, shm string
}

func newTestWorld(t *testing.T) testWorld {
	t.Helper()
	parent, err := os.MkdirTemp("", "c")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { finish.RemoveTree(parent) })
	w := testWorld{parent: parent, base: filepath.Join(parent, "base"), home: filepath.Join(parent, "home"), runtime: filepath.Join(parent, "rt"), tmp: filepath.Join(parent, "tmp"), shm: filepath.Join(parent, "shm")}
	for _, d := range []string{w.base, w.home, w.runtime, w.tmp, w.shm} {
		if err := os.Mkdir(d, 0o700); err != nil {
			t.Fatal(err)
		}
	}
	return w
}

func (w testWorld) opts(x CanaryOptions) CanaryOptions {
	x.HomeDir, x.RuntimeDir, x.TmpDir, x.ShmDir = w.home, w.runtime, w.tmp, w.shm
	return x
}

// ideal is what each canary ends with under a confinement that holds.
func ideal(id string) CanaryRun {
	m := "canary-ran:" + id + "\n"
	switch id {
	case "C0", "C10", "C10b":
		return CanaryRun{Result: finish.ResPass, Stdout: m}
	case "C11":
		return CanaryRun{Result: finish.ResTimeout, ExitCode: -1, Stdout: m}
	}
	return CanaryRun{Result: finish.ResFail, ExitCode: 1, Stdout: m}
}

type scripted struct {
	mu    sync.Mutex
	seen  []string
	alter map[string]func(CanaryRun) CanaryRun
	specs map[string]string
	to    map[string]time.Duration
	check func(id, spec string)
}

func (s *scripted) exec(_ context.Context, c Criterion, timeout time.Duration) CanaryRun {
	m := markerRE.FindStringSubmatch(c.Spec)
	if m == nil {
		return CanaryRun{Result: finish.ResError, Detail: "no marker in the spec: " + c.Spec}
	}
	id := m[1]
	s.mu.Lock()
	s.seen = append(s.seen, id)
	if s.specs == nil {
		s.specs, s.to = map[string]string{}, map[string]time.Duration{}
	}
	s.specs[id], s.to[id] = c.Spec, timeout
	s.mu.Unlock()
	if s.check != nil {
		s.check(id, c.Spec)
	}
	r := ideal(id)
	if f := s.alter[id]; f != nil {
		r = f(r)
	}
	return r
}

func noLeftovers(context.Context) ([]string, error) { return nil, nil }

func verdicts(rs []CanaryResult) map[string]CanaryVerdict {
	out := map[string]CanaryVerdict{}
	for _, r := range rs {
		out[r.ID] = r.Verdict
	}
	return out
}

func runWith(t *testing.T, s *scripted, mut func(*CanaryOptions)) []CanaryResult {
	t.Helper()
	w := newTestWorld(t)
	o := w.opts(CanaryOptions{Exec: s.exec, Leftovers: noLeftovers, HasTool: func(string) bool { return true }})
	if mut != nil {
		mut(&o)
	}
	return RunCanariesWith(context.Background(), &FakeConfiner{}, w.base, o)
}

func TestTheCanaryTableIsTheDesignsC0ToC11AndAConfinementThatHoldsPassesAll(t *testing.T) {
	rs := runWith(t, &scripted{}, nil)
	var ids []string
	for _, r := range rs {
		ids = append(ids, r.ID)
		if r.Verdict != CanaryHeld || r.Name == "" {
			t.Errorf("%s: %+v", r.ID, r)
		}
		if heavy := r.ID == "C8" || r.ID == "C9" || r.ID == "C10" || r.ID == "C10b" || r.ID == "C11"; r.Heavy != heavy {
			t.Errorf("%s heavy = %v", r.ID, r.Heavy)
		}
	}
	if got := strings.Join(ids, ","); got != "C0,C1,C2,C3,C4,C5,C6,C7,C8,C9,C10,C10b,C11,C12,C13,C14" {
		t.Fatalf("canaries: %s", got)
	}
	if err := CanariesHold(rs); err != nil {
		t.Fatal(err)
	}
}

// Revert proof for every verdict: a program that is NOT refused (or a control that is) makes exactly that canary broken and the
// whole self test fail. Turning the verdict logic into "any result is fine" turns this red.
func TestEveryCanaryEndsBrokenWhenItsProgramIsNotRefused(t *testing.T) {
	for _, id := range []string{"C0", "C1", "C2", "C3", "C4", "C5", "C6", "C7", "C8", "C9", "C10", "C10b", "C11", "C12", "C13", "C14"} {
		s := &scripted{alter: map[string]func(CanaryRun) CanaryRun{id: func(r CanaryRun) CanaryRun {
			switch id {
			case "C0", "C10", "C10b": // must pass: make it fail
				return CanaryRun{Result: finish.ResFail, ExitCode: 1, Stdout: r.Stdout}
			case "C11": // must time out: it ended on its own
				return CanaryRun{Result: finish.ResPass, Stdout: r.Stdout}
			}
			return CanaryRun{Result: finish.ResPass, Stdout: r.Stdout} // must be refused: it ran fine
		}}}
		rs := runWith(t, s, nil)
		v := verdicts(rs)
		if v[id] != CanaryBroken {
			t.Errorf("%s: verdict %q, want broken", id, v[id])
		}
		for other, ov := range v {
			if other != id && ov != CanaryHeld {
				t.Errorf("%s broke %s as well: %q", id, other, ov)
			}
		}
		err := CanariesHold(rs)
		if err == nil || !strings.Contains(err.Error(), id) {
			t.Errorf("%s: CanariesHold = %v", id, err)
		}
	}
}

// A canary that never started proves nothing: error, never held (and never a pass of the self test).
func TestACanaryThatNeverRanOrRanBadlyIsAnErrorNotAHeldFence(t *testing.T) {
	for name, alter := range map[string]func(CanaryRun) CanaryRun{
		"never started":           func(CanaryRun) CanaryRun { return CanaryRun{Result: finish.ResError, Detail: "no unit"} },
		"no marker":               func(r CanaryRun) CanaryRun { r.Stdout = ""; return r },
		"marker of another one":   func(r CanaryRun) CanaryRun { r.Stdout = "canary-ran:C99\n"; return r },
		"fail with exit zero":     func(r CanaryRun) CanaryRun { r.ExitCode = 0; return r },
		"hung instead of refused": func(CanaryRun) CanaryRun { return CanaryRun{Result: finish.ResTimeout, Stdout: "canary-ran:C1\n"} },
		"oversize":                func(CanaryRun) CanaryRun { return CanaryRun{Result: finish.ResOversize, Stdout: "canary-ran:C1\n"} },
		"tainted":                 func(CanaryRun) CanaryRun { return CanaryRun{Result: finish.ResTainted, Stdout: "canary-ran:C1\n"} },
	} {
		s := &scripted{alter: map[string]func(CanaryRun) CanaryRun{"C1": alter}}
		rs := runWith(t, s, nil)
		if v := verdicts(rs)["C1"]; v != CanaryError {
			t.Errorf("%s: verdict %q, want error", name, v)
		}
		if CanariesHold(rs) == nil {
			t.Errorf("%s: the self test passed", name)
		}
	}
}

func TestACanaryThatWantsAPassNeedsItsMarkerToo(t *testing.T) {
	s := &scripted{alter: map[string]func(CanaryRun) CanaryRun{"C0": func(r CanaryRun) CanaryRun { r.Stdout = ""; return r }}}
	rs := runWith(t, s, nil)
	if v := verdicts(rs)["C0"]; v != CanaryError {
		t.Fatalf("control without its marker: %q", v)
	}
}

// C2 must also leave no file behind: a confiner that lies about the exit code but lets the write happen is caught by the file.
func TestC2IsBrokenWhenTheOutsideFileExistsAfterwardsWhateverTheExitCodeSaid(t *testing.T) {
	var outside string
	s := &scripted{check: func(id, spec string) {
		if id == "C2" {
			outside = quotedPaths(spec)[0]
			if err := os.WriteFile(outside, []byte("x"), 0o600); err != nil {
				t.Error(err)
			}
		}
	}}
	rs := runWith(t, s, nil)
	if v := verdicts(rs)["C2"]; v != CanaryBroken {
		t.Fatalf("C2 = %q with the file written (%s)", v, outside)
	}
}

var quotedRE = regexp.MustCompile(`'(/[^']*)'`)

func quotedPaths(spec string) []string {
	var out []string
	for _, m := range quotedRE.FindAllStringSubmatch(spec, -1) {
		out = append(out, m[1])
	}
	return out
}

func TestTheUnitMustBeGoneAfterTheRunForTheHeavyCanaries(t *testing.T) {
	leftovers := func(context.Context) ([]string, error) { return []string{"monoagent-check-0123456789abcdef"}, nil }
	rs := runWith(t, &scripted{}, func(o *CanaryOptions) { o.Leftovers = leftovers })
	v := verdicts(rs)
	for _, id := range []string{"C8", "C9", "C10", "C10b", "C11"} {
		if v[id] != CanaryBroken {
			t.Errorf("%s with a unit left behind: %q", id, v[id])
		}
	}
	for _, id := range []string{"C0", "C1", "C2", "C3", "C4", "C5", "C6", "C7", "C12", "C13", "C14"} {
		if v[id] != CanaryHeld {
			t.Errorf("%s asked about units it does not care about: %q", id, v[id])
		}
	}
	// a confiner that cannot say what it left behind cannot pass the canaries that need to know (fail closed)
	rs = runWith(t, &scripted{}, func(o *CanaryOptions) { o.Leftovers = nil })
	v = verdicts(rs)
	for _, id := range []string{"C8", "C10", "C11"} {
		if v[id] != CanaryNotRun {
			t.Errorf("%s without a leftover check: %q", id, v[id])
		}
	}
	if CanariesHold(rs) == nil {
		t.Fatal("not-run passed the self test")
	}
	rs = runWith(t, &scripted{}, func(o *CanaryOptions) {
		o.Leftovers = func(context.Context) ([]string, error) { return nil, errors.New("bus gone") }
	})
	if v := verdicts(rs)["C11"]; v != CanaryError {
		t.Fatalf("a failing leftover check: %q", v)
	}
}

// Fail closed: a tool the spec needs that is missing is not-run, which fails the self test; only the setsid variant is
// reported without failing it.
func TestAMissingToolIsNotRunAndOnlySetsidIsNotFatal(t *testing.T) {
	rs := runWith(t, &scripted{}, func(o *CanaryOptions) { o.HasTool = func(n string) bool { return n != "setsid" } })
	if v := verdicts(rs); v["C10b"] != CanaryNotRun || v["C10"] != CanaryHeld {
		t.Fatalf("%v", v)
	}
	if err := CanariesHold(rs); err != nil {
		t.Fatalf("a missing setsid failed the self test: %v", err)
	}
	for _, tool := range []string{"cat", "touch", "readlink", "head", "sleep"} {
		rs = runWith(t, &scripted{}, func(o *CanaryOptions) { o.HasTool = func(n string) bool { return n != tool } })
		if CanariesHold(rs) == nil {
			t.Errorf("without %s the self test passed", tool)
		}
	}
}

func TestSkipHeavyAndOnlySelectCanaries(t *testing.T) {
	var ids []string
	for _, r := range runWith(t, &scripted{}, func(o *CanaryOptions) { o.SkipHeavy = true }) {
		ids = append(ids, r.ID)
	}
	if got := strings.Join(ids, ","); got != "C0,C1,C2,C3,C4,C5,C6,C7,C12,C13,C14" {
		t.Fatalf("light set: %s", got)
	}
	rs := runWith(t, &scripted{}, func(o *CanaryOptions) { o.Only = []string{"C7", "C1"} })
	if len(rs) != 2 || rs[0].ID != "C1" || rs[1].ID != "C7" {
		t.Fatalf("only: %+v", rs)
	}
}

func TestTheTimeoutCanaryUsesAShortCriterionTimeout(t *testing.T) {
	s := &scripted{}
	runWith(t, s, nil)
	if s.to["C11"] != 5*time.Second || s.to["C0"] <= 0 {
		t.Fatalf("timeouts %v", s.to)
	}
}

// The host plants what the canaries probe, from the places the options name, and takes all of it away again.
func TestTheHostPlantsTheCanariesAndRemovesEverything(t *testing.T) {
	w := newTestWorld(t)
	before := listAll(t, w.parent)
	var sockets, planted int
	var secretName string
	var shmPlanted, shmProbe, ptyArg bool
	var shmProbePath string
	s := &scripted{check: func(id, spec string) {
		switch id {
		case "C1":
			p := quotedPaths(spec)[0]
			if filepath.Dir(p) != w.parent {
				t.Errorf("C1 file %s is not in the parent of the scratch base %s", p, w.parent)
			}
			if b, err := os.ReadFile(p); err != nil || len(b) == 0 {
				t.Errorf("C1 file not planted: %v", err)
			}
		case "C2":
			if p := quotedPaths(spec)[0]; filepath.Dir(p) != w.parent {
				t.Errorf("C2 path %s", p)
			} else if _, err := os.Lstat(p); err == nil {
				t.Errorf("C2 path exists before the run")
			}
		case "C3":
			for _, p := range quotedPaths(spec) {
				if st, err := os.Lstat(p); err != nil || st.Mode()&os.ModeSocket == 0 {
					t.Errorf("C3 %s is not a socket: %v", p, err)
				} else if d := filepath.Dir(p); d == w.runtime || d == w.tmp {
					sockets++
				}
			}
		case "C4":
			for _, p := range quotedPaths(spec) {
				if _, err := os.Lstat(p); err != nil {
					t.Errorf("C4 probes %s which does not exist: %v", p, err)
				}
				if filepath.Dir(p) == w.home {
					planted++
				}
			}
		case "C13":
			ps := quotedPaths(spec)
			if len(ps) != 2 || filepath.Dir(ps[0]) != w.shm || filepath.Dir(ps[1]) != w.shm {
				t.Errorf("C13 paths %v are not in the shm dir", ps)
				break
			}
			if _, err := os.Lstat(ps[0]); err != nil {
				t.Errorf("C13 marker not planted: %v", err)
			}
			if _, err := os.Lstat(ps[1]); err == nil {
				t.Errorf("C13 write probe exists before the run")
			}
			shmPlanted, shmProbe = true, true
			shmProbePath = ps[1]
		case "C14":
			for _, p := range quotedPaths(spec) {
				if !strings.HasPrefix(p, "/dev/pts/") {
					t.Errorf("C14 names %s", p)
				} else if _, err := os.Lstat(p); err != nil {
					t.Errorf("C14 slave %s does not exist during the run: %v", p, err)
				}
				ptyArg = true
			}
		case "C5":
			m := regexp.MustCompile(`/proc/(\d+)/environ`).FindStringSubmatch(spec)
			if m == nil {
				t.Errorf("C5 does not read a host process's environ: %s", spec)
				break
			}
			b, err := os.ReadFile("/proc/" + m[1] + "/environ")
			name := regexp.MustCompile(`MONOAGENT_CANARY_SECRET_[0-9a-f]+`).FindString(spec)
			if err != nil || name == "" || !strings.Contains(string(b), name+"=") {
				t.Errorf("the planted process has no per-run secret in its environment (%q): %v", name, err)
			}
			if name == "" || os.Getenv(name) == "" {
				t.Errorf("the host did not set the per-run secret in its own environment")
			}
			secretName = name
		}
	}}
	rs := runWith2(t, w, s)
	if err := CanariesHold(rs); err != nil {
		t.Fatal(err)
	}
	if sockets != 2 {
		t.Errorf("host sockets seen: %d, want one in the runtime dir and one in tmp", sockets)
	}
	if planted < 1 {
		t.Errorf("no canary file planted in the home directory")
	}
	if after := listAll(t, w.parent); strings.Join(after, "\n") != strings.Join(before, "\n") {
		t.Fatalf("the canaries left files behind:\nbefore %v\nafter  %v", before, after)
	}
	if secretName == "" || os.Getenv(secretName) != "" {
		t.Fatal("the secret stayed in the host's environment")
	}
	for _, kv := range os.Environ() {
		if strings.HasPrefix(kv, "MONOAGENT_CANARY_") {
			t.Fatalf("a canary variable stayed in the host's environment: %.40s", kv)
		}
	}
	if !shmPlanted || !shmProbe {
		t.Fatal("C13 was not planted")
	}
	if _, err := os.Lstat(shmProbePath); err == nil {
		t.Fatal("the write probe survived")
	}
	if f, err := os.OpenFile("/dev/ptmx", os.O_RDWR, 0); err == nil {
		f.Close()
		if !ptyArg {
			t.Error("a pty could be made but C14 names none of the host's own")
		}
	}
}

func runWith2(t *testing.T, w testWorld, s *scripted) []CanaryResult {
	t.Helper()
	return RunCanariesWith(context.Background(), &FakeConfiner{}, w.base, w.opts(CanaryOptions{Exec: s.exec, Leftovers: noLeftovers, HasTool: func(string) bool { return true }}))
}

func listAll(t *testing.T, root string) []string {
	t.Helper()
	var out []string
	filepath.WalkDir(root, func(p string, d os.DirEntry, err error) error {
		if err == nil {
			out = append(out, p)
		}
		return nil
	})
	return out
}

// Every canary is a plan the finish harness can run, prints its marker first and names only system tools.
func TestEveryCanaryIsARunnablePlanThatPrintsItsMarkerFirst(t *testing.T) {
	s := &scripted{}
	runWith(t, s, nil)
	if len(s.specs) != 16 {
		t.Fatalf("%d specs seen", len(s.specs))
	}
	for id, spec := range s.specs {
		plan, err := finish.ParsePlan("command", spec, "exit:0")
		if err != nil {
			t.Errorf("%s: %v", id, err)
			continue
		}
		if strings.Contains(plan.Argv[0], "/") {
			t.Errorf("%s: program %q is not a system-path name", id, plan.Argv[0])
		}
		if !strings.Contains(spec, "echo canary-ran:"+id+";") {
			t.Errorf("%s does not print its marker first: %s", id, spec)
		}
	}
	if !strings.Contains(s.specs["C8"], "-lt 2000") || !strings.Contains(s.specs["C9"], "3000000000") || !strings.Contains(s.specs["C11"], "sleep 600") {
		t.Errorf("heavy specs drifted from the design: %v", s.specs)
	}
}

// The real sandbox with a confiner that confines nothing: the control passes, the network canary holds only because the finish
// sandbox itself denies it, and every other fence is reported broken. Only the light canaries run for real here.
func TestACanaryRunAgainstAConfinerThatConfinesNothingIsCaught(t *testing.T) {
	needsRealSandbox(t, "cat", "touch", "readlink", "ls", "grep", "test")
	w := newTestWorld(t)
	rs := RunCanariesWith(context.Background(), &FakeConfiner{}, w.base, w.opts(CanaryOptions{SkipHeavy: true}))
	v := verdicts(rs)
	if v["C0"] != CanaryHeld {
		t.Errorf("the positive control must pass anywhere: %+v", rs)
	}
	// (C5 is not in the list: the sandbox's own user namespace already keeps the program from reading the host's /proc/<pid>/environ,
	// so it may hold without a confiner; its revert proof is the real confiner's, in sdconfine.)
	if v["C12"] != CanaryHeld {
		t.Errorf("the sandbox builds the environment from nothing, so C12 holds on its own: %+v", rs)
	}
	for _, id := range []string{"C1", "C2", "C3", "C4", "C6", "C13"} {
		if v[id] != CanaryBroken {
			t.Errorf("%s = %q with no confinement at all: %+v", id, v[id], rs)
		}
	}
	if CanariesHold(rs) == nil {
		t.Fatal("no confinement passed the self test")
	}
	for _, p := range listAll(t, w.parent) {
		if strings.Contains(filepath.Base(p), "canary") {
			t.Errorf("left behind: %s", p)
		}
	}
}
