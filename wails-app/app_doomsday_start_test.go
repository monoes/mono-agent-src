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
	State   string `json:"state"`
}) {
	t.Helper()
	if err := json.Unmarshal([]byte(s), &d); err != nil {
		t.Fatalf("not a start document: %q", s)
	}
	return d
}

// The preview and the start hand the charter to the CLI on stdin and nothing else of it: the argv is fixed, the page
// supplies a charter, the phrase the person typed, the render's token and a boss session number, and none of them is a flag.
func TestDoomsdayRenderAndStartRunExactArgvWithTheCharterOnStdin(t *testing.T) {
	g := newGrantRig(t)
	phrase := gPhrase
	g.startBehaves(`echo '{"run_id":"work/1"}'`)
	tok := g.render(dmCharter)
	d := parseStartDoc(t, g.a.DoomsdayStart(dmCharter, phrase, tok, 41))
	if !d.OK {
		t.Fatalf("%+v", d)
	}
	want := []string{
		"--profile|work|--json|doomsday|render|--charter|-|",
		"--profile|work|--json|doomsday|start|--charter|-|--gui-phrase=" + gPhrase + "|--boss-session=41|",
	}
	if got := loggedArgs(t, g.log); strings.Join(got, "\n") != strings.Join(want, "\n") {
		t.Fatalf("argv:\n%q\nwant:\n%q", got, want)
	}
	if b, _ := os.ReadFile(g.stdin); string(b) != dmCharter {
		t.Fatalf("stdin = %q", b)
	}
}

func TestDoomsdayStartAnswersWithTheCLIsWords(t *testing.T) {
	g := newGrantRig(t)
	g.startBehaves(`echo '{"run_id":"work/1","hash":"h","generation":1}'`)
	tok := g.render(dmCharter)
	d := parseStartDoc(t, g.a.DoomsdayStart(dmCharter, gPhrase, tok, 5))
	if !d.OK || !d.Started || d.State != "started" || !strings.Contains(d.Output, "work/1") {
		t.Fatalf("%+v", d)
	}
	g.startBehaves(`echo '{"error":"the phrase does not match; nothing started","code":"invalid_input"}'; exit 2`)
	tok = g.render(dmCharter)
	d = parseStartDoc(t, g.a.DoomsdayStart(dmCharter, gPhrase, tok, 5))
	if d.OK || d.Started || d.Code != "invalid_input" || !strings.Contains(d.Detail, "does not match") {
		t.Fatalf("%+v", d)
	}
}

// What cannot be a charter, a phrase or a boss session never reaches the CLI.
func TestDoomsdayStartRefusesBadInputBeforeTheCLI(t *testing.T) {
	g := newGrantRig(t)
	tok := g.render(dmCharter)
	before := g.calls()
	good := gPhrase
	for name, c := range map[string]struct {
		charter, phrase string
		boss            int64
	}{
		"empty charter":  {"", good, 5},
		"not an object":  {`["x"]`, good, 5},
		"not json":       {`goal: x`, good, 5},
		"huge charter":   {`{"goal":"` + strings.Repeat("x", doomsdayCharterMax) + `"}`, good, 5},
		"empty phrase":   {dmCharter, "", 5},
		"blank phrase":   {dmCharter, "   ", 5},
		"long phrase":    {dmCharter, strings.Repeat("a", doomsdayPhraseMax+1), 5},
		"newline phrase": {dmCharter, good + "\nsecond", 5},
		"negative boss":  {dmCharter, good, -1},
		"no boss":        {dmCharter, good, 0},
	} {
		d := parseStartDoc(t, g.a.DoomsdayStart(c.charter, c.phrase, tok, c.boss))
		if d.OK || d.Code != "invalid_input" {
			t.Errorf("%s: %+v", name, d)
		}
	}
	if g.calls() != before || g.dialogs != 0 {
		t.Fatal("the CLI or the dialog ran for a refused input")
	}
}

// Both are the operator's: a process that is not the user's own is refused here and the CLI is not run.
func TestDoomsdayRenderAndStartNeedTheOperator(t *testing.T) {
	opgate.SetForTest(t, func(bool) string { return "an agent's shell" })
	log, _ := taskCLIFake(t, `{}`, 0)
	a := newTaskTestApp(t)
	if d := parseStartDoc(t, a.DoomsdayStart(dmCharter, "I understand: lift 1 lines, charter 12345678", "", 5)); d.OK || d.Code != "operator_only" {
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

func TestDoomsdayOpenEntryRunsTheProbeVerb(t *testing.T) {
	log, _ := taskCLIFake(t, `{"mode_available":true,"platform":"linux"}`, 0)
	a := newTaskTestApp(t)
	doomsdayEntry.invalidate("work")
	if got := a.DoomsdayOpenEntry(); got != `{"mode_available":true,"platform":"linux"}` {
		t.Fatalf("%q", got)
	}
	if got := loggedArgs(t, log); len(got) != 1 || got[0] != "--profile|work|--json|doomsday|probe|" {
		t.Fatalf("argv %q", got)
	}
}
