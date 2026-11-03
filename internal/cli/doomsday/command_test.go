package doomsday

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/spf13/cobra"

	"github.com/monoes/mono-agent/internal/cli/clicore"
)

// fakeEngine records what reached it. Hash is a stand-in for the store's.
type fakeEngine struct {
	containment error
	canonCalls  int
	enters      []Ack
	enterHash   string
	enterCanon  Canon
	stop        StopOutcome
	stops       int
	canon       Charter
}

func (f *fakeEngine) Containment() error { return f.containment }
func (f *fakeEngine) Canonical(_ context.Context, c Charter) (Canon, error) {
	f.canonCalls++
	f.canon = c
	b, _ := json.Marshal(c)
	h := sha256.Sum256(b)
	return Canon{Bytes: b, Hash: hex.EncodeToString(h[:])}, nil
}
func (f *fakeEngine) Enter(_ context.Context, c Canon, h string, a Ack) (Entry, error) {
	f.enters = append(f.enters, a)
	f.enterHash, f.enterCanon = h, c
	return Entry{RunID: "r1", Hash: c.Hash, Generation: 1}, nil
}
func (f *fakeEngine) Stop(context.Context) (StopOutcome, error) { f.stops++; return f.stop, nil }
func (f *fakeEngine) Status(context.Context) (Status, error) {
	return Status{Active: true, RunID: "r1", State: "running", Hash: "abcdef0123", Lines: []string{"spending"}}, nil
}

const goodCharter = `{"goal":"ship it","limits":{"budget_micro":5000000},
"pins":{"boss":{"provider":"p","model":"m","version":"1"},"verifier":{"provider":"p","model":"v","version":"2"},"judging_policy":"j1"}}`

func writeCharter(t *testing.T, body string) string {
	t.Helper()
	p := filepath.Join(t.TempDir(), "charter.json")
	if err := os.WriteFile(p, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	return p
}

type rig struct {
	e        *fakeEngine
	deps     Deps
	operator error  // RequireOperator's answer
	refusal  string // opgate.Refusal's answer
	terminal bool
}

func newRig() *rig {
	r := &rig{e: &fakeEngine{}, terminal: true}
	r.deps = Deps{
		Engine:          func() Engine { return r.e },
		RequireOperator: func(string) error { return r.operator },
		OperatorRefusal: func() string { return r.refusal },
		StdinIsTerminal: func() bool { return r.terminal },
	}
	return r
}

func run(t *testing.T, d Deps, stdin string, args ...string) (string, error) {
	t.Helper()
	cmd := NewCommand(d)
	var out bytes.Buffer
	cmd.SetOut(&out)
	cmd.SetErr(&out)
	cmd.SetIn(strings.NewReader(stdin))
	cmd.SetArgs(args)
	cmd.SilenceUsage, cmd.SilenceErrors = true, true
	err := cmd.ExecuteContext(context.Background())
	return out.String(), err
}

func fullHash(t *testing.T, r *rig, file string) string {
	t.Helper()
	out, err := run(t, r.deps, "", "render", "--charter", file)
	if err != nil {
		t.Fatal(err, out)
	}
	return strings.Fields(strings.SplitN(out, "\n", 2)[0])[1]
}

func exitOf(err error) int { return clicore.ExitCodeFor(err) }

func TestCommandIsHidden(t *testing.T) {
	if !NewCommand(Deps{}).Hidden {
		t.Fatal("the doomsday group is advertised in help")
	}
}

// TestStartAndStopAreOperatorOnly covers every verb that acts: start, stop, finish and rubric.
func TestStartAndStopAreOperatorOnly(t *testing.T) {
	file := writeCharter(t, goodCharter)
	cases := map[string]func(*rig){
		"gate refuses":       func(r *rig) { r.operator = clicore.OperatorOnly("not you") },
		"unwired gate":       func(r *rig) { r.deps.RequireOperator = nil },
		"human-only refusal": func(r *rig) { r.refusal = "an agent token is set" },
		"unwired refusal":    func(r *rig) { r.deps.OperatorRefusal = nil },
	}
	for name, set := range cases {
		for _, verb := range [][]string{{"start", "--charter", file, "--yes-i-lift", "--hash", "x"}, {"stop"}, {"finish"}, {"rubric", "c1", "approve", "--dossier", "abcdef12"}} {
			t.Run(name+"/"+verb[0], func(t *testing.T) {
				r := newRig()
				set(r)
				_, err := run(t, r.deps, "", verb...)
				if exitOf(err) != 3 {
					t.Fatalf("want exit 3, got %v (%v)", exitOf(err), err)
				}
				if r.e.canonCalls+len(r.e.enters)+r.e.stops != 0 {
					t.Fatalf("the engine was reached by a refused caller: %+v", r.e)
				}
			})
		}
	}
}

func TestStartRefusesWhereContainmentIsUnavailable(t *testing.T) {
	file := writeCharter(t, goodCharter)
	r := newRig()
	h := fullHash(t, r, file)
	r.e.containment = errors.New("no cgroup v2 delegation")
	out, err := run(t, r.deps, "", "start", "--charter", file, "--yes-i-lift", "--hash", h)
	if err == nil || len(r.e.enters) != 0 || !strings.Contains(err.Error(), "no cgroup v2 delegation") {
		t.Fatalf("started without containment: %v %v %s", err, r.e.enters, out)
	}
}

func TestDefaultEngineCannotStartARun(t *testing.T) {
	file := writeCharter(t, goodCharter)
	d := newRig().deps
	d.Engine = nil // an install with no engine wired: the mode is off
	out, err := run(t, d, "", "start", "--charter", file, "--yes-i-lift", "--hash", strings.Repeat("a", 64))
	if err == nil || !strings.Contains(err.Error(), ErrOff.Error()) {
		t.Fatalf("start did not refuse on the off engine: %v %s", err, out)
	}
	if _, err := (Off{}).Enter(context.Background(), Canon{}, "", Ack{}); !errors.Is(err, ErrOff) {
		t.Fatal("Off.Enter did not refuse")
	}
}

func TestTypedPhraseIsRequiredAndExact(t *testing.T) {
	file := writeCharter(t, goodCharter)
	r := newRig()
	h := fullHash(t, r, file)
	for name, in := range map[string]string{
		"empty":       "",
		"wrong":       "yes\n",
		"short hash":  "I understand: lift 0 lines, charter 1234abcd\n",
		"wrong count": Phrase(3, h) + "\n",
	} {
		t.Run(name, func(t *testing.T) {
			_, err := run(t, r.deps, in, "start", "--charter", file)
			if err == nil || len(r.e.enters) != 0 {
				t.Fatalf("started on %q: %v", in, err)
			}
		})
	}
	out, err := run(t, r.deps, Phrase(0, h)+"\n", "start", "--charter", file)
	if err != nil || len(r.e.enters) != 1 || r.e.enters[0].Kind != "terminal" {
		t.Fatalf("typed start: %v %v %s", err, r.e.enters, out)
	}
	if r.e.enterHash != h {
		t.Fatal("Enter was not handed the hash the operator saw")
	}
}

func TestPhraseNamesLiftedLineCountAndFirst8Hex(t *testing.T) {
	h := strings.Repeat("0123456789abcdef", 4)
	p := Phrase(6, h)
	if !strings.Contains(p, "6 lines") || !strings.Contains(p, "01234567") || strings.Contains(p, "012345678") {
		t.Fatal(p)
	}
}

func TestStartWithoutATerminalNeedsTheScriptFlags(t *testing.T) {
	file := writeCharter(t, goodCharter)
	r := newRig()
	h := fullHash(t, r, file)
	r.terminal = false
	if _, err := run(t, r.deps, Phrase(0, h)+"\n", "start", "--charter", file); err == nil || len(r.e.enters) != 0 {
		t.Fatalf("started from a pipe on a typed phrase: %v", err)
	}
}

func TestScriptStartNeedsTheFullHashAndIsRecorded(t *testing.T) {
	file := writeCharter(t, goodCharter)
	r := newRig()
	h := fullHash(t, r, file)
	for name, args := range map[string][]string{
		"flag alone":             {"--yes-i-lift"},
		"short hash":             {"--yes-i-lift", "--hash", h[:8]},
		"wrong hash":             {"--yes-i-lift", "--hash", strings.Repeat("0", 64)},
		"hash alone":             {"--hash", h},
		"hash no flag in script": {"--hash", h},
	} {
		t.Run(name, func(t *testing.T) {
			_, err := run(t, r.deps, "", append([]string{"start", "--charter", file}, args...)...)
			if err == nil || len(r.e.enters) != 0 {
				t.Fatalf("started: %v", err)
			}
		})
	}
	if _, err := run(t, r.deps, "", "start", "--charter", file, "--yes-i-lift", "--hash", h); err != nil {
		t.Fatal(err)
	}
	if len(r.e.enters) != 1 || r.e.enters[0].Kind != "script" {
		t.Fatalf("a script start was not recorded as one: %+v", r.e.enters)
	}
}

func TestEverythingNeverTicksPlatformCaps(t *testing.T) {
	file := writeCharter(t, goodCharter)
	r := newRig()
	out, err := run(t, r.deps, "", "render", "--charter", file, "--everything")
	if err != nil {
		t.Fatal(err)
	}
	if r.e.canon.Has(LinePlatformCaps) || len(r.e.canon.Lines) != 6 {
		t.Fatalf("lines: %v", r.e.canon.Lines)
	}
	if !strings.Contains(out, "[no ] platform_caps") || !strings.Contains(out, "never ticks them") {
		t.Fatal(out)
	}
	if _, err := run(t, r.deps, "", "render", "--charter", file, "--everything", "--lift", "platform_caps"); err != nil {
		t.Fatal(err)
	}
	if !r.e.canon.Has(LinePlatformCaps) || len(r.e.canon.Lines) != 7 {
		t.Fatalf("by name: %v", r.e.canon.Lines)
	}
	if _, err := run(t, r.deps, "", "render", "--charter", file, "--lift", "nonsense"); err == nil {
		t.Fatal("an unknown line was accepted")
	}
}

func TestRenderWritesNothingAndStatesWhatIsNotDefended(t *testing.T) {
	file := writeCharter(t, goodCharter)
	r := newRig()
	out, err := run(t, r.deps, "", "render", "--charter", file)
	if err != nil || len(r.e.enters) != 0 {
		t.Fatal(err, r.e.enters)
	}
	for _, want := range []string{"does NOT defend", "no isolation", "outlive a hard Stop", "auto-approve nothing", "tree depth 4", "live sessions per tree 30", "max concurrent processes 30"} {
		if !strings.Contains(out, want) {
			t.Errorf("render lacks %q", want)
		}
	}
}

func TestStartShowsTheDialogBeforeAsking(t *testing.T) {
	file := writeCharter(t, goodCharter)
	r := newRig()
	out, _ := run(t, r.deps, "no\n", "start", "--charter", file)
	if !strings.Contains(out, "does NOT defend") || !strings.Contains(out, "type exactly") {
		t.Fatal(out)
	}
}

func TestStopNeverSaysStoppedWithoutTheFacts(t *testing.T) {
	cases := map[string]struct {
		o    StopOutcome
		want string
		ok   bool
	}{
		"marker failed": {StopOutcome{IntentRecorded: true}, "NOT RECORDED", false},
		"survivor":      {StopOutcome{MarkerRecorded: true, IntentRecorded: true, Survivors: []string{"pid 7"}}, "pid 7", false},
		"no intent":     {StopOutcome{MarkerRecorded: true}, "NOT RECORDED", false},
		"clean":         {StopOutcome{MarkerRecorded: true, IntentRecorded: true}, "stopped", true},
	}
	for name, c := range cases {
		t.Run(name, func(t *testing.T) {
			r := newRig()
			r.e.stop = c.o
			out, err := run(t, r.deps, "", "stop")
			if (err == nil) != c.ok || !strings.Contains(out, c.want) {
				t.Fatalf("err=%v out=%q", err, out)
			}
			if !c.ok && strings.Contains(strings.ToLower(strings.ReplaceAll(out, "NOT stopped", "")), "stopped\n") {
				t.Fatalf("said stopped: %q", out)
			}
		})
	}
}

func TestStatusIsReadOnlyAndUngated(t *testing.T) {
	r := newRig()
	r.operator = clicore.OperatorOnly("no")
	out, err := run(t, r.deps, "", "status")
	if err != nil || !strings.Contains(out, "running") {
		t.Fatal(err, out)
	}
	if out, _ := run(t, Deps{}, "", "status"); !strings.Contains(out, "no run") {
		t.Fatal(out)
	}
}

func TestCharterValidation(t *testing.T) {
	load := func(body string) (Charter, error) { return LoadCharter(strings.NewReader(body)) }
	c, err := load(goodCharter)
	if err != nil || c.Validate() != nil {
		t.Fatal(err, c.Validate())
	}
	if l := c.Limits; l.TreeDepth != 4 || l.LivePerTree != 30 || l.MaxConcurrent != 30 {
		t.Fatalf("defaults: %+v", l)
	}
	mut := map[string]func(*Charter){
		"depth 13":            func(c *Charter) { c.Limits.TreeDepth = 13 },
		"depth -1":            func(c *Charter) { c.Limits.TreeDepth = -1 },
		"concurrent > live":   func(c *Charter) { c.Limits.MaxConcurrent = 31 },
		"no budget choice":    func(c *Charter) { c.Limits.BudgetMicro = 0 },
		"both budgets":        func(c *Charter) { c.Limits.NoCeiling = true },
		"no boss pin":         func(c *Charter) { c.Pins.Boss = Pin{} },
		"same verifier":       func(c *Charter) { c.Pins.Verifier = c.Pins.Boss },
		"no judging policy":   func(c *Charter) { c.Pins.Judging = "" },
		"ask access full":     func(c *Charter) { c.PerAsk.Access = "full" },
		"ask limit no access": func(c *Charter) { c.PerAsk = PerAsk{Tasks: 3} },
		"no verifier":         func(c *Charter) { c.Pins.Verifier = Pin{} },
		"negative ask":        func(c *Charter) { c.PerAsk.Tasks = -1 },
		"empty goal":          func(c *Charter) { c.Goal = " " },
		"bad criterion":       func(c *Charter) { c.Criteria = []Criterion{{Kind: "command"}} },
		"total below live":    func(c *Charter) { c.Limits.MaxTotal = 5 },
	}
	for name, m := range mut {
		cc := c
		m(&cc)
		if cc.Validate() == nil {
			t.Errorf("%s was accepted", name)
		}
	}
	cc := c
	cc.Limits.TreeDepth = 12
	cc.Limits.MaxConcurrent, cc.Limits.ConcurrentAboveLive = 40, true
	cc.Limits.MaxTotal = 100
	if err := cc.Validate(); err != nil {
		t.Errorf("a valid charter (depth 12, explicit concurrency) refused: %v", err)
	}
	for _, bad := range []string{`{"goal":"x","surprise":1}`, goodCharter + goodCharter, `nope`} {
		if _, err := load(bad); err == nil {
			t.Errorf("loaded %q", bad)
		}
	}
}

var _ = cobra.Command{}

// bossEngine is a fakeEngine that can record a boss.
type bossEngine struct {
	*fakeEngine
	bound []int64
	err   error
}

func (b *bossEngine) BindBoss(_ context.Context, session int64, _ Entry) error {
	b.bound = append(b.bound, session)
	return b.err
}

// `start --boss-session` hands the named session to the engine, once, and nothing without the flag; a refusal of the
// engine or an engine that cannot record a boss is an error naming the run (it started and must be stopped). Mutants
// tried: ignoring the flag (red on the bound list), binding a fixed session (red on the id), swallowing the bind error
// (red on the refusal step).
func TestStartHonoursBossSession(t *testing.T) {
	file := writeCharter(t, goodCharter)
	r := newRig()
	h := fullHash(t, r, file)
	be := &bossEngine{fakeEngine: r.e}
	r.deps.Engine = func() Engine { return be }

	if _, err := run(t, r.deps, "", "start", "--charter", file, "--yes-i-lift", "--hash", h); err != nil || len(be.bound) != 0 {
		t.Fatalf("without the flag: %v %v", err, be.bound)
	}
	if _, err := run(t, r.deps, "", "start", "--charter", file, "--yes-i-lift", "--hash", h, "--boss-session", "7"); err != nil || len(be.bound) != 1 || be.bound[0] != 7 {
		t.Fatalf("with the flag: %v %v", err, be.bound)
	}
	be.err = errors.New("not a lead mission")
	_, err := run(t, r.deps, "", "start", "--charter", file, "--yes-i-lift", "--hash", h, "--boss-session", "43")
	if err == nil || !strings.Contains(err.Error(), "r1") || !strings.Contains(err.Error(), "43") || !strings.Contains(err.Error(), "not a lead mission") {
		t.Fatalf("a refused boss: %v", err)
	}
	// an engine that cannot record a boss refuses the flag rather than ignoring it
	r2 := newRig()
	if _, err := run(t, r2.deps, "", "start", "--charter", file, "--yes-i-lift", "--hash", h, "--boss-session", "44"); err == nil || !strings.Contains(err.Error(), "cannot record a boss") {
		t.Fatalf("an engine without a boss binder: %v", err)
	}
}
