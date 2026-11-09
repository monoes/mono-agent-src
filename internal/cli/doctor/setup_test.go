package doctor

import (
	"bufio"
	"bytes"
	"context"
	"fmt"
	"github.com/monoes/mono-agent/internal/testhome"
	"reflect"
	"strings"
	"testing"

	"github.com/monoes/mono-agent/internal/health"
	"github.com/zalando/go-keyring"
)

func optRow(id, fixID string) health.Result {
	return health.Result{ID: id, Status: health.StatusInfo, Fix: &health.FixInfo{ID: fixID, Optional: true}}
}

func TestOptionalFixIDs(t *testing.T) {
	noRuntime := &health.Report{Results: []health.Result{
		{ID: health.CheckRuntimes, Status: health.StatusFail},
		optRow("runtimes.claude", "runtimes.install:claude"),
		optRow("runtimes.codex", "runtimes.install:codex"),
		optRow(health.CheckAutostart, health.FixAutostart),
		optRow(health.CheckMCP, health.FixMCPRegister),
	}}
	silent := func(string) string { return "" }

	// Non-interactive, no flags: nothing optional happens.
	if got := optionalFixIDs(noRuntime, setupWants{}, silent); len(got.ids) != 0 || len(got.skipped) != 0 {
		t.Fatalf("no flags: %+v", got)
	}
	// Flags.
	got := optionalFixIDs(noRuntime, setupWants{runtimes: []string{"codex"}, autostart: true}, silent)
	if want := []string{"runtimes.install:codex", health.FixAutostart}; !reflect.DeepEqual(got.ids, want) {
		t.Fatalf("flags: %v, want %v", got.ids, want)
	}
	// Interactive answers.
	answers := func(q string) string {
		switch {
		case strings.HasPrefix(q, "No"):
			return "claude"
		case strings.HasPrefix(q, "Start"):
			return "y"
		}
		return "n"
	}
	got = optionalFixIDs(noRuntime, setupWants{}, answers)
	if want := []string{"runtimes.install:claude", health.FixAutostart}; !reflect.DeepEqual(got.ids, want) {
		t.Fatalf("interactive: %v, want %v", got.ids, want)
	}

	// A runtime is already installed: don't ask about runtimes again.
	has := &health.Report{Results: []health.Result{
		{ID: health.CheckRuntimes, Status: health.StatusOK},
		optRow("runtimes.codex", "runtimes.install:codex"),
	}}
	asked := false
	optionalFixIDs(has, setupWants{}, func(string) string { asked = true; return "" })
	if asked {
		t.Fatal("must not offer a runtime when one is installed")
	}
}

// An extra that was asked for but won't be done is reported as skipped,
// with why, instead of being dropped (or reported as applied).
func TestOptionalExtrasReportSkipped(t *testing.T) {
	rep := &health.Report{Results: []health.Result{
		{ID: health.CheckRuntimes, Status: health.StatusOK},
		{ID: "runtimes.claude", Parent: health.CheckRuntimes, Status: health.StatusOK, Summary: "2.0"},
		{ID: "runtimes.codex", Parent: health.CheckRuntimes, Status: health.StatusInfo,
			Fix: &health.FixInfo{ID: "runtimes.install:codex", Optional: true}},
		{ID: "runtimes.cursor", Parent: health.CheckRuntimes, Status: health.StatusInfo, Detail: "see cursor.com"},
		{ID: health.CheckAutostart, Status: health.StatusOK, Summary: "systemd user service"},
		{ID: health.CheckMCP, Status: health.StatusSkip, Summary: "Claude Code not installed"},
	}}
	got := optionalFixIDs(rep, setupWants{runtimes: []string{"claude", "codex", "cursor", "claud"}, autostart: true, mcp: true},
		func(string) string { return "" })
	if want := []string{"runtimes.install:codex"}; !reflect.DeepEqual(got.ids, want) {
		t.Errorf("ids %v, want %v", got.ids, want)
	}
	reasons := map[string]string{}
	for _, o := range got.skipped {
		if o.Outcome != "skipped" {
			t.Errorf("%s: outcome %q", o.ID, o.Outcome)
		}
		reasons[o.ID] = o.Reason
	}
	for id, want := range map[string]string{
		"runtimes.install:claude": "already installed",
		"runtimes.install:cursor": "see cursor.com",
		"runtimes.install:claud":  `unknown runtime "claud" (can be installed: codex)`,
		health.FixAutostart:       "already set up (systemd user service)",
		health.FixMCPRegister:     "Claude Code not installed",
	} {
		if !strings.Contains(reasons[id], want) {
			t.Errorf("%s: reason %q, want %q", id, reasons[id], want)
		}
	}

	// A typo at the runtime prompt is reported too.
	none := &health.Report{Results: []health.Result{
		{ID: health.CheckRuntimes, Status: health.StatusFail},
		{ID: "runtimes.codex", Parent: health.CheckRuntimes, Status: health.StatusInfo,
			Fix: &health.FixInfo{ID: "runtimes.install:codex", Optional: true}},
	}}
	got = optionalFixIDs(none, setupWants{}, func(q string) string {
		if strings.HasPrefix(q, "No") {
			return "codx, codex"
		}
		return ""
	})
	if len(got.ids) != 1 || got.ids[0] != "runtimes.install:codex" || len(got.skipped) != 1 ||
		!strings.Contains(got.skipped[0].Reason, `unknown runtime "codx"`) {
		t.Errorf("typo: %+v", got)
	}
}

// Before anyone picks a runtime, the prompt names what each one runs: a
// vendor script's URL, or the npm package.
func TestRuntimePromptNamesWhatRuns(t *testing.T) {
	rep := &health.Report{Results: []health.Result{
		{ID: health.CheckRuntimes, Status: health.StatusFail},
		{ID: "runtimes.hermes", Status: health.StatusInfo, Fix: &health.FixInfo{ID: "runtimes.install:hermes", Optional: true,
			Command: "downloads and runs the vendor installer https://hermes.example/install.sh with bash"}},
		{ID: "runtimes.codex", Status: health.StatusInfo, Fix: &health.FixInfo{ID: "runtimes.install:codex", Optional: true,
			Command: "npm install -g @openai/codex"}},
	}}
	var asked string
	optionalFixIDs(rep, setupWants{}, func(q string) string {
		if strings.HasPrefix(q, "No") {
			asked = q
		}
		return ""
	})
	for _, want := range []string{"https://hermes.example/install.sh", "npm install -g @openai/codex"} {
		if !strings.Contains(asked, want) {
			t.Errorf("runtime prompt %q does not name %q", asked, want)
		}
	}
}

// A confirm fix nobody accepted is listed as not done, with how to accept
// it, so a run with no terminal does not look complete.
func TestManualStepsListDeclinedFixes(t *testing.T) {
	rep := &health.Report{Results: []health.Result{
		{ID: "a", Title: "Workflow daemon", Status: health.StatusWarn, Fix: &health.FixInfo{ID: "services.daemon.start", Label: "Start the workflow daemon", Safety: health.SafetyConfirm}},
		{ID: "b", Title: "Extension", Status: health.StatusWarn, Fix: &health.FixInfo{ID: "x", Command: "load it in the browser", Safety: health.SafetyManual}},
		{ID: "c", Title: "Runtime", Status: health.StatusInfo, Fix: &health.FixInfo{ID: "runtimes.install:qwen", Safety: health.SafetyConfirm, Optional: true}},
	}}
	var out bytes.Buffer
	printManualSteps(&out, rep)
	got := out.String()
	for _, want := range []string{"Start the workflow daemon", "--yes", "load it in the browser"} {
		if !strings.Contains(got, want) {
			t.Errorf("manual steps %q lack %q", got, want)
		}
	}
	if strings.Contains(got, "qwen") {
		t.Errorf("an optional extra is listed as not done: %q", got)
	}
}

// Service fixes wait while anything else is being fixed.
func TestWithoutServiceFixes(t *testing.T) {
	rep := &health.Report{Results: []health.Result{
		{ID: "core.db", Fix: &health.FixInfo{ID: "core.db.migrate"}},
		{ID: "services.daemon", Fix: &health.FixInfo{ID: health.FixDaemonStart}},
	}}
	held := withoutServiceFixes(rep)
	if held.Results[0].Fix == nil || held.Results[1].Fix != nil {
		t.Fatalf("held view: %+v", held.Results)
	}
	if rep.Results[1].Fix == nil {
		t.Fatal("the original report was modified")
	}
}

// A check that offers a new fix every time can't keep setup busy forever,
// and the stop is said out loud.
func TestSetupFixesSaysWhenThePassLimitStopsIt(t *testing.T) {
	keyring.MockInit()
	home := t.TempDir()
	testhome.SetHome(t, home)
	cfg := testDeps(true)
	n := 0
	reg := health.NewRegistry([]health.Check{{ID: "t.endless", Group: "t", Title: "Endless",
		Run: func(context.Context, *health.Env) health.Result {
			n++
			return health.Result{Status: health.StatusWarn, Summary: "more", FixID: fmt.Sprintf("t.fix:%d", n)}
		}}},
		[]health.Fix{{FixInfo: health.FixInfo{ID: "t.fix", Label: "Fix {arg}", Safety: health.SafetyAuto},
			ApplyArg: func(context.Context, *health.Env, string, func(string)) error { return nil }}})
	ctx := context.Background()
	var buf bytes.Buffer
	ev := setupEvents{w: &buf, json: true}
	_, outcomes := setupFixes(ctx, cfg, reg, health.Options{}, runHealth(ctx, cfg, reg, health.Options{}),
		func(health.FixInfo) bool { return false }, ev)
	if len(outcomes) != maxFixPasses+1 {
		t.Errorf("%d fixes, want %d", len(outcomes), maxFixPasses+1)
	}
	if !strings.Contains(buf.String(), "stopped after 5 passes") {
		t.Errorf("no word about the pass limit:\n%s", buf.String())
	}
	if c := strings.Count(buf.String(), `"kind":"fix_start","message":"Fix`); c != maxFixPasses+1 {
		t.Errorf("%d fix_start events:\n%s", c, buf.String())
	}
}

// setup's prompts and fixPrompter's read one buffer: given a
// *bufio.Reader, bufio.NewReader (what fixPrompter calls on InOrStdin)
// returns it as is, so the answer to one prompt isn't lost in the other's
// buffer.
func TestSetupSharesOneStdinReader(t *testing.T) {
	cmd := newSetupCmd(testDeps(false))
	in := bufio.NewReader(strings.NewReader("y\nclaude\n"))
	cmd.SetIn(in)
	if bufio.NewReader(cmd.InOrStdin()) != in {
		t.Fatal("a second reader would wrap stdin")
	}
}
