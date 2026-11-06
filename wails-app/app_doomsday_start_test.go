//go:build !windows

package main

import (
	"encoding/json"
	"os"
	"strings"
	"testing"

	"github.com/monoes/mono-agent/internal/cli/opgate"
)

const dmCharter = `{"goal":"g","limits":{"budget_micro":1000000},"lines":["spending"]}`

func parseStartDoc(t *testing.T, s string) (d struct {
	OK      bool   `json:"ok"`
	Started bool   `json:"started"`
	Output  string `json:"output"`
	Detail  string `json:"detail"`
	Code    string `json:"code"`
}) {
	t.Helper()
	if err := json.Unmarshal([]byte(s), &d); err != nil {
		t.Fatalf("not a start document: %q", s)
	}
	return d
}

// The preview and the start hand the charter to the CLI on stdin and nothing else of it: the argv is fixed, the page
// supplies a charter, the phrase the person typed and a boss session number, and none of them is a flag.
func TestDoomsdayRenderAndStartRunExactArgvWithTheCharterOnStdin(t *testing.T) {
	log, stdin := taskCLIFake(t, `{"hash":"h"}`, 0)
	a := newTaskTestApp(t)
	if got := a.DoomsdayRender(dmCharter); got != `{"hash":"h"}` {
		t.Fatalf("render = %q", got)
	}
	if b, _ := os.ReadFile(stdin); string(b) != dmCharter {
		t.Fatalf("stdin = %q", b)
	}
	phrase := "I understand: lift 1 lines, charter 12345678"
	a.DoomsdayStart(dmCharter, phrase, 0)
	a.DoomsdayStart(dmCharter, "--yes-i-lift x", 41)
	want := []string{
		"--profile|work|--json|doomsday|render|--charter|-|",
		"--profile|work|--json|doomsday|start|--charter|-|--gui-phrase=" + phrase + "|",
		"--profile|work|--json|doomsday|start|--charter|-|--gui-phrase=--yes-i-lift x|--boss-session=41|",
	}
	if got := loggedArgs(t, log); strings.Join(got, "\n") != strings.Join(want, "\n") {
		t.Fatalf("argv:\n%q\nwant:\n%q", got, want)
	}
}

func TestDoomsdayStartAnswersWithTheCLIsWords(t *testing.T) {
	taskCLIFake(t, `{"run_id":"work/1","hash":"h","generation":1}`, 0)
	d := parseStartDoc(t, newTaskTestApp(t).DoomsdayStart(dmCharter, "I understand: lift 1 lines, charter 12345678", 0))
	if !d.OK || !d.Started || !strings.Contains(d.Output, "work/1") {
		t.Fatalf("%+v", d)
	}
	taskCLIFake(t, `{"error":"the phrase does not match; nothing started","code":"invalid_input"}`, 2)
	d = parseStartDoc(t, newTaskTestApp(t).DoomsdayStart(dmCharter, "I understand: lift 1 lines, charter 12345678", 0))
	if d.OK || d.Started || d.Code != "invalid_input" || !strings.Contains(d.Detail, "does not match") {
		t.Fatalf("%+v", d)
	}
}

// What cannot be a charter, a phrase or a boss session never reaches the CLI.
func TestDoomsdayStartRefusesBadInputBeforeTheCLI(t *testing.T) {
	log, _ := taskCLIFake(t, `{}`, 0)
	a := newTaskTestApp(t)
	good := "I understand: lift 1 lines, charter 12345678"
	for name, c := range map[string]struct {
		charter, phrase string
		boss            int64
	}{
		"empty charter":  {"", good, 0},
		"not an object":  {`["x"]`, good, 0},
		"not json":       {`goal: x`, good, 0},
		"huge charter":   {`{"goal":"` + strings.Repeat("x", doomsdayCharterMax) + `"}`, good, 0},
		"empty phrase":   {dmCharter, "", 0},
		"blank phrase":   {dmCharter, "   ", 0},
		"long phrase":    {dmCharter, strings.Repeat("a", doomsdayPhraseMax+1), 0},
		"newline phrase": {dmCharter, good + "\nsecond", 0},
		"negative boss":  {dmCharter, good, -1},
	} {
		d := parseStartDoc(t, a.DoomsdayStart(c.charter, c.phrase, c.boss))
		if d.OK || d.Code != "invalid_input" {
			t.Errorf("%s: %+v", name, d)
		}
	}
	if callCount(t, log) != 0 {
		t.Fatal("the CLI ran for a refused input")
	}
}

// Both are the operator's: a process that is not the user's own is refused here and the CLI is not run.
func TestDoomsdayRenderAndStartNeedTheOperator(t *testing.T) {
	opgate.SetForTest(t, func(bool) string { return "an agent's shell" })
	log, _ := taskCLIFake(t, `{}`, 0)
	a := newTaskTestApp(t)
	if d := parseStartDoc(t, a.DoomsdayStart(dmCharter, "I understand: lift 1 lines, charter 12345678", 0)); d.OK || d.Code != "operator_only" {
		t.Fatalf("start: %+v", d)
	}
	if got := a.DoomsdayRender(dmCharter); !strings.Contains(got, "operator_only") {
		t.Fatalf("render: %q", got)
	}
	if got := a.DoomsdayOpenEntry(); !strings.Contains(got, "operator_only") {
		t.Fatalf("open entry: %q", got)
	}
	if callCount(t, log) != 0 {
		t.Fatal("the CLI ran for a refused caller")
	}
}

// A second click while a start runs is refused, not queued: two starts of one form must not both be tried.
func TestDoomsdayStartIsOneAtATime(t *testing.T) {
	if !doomsdayStartMu.TryLock() {
		t.Fatal("the start lock is held")
	}
	defer doomsdayStartMu.Unlock()
	log, _ := taskCLIFake(t, `{}`, 0)
	if d := parseStartDoc(t, newTaskTestApp(t).DoomsdayStart(dmCharter, "I understand: lift 1 lines, charter 12345678", 0)); d.OK || d.Code != "busy" {
		t.Fatalf("%+v", d)
	}
	if callCount(t, log) != 0 {
		t.Fatal("the CLI ran")
	}
}

func TestDoomsdayOpenEntryRunsTheProbeVerb(t *testing.T) {
	log, _ := taskCLIFake(t, `{"mode_available":true,"platform":"linux"}`, 0)
	a := newTaskTestApp(t)
	if got := a.DoomsdayOpenEntry(); got != `{"mode_available":true,"platform":"linux"}` {
		t.Fatalf("%q", got)
	}
	if got := loggedArgs(t, log); len(got) != 1 || got[0] != "--profile|work|--json|doomsday|probe|" {
		t.Fatalf("argv %q", got)
	}
}
