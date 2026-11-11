package verifier

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/monoes/mono-agent/internal/doomsday/finish"
)

// The canaries are the proof a confiner is more than a claim (doc.go). Each is a plan run through the SAME placement the real
// criteria get (finish.Sandbox, placeBoth, the same limits and network denial): the host plants what the plan probes, runs the
// plan, and reads the verdict from what the sandbox measured. A fence canary must END in fail: the program ran (it printed its
// marker first) and was refused. A canary that never started proves nothing: that is "error", never a pass. The positive control
// C0 must pass, or the confiner "works" only by breaking everything.

// CanaryVerdict is what one canary showed.
type CanaryVerdict string

const (
	// CanaryHeld: the fence held (or the control passed).
	CanaryHeld CanaryVerdict = "held"
	// CanaryBroken: the program ran and was NOT refused (or the control was refused), or a unit was left behind.
	CanaryBroken CanaryVerdict = "broken"
	// CanaryError: the canary did not run properly (never started, no marker, hung): the fence is unproven.
	CanaryError CanaryVerdict = "error"
	// CanaryNotRun: a tool it needs is missing, or the confiner cannot say what it left behind: fail closed.
	CanaryNotRun CanaryVerdict = "not-run"
)

// CanaryResult is one canary's outcome.
type CanaryResult struct {
	ID, Name string
	// Heavy canaries (C8 to C11) take seconds and memory; a host runs them at start and then at most every ten minutes.
	Heavy bool
	// Optional: not-run does not fail the self test (the setsid variant, where setsid does not exist).
	Optional bool
	Verdict  CanaryVerdict
	Detail   string
}

// Held says whether the fence held.
func (r CanaryResult) Held() bool { return r.Verdict == CanaryHeld }

// CanariesHold is nil when every canary that ran held and at least one ran. The error names every one that did not (by id and
// name, with what was seen), never says "unconfined".
func CanariesHold(rs []CanaryResult) error {
	if len(rs) == 0 {
		return errors.New("no canary ran, so the confinement is unproven")
	}
	var bad []string
	for _, r := range rs {
		if r.Held() || (r.Optional && r.Verdict == CanaryNotRun) {
			continue
		}
		bad = append(bad, fmt.Sprintf("%s (%s): %s: %s", r.ID, r.Name, r.Verdict, r.Detail))
	}
	if len(bad) > 0 {
		return fmt.Errorf("canary check failed: %s", strings.Join(bad, "; "))
	}
	return nil
}

// CanaryRun is what running one canary criterion measured.
type CanaryRun struct {
	Result   string // the finish harness's closed list: pass, fail, timeout, error, oversize, tainted
	ExitCode int
	Stdout   string // the excerpt the harness kept (the marker is first)
	Detail   string
}

// CanaryExec runs one canary criterion with its own timeout. The default runs it through the real sandbox and the confiner; a
// test passes a fake to exercise the verdict logic without starting anything.
type CanaryExec func(ctx context.Context, c Criterion, timeout time.Duration) CanaryRun

// CanaryOptions tunes a run. The zero value is the production run: everything, through the real sandbox.
type CanaryOptions struct {
	// SkipHeavy leaves out C8 to C11. Only, when not empty, runs just those ids.
	SkipHeavy bool
	Only      []string
	// Exec, Leftovers and HasTool are test seams (see CanaryExec). Leftovers defaults to the confiner's LeftoverChecker.
	Exec      CanaryExec
	Leftovers func(ctx context.Context) ([]string, error)
	HasTool   func(name string) bool
	// HomeDir, RuntimeDir and TmpDir are where the host plants what C3 and C4 probe: the person's home, $XDG_RUNTIME_DIR and
	// the temp dir by default.
	HomeDir, RuntimeDir, TmpDir string
	// ShmDir is the host's shared-memory folder (/dev/shm by default), where C13 plants a marker.
	ShmDir string
}

// ManagerEnvPlanter is an optional part of a Confiner that starts its programs through a manager with an environment of its own
// (systemd's user manager): C12 puts a variable there and expects no program to see it. The returned function takes it out again
// by name.
type ManagerEnvPlanter interface {
	PlantManagerEnv(ctx context.Context, name, value string) (remove func(), err error)
}

// canaryStep is one program of a canary.
type canaryStep struct {
	spec      string
	want      string // pass, fail or timeout
	timeout   time.Duration
	leftovers bool         // after it, nothing of the unit may be left
	after     func() error // an extra check on the host's side (a file that must not exist)
	tools     []string     // system programs the spec needs
}

type canarySpec struct {
	id, name        string
	heavy, optional bool
	steps           func(w *canaryWorld) ([]canaryStep, error)
}

// envAllow are the variable names a program may see: the ones the sandbox gives it and the few systemd puts in every service.
const envAllow = `PATH|HOME|TMPDIR|XDG_CACHE_HOME|XDG_CONFIG_HOME|XDG_DATA_HOME|LANG|PWD|OLDPWD|SHLVL|_|INVOCATION_ID|JOURNAL_STREAM|SYSTEMD_EXEC_PID|MEMORY_PRESSURE_WATCH|MEMORY_PRESSURE_WRITE`

const (
	canaryLight = 20 * time.Second
	canaryHeavy = 60 * time.Second
)

func sh(id, script string, args ...string) string {
	out := "sh -c '" + "echo canary-ran:" + id + "; " + script + "' sh"
	for _, a := range args {
		out += " '" + a + "'"
	}
	return out
}

func canaryTable() []canarySpec {
	one := func(s canaryStep) func(*canaryWorld) ([]canaryStep, error) {
		return func(*canaryWorld) ([]canaryStep, error) { return []canaryStep{s}, nil }
	}
	return []canarySpec{
		{id: "C0", name: "control: read the working copy, write the scratch area", steps: one(canaryStep{
			spec: sh("C0", `cat marker.txt && touch "$TMPDIR/ok" && test -f "$TMPDIR/ok"`), want: finish.ResPass, timeout: canaryLight, tools: []string{"sh", "cat", "touch", "test"}})},
		{id: "C1", name: "read a file outside the work area", steps: func(w *canaryWorld) ([]canaryStep, error) {
			return []canaryStep{{spec: sh("C1", `cat "$1"`, w.file), want: finish.ResFail, timeout: canaryLight, tools: []string{"sh", "cat"}}}, w.err("C1")
		}},
		{id: "C2", name: "write a file outside the work area", steps: func(w *canaryWorld) ([]canaryStep, error) {
			return []canaryStep{{spec: sh("C2", `touch "$1"`, w.outside), want: finish.ResFail, timeout: canaryLight, tools: []string{"sh", "touch"},
				after: func() error {
					if _, err := os.Lstat(w.outside); err == nil {
						return errors.New("the file outside the work area exists after the run")
					}
					return nil
				}}}, w.err("C2")
		}},
		{id: "C3", name: "host unix sockets", steps: func(w *canaryWorld) ([]canaryStep, error) {
			return []canaryStep{{spec: sh("C3", `for p in "$@"; do test -S "$p" && exit 0; done; exit 1`, w.sockets...), want: finish.ResFail, timeout: canaryLight, tools: []string{"sh", "test"}}}, w.err("C3")
		}},
		{id: "C4", name: "secrets by path", steps: func(w *canaryWorld) ([]canaryStep, error) {
			return []canaryStep{{spec: sh("C4", `for p in "$@"; do test -e "$p" && exit 0; done; exit 1`, w.secrets...), want: finish.ResFail, timeout: canaryLight, tools: []string{"sh", "test"}}}, w.err("C4")
		}},
		{id: "C5", name: "environment secrets and /proc of other processes", steps: func(w *canaryWorld) ([]canaryStep, error) {
			return []canaryStep{{spec: sh("C5", `env | grep -q MONOAGENT_CANARY && exit 0; grep -aq "$2"= "$1" && exit 0; exit 1`, "/proc/"+strconv.Itoa(w.secretPID)+"/environ", w.secretVar),
				want: finish.ResFail, timeout: canaryLight, tools: []string{"sh", "env", "grep"}}}, w.err("C5")
		}},
		{id: "C6", name: "namespaces differ from the host's", steps: func(w *canaryWorld) ([]canaryStep, error) {
			return []canaryStep{{spec: sh("C6", `for kv in "$@"; do n=${kv%%=*}; v=${kv#*=}; [ "$(readlink /proc/self/ns/$n)" = "$v" ] && { echo same:$n; exit 0; }; done; exit 1`, w.nsLinks...),
				want: finish.ResFail, timeout: canaryLight, tools: []string{"sh", "readlink"}}}, w.err("C6")
		}},
		{id: "C7", name: "network interfaces", steps: one(canaryStep{
			spec: sh("C7", `ls /sys/class/net | grep -v -x lo | grep -q .`), want: finish.ResFail, timeout: canaryLight, tools: []string{"sh", "ls", "grep"}})},
		{id: "C8", name: "fork bomb", heavy: true, steps: one(canaryStep{
			spec: sh("C8", `i=0; while [ $i -lt 2000 ]; do sleep 30 & i=$((i+1)); done; n=0; for d in /proc/[0-9]*; do read c 2>/dev/null < $d/comm && [ "$c" = sleep ] && n=$((n+1)); done; [ $n -ge 1000 ] && exit 0; exit 1`),
			want: finish.ResFail, timeout: canaryHeavy, leftovers: true, tools: []string{"sh", "sleep"}})},
		{id: "C9", name: "memory hog", heavy: true, steps: one(canaryStep{
			spec: sh("C9", `x=$(head -c 3000000000 /dev/zero | tr "\0" a)`), want: finish.ResFail, timeout: canaryHeavy, leftovers: true, tools: []string{"sh", "head", "tr"}})},
		{id: "C10", name: "a child outlives the criterion", heavy: true, steps: one(canaryStep{
			spec: sh("C10", `sleep 600 & exit 0`), want: finish.ResPass, timeout: canaryLight, leftovers: true, tools: []string{"sh", "sleep"}})},
		{id: "C10b", name: "a setsid child outlives the criterion", heavy: true, optional: true, steps: one(canaryStep{
			spec: sh("C10b", `setsid sleep 600 & exit 0`), want: finish.ResPass, timeout: canaryLight, leftovers: true, tools: []string{"sh", "setsid", "sleep"}})},
		{id: "C11", name: "wall-clock timeout kills the unit", heavy: true, steps: one(canaryStep{
			spec: sh("C11", `exec sleep 600`), want: finish.ResTimeout, timeout: 5 * time.Second, leftovers: true, tools: []string{"sh", "sleep"}})},
		{id: "C12", name: "the manager's environment does not reach the program", steps: func(w *canaryWorld) ([]canaryStep, error) {
			return []canaryStep{{spec: sh("C12", `env | cut -d= -f1 | grep -v -x -E "`+envAllow+`" | grep -q .`), want: finish.ResFail, timeout: canaryLight, tools: []string{"sh", "env", "cut", "grep"}}}, w.err("C12")
		}},
		{id: "C13", name: "the host's /dev/shm", steps: func(w *canaryWorld) ([]canaryStep, error) {
			return []canaryStep{{spec: sh("C13", `touch "$2"; test -e "$1" && exit 0; exit 1`, w.shmMarker, w.shmProbe), want: finish.ResFail, timeout: canaryLight, tools: []string{"sh", "touch", "test"},
				after: func() error {
					if _, err := os.Lstat(w.shmProbe); err == nil {
						return errors.New("a file the program wrote in /dev/shm exists on the host afterwards")
					}
					return nil
				}}}, w.err("C13")
		}},
		{id: "C14", name: "the host's terminals (/dev/pts)", steps: func(w *canaryWorld) ([]canaryStep, error) {
			return []canaryStep{{spec: sh("C14", `for p in "$@" /dev/pts/[0-9]*; do test -e "$p" && exit 0; done; exit 1`, w.ptySlaves...), want: finish.ResFail, timeout: canaryLight, tools: []string{"sh", "test"}}}, w.err("C14")
		}},
	}
}

// RunCanaries runs every canary through conf, placed exactly as the real criteria are, with the working area made in
// scratchBase and the host's plants beside it (in its parent). It starts a fork bomb and a memory hog: a host runs it on a machine
// meant for that.
func RunCanaries(ctx context.Context, conf Confiner, scratchBase string) []CanaryResult {
	return RunCanariesWith(ctx, conf, scratchBase, CanaryOptions{})
}

// RunCanariesWith is RunCanaries with options.
func RunCanariesWith(ctx context.Context, conf Confiner, scratchBase string, o CanaryOptions) []CanaryResult {
	var specs []canarySpec
	for _, s := range canaryTable() {
		if o.SkipHeavy && s.heavy {
			continue
		}
		if len(o.Only) > 0 && !contains(o.Only, s.id) {
			continue
		}
		specs = append(specs, s)
	}
	w := newCanaryWorld(ctx, scratchBase, o, conf)
	defer w.close()
	run := o.Exec
	if run == nil {
		r, closeRun := newRealCanaryExec(conf, scratchBase)
		defer closeRun()
		run = r
	}
	leftovers := o.Leftovers
	if leftovers == nil {
		if lc, ok := conf.(LeftoverChecker); ok {
			leftovers = lc.Leftovers
		}
	}
	hasTool := o.HasTool
	if hasTool == nil {
		hasTool = toolOnPath
	}
	var out []CanaryResult
	for _, s := range specs {
		r := CanaryResult{ID: s.id, Name: s.name, Heavy: s.heavy, Optional: s.optional}
		if ctx.Err() != nil {
			r.Verdict, r.Detail = CanaryError, "cancelled"
			out = append(out, r)
			continue
		}
		steps, err := s.steps(w)
		if err != nil {
			r.Verdict, r.Detail = CanaryError, "the host could not plant it: "+err.Error()
			out = append(out, r)
			continue
		}
		r.Verdict = CanaryHeld
		for _, st := range steps {
			if v, d := runStep(ctx, s.id, st, run, leftovers, hasTool); v != CanaryHeld {
				r.Verdict, r.Detail = v, d
				break
			}
		}
		out = append(out, r)
	}
	return out
}

func contains(xs []string, s string) bool {
	for _, x := range xs {
		if x == s {
			return true
		}
	}
	return false
}

func toolOnPath(name string) bool {
	for _, d := range filepath.SplitList(finish.SystemPath) {
		if st, err := os.Stat(filepath.Join(d, name)); err == nil && st.Mode().IsRegular() && st.Mode()&0o111 != 0 {
			return true
		}
	}
	return false
}

func runStep(ctx context.Context, id string, st canaryStep, run CanaryExec, leftovers func(context.Context) ([]string, error), hasTool func(string) bool) (CanaryVerdict, string) {
	for _, tool := range st.tools {
		if !hasTool(tool) {
			return CanaryNotRun, tool + " is not on the system path"
		}
	}
	if st.leftovers && leftovers == nil {
		return CanaryNotRun, "the confiner cannot say what a program left behind"
	}
	m := run(ctx, Criterion{ID: strings.ToLower(id), Kind: "command", Spec: st.spec, Expected: "exit:0"}, st.timeout)
	marker := "canary-ran:" + id
	if !strings.Contains(m.Stdout, marker) {
		return CanaryError, "the program did not print its marker, so it may never have started (" + m.Result + "): " + m.Detail
	}
	switch {
	case st.want == finish.ResPass && m.Result == finish.ResPass:
	case st.want == finish.ResPass:
		return CanaryBroken, "it must pass and ended " + m.Result + ": " + m.Detail
	case st.want == finish.ResTimeout && m.Result == finish.ResTimeout:
	case st.want == finish.ResTimeout:
		return CanaryBroken, "it must be killed at its deadline and ended " + m.Result
	case m.Result == finish.ResPass:
		return CanaryBroken, "the program ran and was not refused"
	case m.Result == finish.ResFail && m.ExitCode != 0:
	default:
		return CanaryError, "it must end refused (fail, non-zero exit) and ended " + m.Result + " with exit " + strconv.Itoa(m.ExitCode) + ": " + m.Detail
	}
	if st.after != nil {
		if err := st.after(); err != nil {
			return CanaryBroken, err.Error()
		}
	}
	if st.leftovers {
		left, err := leftovers(ctx)
		if err != nil {
			return CanaryError, "what the unit left behind could not be read: " + err.Error()
		}
		if len(left) > 0 {
			return CanaryBroken, "left behind after the release: " + strings.Join(left, ", ")
		}
	}
	return CanaryHeld, ""
}

// exec makes the real runner: one sealed snapshot and a working area per run, each criterion through finish.Sandbox with the
// same placement a check gets.
func newRealCanaryExec(conf Confiner, scratchBase string) (CanaryExec, func()) {
	dir, derr := os.MkdirTemp(scratchBase, "canary-")
	closeFn := func() {
		if dir != "" {
			finish.RemoveTree(dir)
		}
	}
	fail := func(f string, a ...any) (CanaryExec, func()) {
		return func(context.Context, Criterion, time.Duration) CanaryRun {
			return CanaryRun{Result: finish.ResError, Detail: fmt.Sprintf(f, a...)}
		}, closeFn
	}
	if derr != nil {
		dir = ""
		return fail("no scratch area for the canaries: %v", derr)
	}
	src, work := filepath.Join(dir, "src"), filepath.Join(dir, "work")
	for _, d := range []string{src, work} {
		if err := os.Mkdir(d, 0o700); err != nil {
			return fail("no scratch area for the canaries: %v", err)
		}
	}
	if err := os.WriteFile(filepath.Join(src, "marker.txt"), []byte("canary data\n"), 0o600); err != nil {
		return fail("%v", err)
	}
	snap := filepath.Join(dir, "snap")
	tree, err := finish.Snapshot(src, snap)
	if err != nil {
		return fail("the canary snapshot: %v", err)
	}
	gp := &GroupPlace{}
	closeAll := func() { gp.Close(); closeFn() }
	return func(ctx context.Context, c Criterion, timeout time.Duration) CanaryRun {
		plan, perr := finish.ParsePlan(c.Kind, c.Spec, c.Expected)
		if perr != nil {
			return CanaryRun{Result: finish.ResError, Detail: perr.Error()}
		}
		sb := finish.Sandbox{RequireNetDeny: true, Timeout: timeout, OutCap: OutputCap, Place: placeBoth(gp, conf), Scratch: work}
		if !sb.NetEnforceable() {
			return CanaryRun{Result: finish.ResError, Detail: "the platform cannot deny the network, so no program is started"}
		}
		ir, _ := conf.(InfraReporter)
		if ir != nil {
			ir.TakeInfra()
		}
		m := sb.Run(ctx, snap, tree, plan)
		if ir != nil {
			if infra := ir.TakeInfra(); len(infra) > 0 {
				return CanaryRun{Result: finish.ResError, Detail: Excerpt("the confinement failed: "+strings.Join(infra, "; "), detailCap)}
			}
		}
		return CanaryRun{Result: closedResult(m.Result), ExitCode: m.ExitCode, Stdout: m.Stdout, Detail: Excerpt(m.Detail, detailCap)}
	}, closeAll
}
