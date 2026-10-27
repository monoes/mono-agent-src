package doctor

import (
	"bytes"
	"context"
	"strings"
	"testing"

	"github.com/monoes/mono-agent/internal/health"
)

func TestApplyReportFixesSkipsOptional(t *testing.T) {
	applied := map[string]bool{}
	reg := health.NewRegistry(nil, []health.Fix{
		{FixInfo: health.FixInfo{ID: "a", Safety: health.SafetyAuto},
			Apply: func(context.Context, *health.Env, func(string)) error { applied["a"] = true; return nil }},
		{FixInfo: health.FixInfo{ID: "rt", Safety: health.SafetyConfirm, Optional: true},
			ApplyArg: func(_ context.Context, _ *health.Env, arg string, _ func(string)) error {
				applied[arg] = true
				return nil
			}},
	})
	rep := &health.Report{Results: []health.Result{
		{ID: "x", Status: health.StatusFail, Fix: &health.FixInfo{ID: "a"}},
		{ID: "y", Status: health.StatusInfo, Fix: &health.FixInfo{ID: "rt:claude", Optional: true}},
	}}
	t.Setenv("HOME", t.TempDir())
	outcomes := applyReportFixes(context.Background(), testDeps(false), reg, rep,
		map[string]bool{}, func(health.FixInfo) bool { return true }, func(string) {}, false)
	if !applied["a"] || applied["claude"] {
		t.Fatalf("applied %v; optional runtime install must not run under --fix", applied)
	}
	if len(outcomes) != 1 || outcomes[0].ID != "a" {
		t.Fatalf("outcomes %v", outcomes)
	}
}

// The footer counts only what --fix applies, each fix once however many
// rows offer it; optional fixes are named
// separately, since --fix skips them (a runtime that is not installed is
// folded into one line with its own hint, and not counted).
func TestDoctorFooterSeparatesOptionalFixes(t *testing.T) {
	var out bytes.Buffer
	rep := &health.Report{Summary: map[health.Status]int{}, Results: []health.Result{
		{ID: "a", Group: "core", Title: "A", Status: health.StatusFail, Fix: &health.FixInfo{ID: "a.fix", Safety: health.SafetyAuto}},
		{ID: "b", Group: "runtimes", Title: "B", Status: health.StatusInfo, Fix: &health.FixInfo{ID: "rt:b", Safety: health.SafetyConfirm, Optional: true}},
		{ID: "c", Group: "runtimes", Title: "C", Status: health.StatusWarn, Fix: &health.FixInfo{ID: "rt:c", Safety: health.SafetyConfirm, Optional: true}},
		{ID: "d", Group: "browser", Title: "D", Status: health.StatusWarn, Fix: &health.FixInfo{ID: "m", Safety: health.SafetyManual, Command: "do it"}},
		{ID: "e", Group: "browser", Title: "E", Status: health.StatusWarn, Fix: &health.FixInfo{ID: "a.fix", Safety: health.SafetyAuto}},
	}}
	printDoctorReport(&out, rep, false)
	s := out.String()
	if !strings.Contains(s, "apply 1 fix(es)") || !strings.Contains(s, "1 optional fix(es) are not applied by --fix") {
		t.Fatalf("footer:\n%s", s)
	}
	if got := doctorFooter(0, 1, false); len(got) != 1 || strings.Contains(got[0], "--fix` to") {
		t.Fatalf("only optional fixes must not say run --fix: %q", got)
	}
	if got := doctorFooter(3, 0, true); len(got) != 0 {
		t.Fatalf("after --fix: %q", got)
	}
}

func TestRunHealthPassesTheAgentAccount(t *testing.T) {
	var seen string
	cfg := testDeps(false)
	build := cfg.NewEnv
	cfg.NewEnv = func(p []string) (*health.Env, func()) {
		env, closeEnv := build(p)
		return env, func() { seen = env.AgentAccount; closeEnv() }
	}
	t.Setenv(health.AgentAccountEnv, "fromenv")
	runHealth(context.Background(), cfg, health.NewRegistry(nil, nil), health.Options{})
	if seen != "fromenv" {
		t.Errorf("env var: %q", seen)
	}
	cfg.agentAccount = "fromflag"
	runHealth(context.Background(), cfg, health.NewRegistry(nil, nil), health.Options{})
	if seen != "fromflag" {
		t.Errorf("flag must win: %q", seen)
	}
}

// A workflow file that appeared by itself is adopted only when an operator names it: no "fix
// everything" path (doctor --fix --yes, setup --yes, the app's Fix issues) runs the adopt fix.
func TestApplyReportFixesNeverAdoptsAWorkflowFile(t *testing.T) {
	reg := health.Default()
	adopt, ok := reg.Fix("workflows.publications.adopt:wf-new:default:0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef")
	if !ok {
		t.Fatal("the adopt fix is not registered")
	}
	rep := &health.Report{Results: []health.Result{
		{ID: "workflows.publications.adopt.wf-new", Status: health.StatusInfo, Fix: &adopt.FixInfo},
	}}
	t.Setenv("HOME", t.TempDir())
	outcomes := applyReportFixes(context.Background(), testDeps(false), reg, rep,
		map[string]bool{}, func(health.FixInfo) bool { return true }, func(string) {}, false)
	if len(outcomes) != 0 {
		t.Fatalf("a fix-everything pass touched the adopt fix: %v", outcomes)
	}
}
