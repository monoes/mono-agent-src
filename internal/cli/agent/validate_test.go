package agent

import (
	"bufio"
	"context"
	"encoding/json"
	"io"
	"math"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/monoes/mono-agent/internal/agentroster"
	"github.com/monoes/mono-agent/internal/monomind"
	"github.com/monoes/mono-agent/internal/storage"
)

// writeRosterMonomind writes a fake monomind with three runtimes: alpha
// (signed in, answers "ok" at $0.002), beta (signed out: its turn fails the
// way a real logged-out runtime does) and gamma (not installed). alpha's
// version is read from versionFile, so a test can "upgrade" it.
func writeRosterMonomind(t *testing.T) (versionFile, argsLog string) {
	t.Helper()
	if runtime.GOOS == "windows" {
		t.Skip("fake monomind is a shell script")
	}
	dir := t.TempDir()
	bin := filepath.Join(dir, "monomind")
	versionFile = filepath.Join(dir, "alpha.version")
	argsLog = filepath.Join(dir, "args.log")
	if err := os.WriteFile(versionFile, []byte("1.0.0"), 0o644); err != nil {
		t.Fatal(err)
	}
	script := `#!/bin/sh
echo "$*" >> '` + argsLog + `'
if [ "$1" = "--version" ]; then echo '{"v":1,"version":"9.0.0","min_caller":"1.0.0","capabilities":["agent-exec","agent-scan","org-json-v1"]}'; exit 0; fi
if [ "$1" = "agent" ] && [ "$2" = "scan" ]; then
  v=$(cat '` + versionFile + `')
  echo '{"v":1,"agents":[{"id":"alpha","installed":true,"binary":"/bin/true","version":"'"$v"'"},{"id":"beta","installed":true,"binary":"/bin/true","version":"2.0.0","login_hint":"beta login"},{"id":"gamma","installed":false}]}'
  exit 0
fi
if [ "$1" = "agent" ] && [ "$2" = "exec" ]; then
  rt=""; prev=""
  for a in "$@"; do [ "$prev" = "--runtime" ] && rt="$a"; prev="$a"; done
  echo '{"v":1,"type":"start","runtime":"'"$rt"'","cwd":"/w","pid":1}'
  if [ "$rt" = "beta" ]; then
    echo '{"v":1,"type":"error","code":"runner-error","fatal":true,"message":"BetaRunner: beta failed (exit 1): Not signed in. Run: beta login"}'
    echo '{"v":1,"type":"done","exit_code":1}'
    exit 1
  fi
  echo '{"v":1,"type":"result","subtype":"success","is_error":false,"stop_reason":"end_turn","text":"ok","cost_usd":0.002}'
  echo '{"v":1,"type":"done","exit_code":0}'
  exit 0
fi
echo "unsupported: $*" >&2
exit 2
`
	if err := os.WriteFile(bin, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv(monomind.EnvOverride, bin)
	monomind.ResetCapabilityCache()
	t.Cleanup(monomind.ResetCapabilityCache)
	return versionFile, argsLog
}

// runAgentCLI runs `agent <args>` with --json and returns what it printed.
func runAgentCLI(t *testing.T, dbPath string, args ...string) (string, error) {
	t.Helper()
	var err error
	out := captureStdout(t, func() {
		cmd := NewCommand(testDeps(dbPath, true))
		cmd.SetArgs(args)
		cmd.SilenceErrors, cmd.SilenceUsage = true, true
		err = cmd.Execute()
	})
	return out, err
}

func validateLines(t *testing.T, out string) []agentroster.Line {
	t.Helper()
	var lines []agentroster.Line
	sc := bufio.NewScanner(strings.NewReader(out))
	for sc.Scan() {
		var l agentroster.Line
		if err := json.Unmarshal(sc.Bytes(), &l); err != nil {
			t.Fatalf("not an NDJSON line: %q (%v)", sc.Text(), err)
		}
		lines = append(lines, l)
	}
	return lines
}

func rosterByRuntime(t *testing.T, dbPath string) map[string]agentroster.RuntimeRoster {
	t.Helper()
	out, err := runAgentCLI(t, dbPath, "roster")
	if err != nil {
		t.Fatalf("roster: %v\n%s", err, out)
	}
	var raw struct {
		Runtimes []agentroster.RuntimeRoster `json:"runtimes"`
	}
	if err := json.Unmarshal([]byte(out), &raw); err != nil {
		t.Fatalf("roster output %q: %v", out, err)
	}
	by := map[string]agentroster.RuntimeRoster{}
	for _, rr := range raw.Runtimes {
		by[rr.Runtime] = rr
	}
	return by
}

// TestAgentValidateAllEndToEnd is #231's phase-0 matrix through the CLI:
// validate --all covers every installed runtime, a signed-out runtime is
// stored as auth with its sign-in hint, --dry-run prices the plan from the
// previous costs without calling a model, and a runtime version change
// makes its models stale (and back in a --stale-only run).
func TestAgentValidateAllEndToEnd(t *testing.T) {
	dbPath := newTestDBPath(t)
	versionFile, argsLog := writeRosterMonomind(t)
	execCalls := func() int {
		b, _ := os.ReadFile(argsLog)
		return strings.Count(string(b), "agent exec")
	}

	// A dry run before anything is known: one call per installed runtime,
	// no cost known yet, nothing run.
	out, err := runAgentCLI(t, dbPath, "validate", "--all", "--dry-run")
	if err != nil {
		t.Fatalf("dry run: %v\n%s", err, out)
	}
	lines := validateLines(t, out)
	if len(lines) != 1 || lines[0].Type != "validate.plan" {
		t.Fatalf("a dry run prints only the plan, got %+v", lines)
	}
	if p := lines[0].Plan; p.Calls != 2 || p.UnknownCost != 2 || p.EstCostUSD != 0 {
		t.Errorf("first plan = calls %d, $%v, unknown %d; want 2, 0, 2", p.Calls, p.EstCostUSD, p.UnknownCost)
	}
	if execCalls() != 0 {
		t.Fatal("a dry run must not call a model")
	}

	// The real run: both installed runtimes, gamma untouched.
	out, err = runAgentCLI(t, dbPath, "validate", "--all")
	if err != nil {
		t.Fatalf("validate --all: %v\n%s", err, out)
	}
	results := map[string]*agentroster.Result{}
	var summary *agentroster.Summary
	for _, l := range validateLines(t, out) {
		switch l.Type {
		case "validate.result":
			results[l.Result.Runtime] = l.Result
		case "validate.done":
			summary = l.Summary
		}
	}
	if len(results) != 2 || results["gamma"] != nil {
		t.Fatalf("results = %v, want alpha and beta only", results)
	}
	if r := results["alpha"]; r.Status != agentroster.StatusOK || !r.HasCost || r.RuntimeVersion != "1.0.0" {
		t.Errorf("alpha = %+v", r)
	}
	if r := results["beta"]; r.Status != agentroster.StatusAuth || !strings.Contains(r.Detail, "beta login") {
		t.Errorf("a signed-out runtime must be auth with its message, got %+v", r)
	}
	if summary == nil || summary.OK != 1 || summary.Failed != 1 {
		t.Errorf("summary = %+v", summary)
	}
	if n := execCalls(); n != 2 {
		t.Errorf("exec calls = %d, want 2", n)
	}

	roster := rosterByRuntime(t, dbPath)
	if b := roster["beta"]; b.LoginHint != "beta login" || len(b.Models) != 1 || b.Models[0].State != agentroster.StateFailed {
		t.Errorf("beta roster = %+v, want failed with the login hint", b)
	}
	if a := roster["alpha"]; a.Ready != 1 || a.Models[0].State != agentroster.StateReady {
		t.Errorf("alpha roster = %+v", a)
	}

	// Now the dry run prices alpha from its last call; beta is unknown.
	out, _ = runAgentCLI(t, dbPath, "validate", "--dry-run")
	if p := validateLines(t, out)[0].Plan; p.Calls != 2 || math.Abs(p.EstCostUSD-0.002) > 1e-9 || p.UnknownCost != 1 {
		t.Errorf("second plan = calls %d, $%v, unknown %d; want 2, 0.002, 1", p.Calls, p.EstCostUSD, p.UnknownCost)
	} else if len(p.SignIn) != 1 || p.SignIn[0] != (agentroster.SignInNote{Runtime: "beta", LoginHint: "beta login"}) {
		t.Errorf("second plan sign-in notes = %+v, want beta with its hint", p.SignIn)
	}
	// --stale-only skips the ready alpha.
	out, _ = runAgentCLI(t, dbPath, "validate", "--stale-only", "--dry-run")
	if p := validateLines(t, out)[0].Plan; len(p.Targets) != 1 || p.Targets[0].Runtime != "beta" {
		t.Errorf("stale-only plan before the upgrade = %+v", p.Targets)
	}

	// alpha is upgraded: its pass no longer counts as ready.
	if err := os.WriteFile(versionFile, []byte("1.1.0"), 0o644); err != nil {
		t.Fatal(err)
	}
	a := rosterByRuntime(t, dbPath)["alpha"]
	if a.Version != "1.1.0" || a.Ready != 0 || a.Models[0].State != agentroster.StateStale || a.Models[0].StaleReason != "version" {
		t.Errorf("alpha after a version change = %+v", a)
	}
	out, _ = runAgentCLI(t, dbPath, "validate", "--stale-only", "--dry-run")
	if p := validateLines(t, out)[0].Plan; len(p.Targets) != 2 {
		t.Errorf("stale-only plan after the upgrade must include alpha again, got %+v", p.Targets)
	}
	// Re-validating it records the new version and makes it ready again.
	if out, err = runAgentCLI(t, dbPath, "validate", "--runtime", "alpha"); err != nil {
		t.Fatalf("re-validate alpha: %v\n%s", err, out)
	}
	if a := rosterByRuntime(t, dbPath)["alpha"]; a.Ready != 1 || a.Models[0].RuntimeVersion != "1.1.0" {
		t.Errorf("alpha after re-validating = %+v", a)
	}
}

func TestAgentValidateAllRejectsAFilter(t *testing.T) {
	dbPath := newTestDBPath(t)
	writeRosterMonomind(t)
	if _, err := runAgentCLI(t, dbPath, "validate", "--all", "--runtime", "alpha"); exitCodeFor(err) != 3 {
		t.Errorf("--all with --runtime = %v, want an invalid-input error", err)
	}
}

func TestPlanCost(t *testing.T) {
	t.Parallel()
	cases := []struct {
		plan agentroster.Plan
		want string
	}{
		{agentroster.Plan{EstCostUSD: 0.0123}, "≈ $0.0123"},
		{agentroster.Plan{EstCostUSD: 0.2, TableEstimated: 12}, "≈ $0.2000 (12 priced from the built-in table)"},
		{agentroster.Plan{EstCostUSD: 0.2, TableEstimated: 2, UnknownCost: 3}, "≈ $0.2000 (2 priced from the built-in table; + 3 with unknown cost)"},
		{agentroster.Plan{UnknownCost: 3}, "≈ <$0.0001 (+ 3 with unknown cost)"},
	}
	for _, c := range cases {
		if got := planCost(&c.plan); got != c.want {
			t.Errorf("planCost(%+v) = %q, want %q", c.plan, got, c.want)
		}
	}
}

// The text output shows the sign-in note for the plan and the login hint on
// an auth result (monoes/mono-agent#271, #272).
func TestValidateEmitterShowsSignIn(t *testing.T) {
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	orig := os.Stderr
	os.Stderr = w
	var out string
	func() {
		defer func() { os.Stderr = orig }()
		out = captureStdout(t, func() {
			emit := validateEmitter(false)
			emit(agentroster.Line{Type: "validate.plan", Plan: &agentroster.Plan{Calls: 5, EstCostUSD: 0.1, TableEstimated: 5,
				SignIn: []agentroster.SignInNote{{Runtime: "claude", LoginHint: "claude /login"}}}})
			emit(agentroster.Line{Type: "validate.result", Result: &agentroster.Result{Runtime: "claude", Model: "opus",
				Status: agentroster.StatusAuth, Detail: "Not logged in", LoginHint: "claude /login"}})
		})
	}()
	w.Close()
	errOut, _ := io.ReadAll(r)
	if !strings.Contains(string(errOut), "claude isn't signed in, so its model list may be incomplete; sign in with: claude /login") {
		t.Errorf("stderr = %q", errOut)
	}
	if !strings.Contains(out, "5 test calls, ≈ $0.1000 (5 priced from the built-in table)") || !strings.Contains(out, "(sign in: claude /login)") {
		t.Errorf("stdout = %q", out)
	}
}

// `agent roster` shows an auth row's login hint in the table and the JSON.
func TestAgentRosterShowsLoginHint(t *testing.T) {
	dbPath := newTestDBPath(t)
	db, err := storage.NewDatabase(dbPath)
	if err != nil {
		t.Fatal(err)
	}
	err = agentroster.Save(context.Background(), db.DB, agentroster.Result{Runtime: "claude", Model: "opus",
		Status: agentroster.StatusAuth, Detail: "Not logged in", LoginHint: "claude /login", ValidatedAt: time.Now()})
	db.Close()
	if err != nil {
		t.Fatal(err)
	}
	for _, jsonOut := range []bool{false, true} {
		d := testDeps(dbPath, jsonOut)
		cmd := d.newAgentRosterCmd()
		cmd.SetArgs([]string{"--no-scan"})
		out := captureStdout(t, func() {
			if err := cmd.Execute(); err != nil {
				t.Errorf("roster: %v", err)
			}
		})
		want := "claude /login"
		if jsonOut {
			want = `"login_hint": "claude /login"`
		}
		if !strings.Contains(out, want) || (!jsonOut && !strings.Contains(out, "SIGN IN")) {
			t.Errorf("json=%v: output = %q, want %q", jsonOut, out, want)
		}
	}
}
