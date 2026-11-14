//go:build !windows

package main

import (
	"go/ast"
	"go/token"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/monoes/mono-agent/internal/cli/opgate"
)

// authorityFixture: a fake CLI that logs its arguments, the native dialog answering with yes/err and recording what it said.
func authorityFixture(t *testing.T, yes bool, dialogErr error) (a *App, log string, dialogs *[]string) {
	t.Helper()
	log = filepath.Join(t.TempDir(), "args.log")
	bin := fakeCLI(t, `echo "$*" >> '`+log+"'\n"+`echo '{}'`+"\n")
	a, _ = newCLIChatApp(t, bin)
	if err := os.WriteFile(log, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	var said []string
	old := confirmNative
	confirmNative = func(_ *App, title, body string) (bool, error) {
		said = append(said, title+": "+body)
		return yes, dialogErr
	}
	t.Cleanup(func() { confirmNative = old })
	return a, log, &said
}

func ran(t *testing.T, log string) []string {
	t.Helper()
	var out []string
	for _, l := range loggedArgs(t, log) {
		if l != "" {
			out = append(out, l)
		}
	}
	return out
}

func TestTurningOnAuthorityAsksTheNativeDialogAndNamesTheCeiling(t *testing.T) {
	a, log, said := authorityFixture(t, true, nil)
	a.SetAgentApproval("c1", true, 5)
	a.SetMissionChildren(true)
	want := []string{
		`--profile default --json coder sessions settings set --patch={"agent_approval":"lead","approval_ceiling_usd":5} -- c1`,
		"--profile default --json coder sessions mission-children enable",
	}
	if got := ran(t, log); strings.Join(got, "\n") != strings.Join(want, "\n") {
		t.Fatalf("calls %q", got)
	}
	if len(*said) != 2 || !strings.Contains((*said)[0], "$5.00") {
		t.Fatalf("dialogs %q: the first must state the ceiling", *said)
	}
}

func TestTurningOnAuthorityIsRefusedWithoutAYesAndTurningOffNeedsNoDialog(t *testing.T) {
	for name, c := range map[string]struct {
		yes bool
		err error
	}{"declined": {false, nil}, "no native dialog": {false, errNoNativeDialog}, "error": {true, errNoNativeDialog}} {
		a, log, _ := authorityFixture(t, c.yes, c.err)
		if out := a.SetAgentApproval("c1", true, 5); !strings.Contains(out, "error") {
			t.Errorf("%s: approval not refused: %s", name, out)
		}
		if out := a.SetMissionChildren(true); !strings.Contains(out, "error") {
			t.Errorf("%s: children not refused: %s", name, out)
		}
		if got := ran(t, log); len(got) != 0 {
			t.Errorf("%s: the CLI ran: %q", name, got)
		}
	}
	a, log, said := authorityFixture(t, false, errNoNativeDialog)
	a.SetAgentApproval("c1", false, 0)
	a.SetMissionChildren(false)
	if got := ran(t, log); len(got) != 2 || len(*said) != 0 {
		t.Fatalf("off: calls %q, dialogs %q: turning authority off must run without a dialog", got, *said)
	}
}

func TestTurningOnAuthorityIsRefusedForANonOperatorProcessBeforeAnyDialog(t *testing.T) {
	a, log, said := authorityFixture(t, true, nil)
	opgate.SetForTest(t, func(bool) string { return "this process runs inside a coder session" })
	if out := a.SetAgentApproval("c1", true, 5); !strings.Contains(out, "operator_only") {
		t.Fatalf("approval: %s", out)
	}
	if out := a.SetMissionChildren(true); !strings.Contains(out, "operator_only") {
		t.Fatalf("children: %s", out)
	}
	if len(ran(t, log)) != 0 || len(*said) != 0 {
		t.Fatalf("ran %q, asked %q", ran(t, log), *said)
	}
}

func TestTheApprovalCeilingFromTheAppIsBounded(t *testing.T) {
	a, log, said := authorityFixture(t, true, nil)
	for _, v := range []float64{0, -3, 1000.01, 1e6, 1e9} {
		if out := a.SetAgentApproval("c1", true, v); !strings.Contains(out, "error") {
			t.Errorf("%v accepted: %s", v, out)
		}
	}
	if len(ran(t, log)) != 0 || len(*said) != 0 {
		t.Fatal("a refused ceiling reached the dialog or the CLI")
	}
	if out := a.SetAgentApproval("c1", true, maxApprovalCeilingUSD); strings.Contains(out, "error") {
		t.Fatalf("the maximum was refused: %s", out)
	}
}

// Every App function that can raise authority (it names agent_approval, or the mission-children switch's "enable") checks the operator gate and
// asks in the native dialog, both before it runs the CLI. A new binding of this kind must come with the same shape.
func TestAuthorityRaisingBindingsAskInANativeDialog(t *testing.T) {
	raises := func(fd *ast.FuncDecl) bool {
		approval, kids, enable := false, false, false
		ast.Inspect(fd, func(n ast.Node) bool {
			if s, ok := strLit(n); ok {
				approval = approval || s == "agent_approval"
				kids = kids || s == "mission-children"
				enable = enable || s == "enable"
			}
			return true
		})
		return approval || (kids && enable)
	}
	check := func(f srcFile) (names []string, bad []string) {
		for _, d := range f.file.Decls {
			fd, ok := d.(*ast.FuncDecl)
			if !ok || fd.Body == nil || !raises(fd) {
				continue
			}
			names = append(names, fd.Name.Name)
			first := func(name string) token.Pos {
				cs, _ := callsOf(fd, name)
				if len(cs) == 0 {
					return token.NoPos
				}
				return cs[0]
			}
			gate, ask, run := first("Refusal"), first("authorityConfirm"), first("jsonResult")
			if gate == token.NoPos || ask == token.NoPos || run == token.NoPos || !(gate < ask && ask < run) {
				bad = append(bad, fd.Name.Name)
			}
		}
		return
	}
	var all []string
	for _, f := range nonTestSources(t) {
		n, bad := check(f)
		all = append(all, n...)
		for _, b := range bad {
			t.Errorf("%s: %s raises authority without opgate.Refusal then authorityConfirm before the CLI", f.path, b)
		}
	}
	if strings.Join(all, ",") != "SetMissionChildren,SetAgentApproval" && strings.Join(all, ",") != "SetAgentApproval,SetMissionChildren" {
		t.Fatalf("authority-raising functions: %v (a new one needs its own review)", all)
	}
	if n, bad := check(parseSource(t, "mutant.go", []byte(`package main
func (a *App) SetX() string { return a.jsonResult("coder", "sessions", "settings", "set", "agent_approval") }`))); len(n) != 1 || len(bad) != 1 {
		t.Error("the scan does not see an authority binding without the dialog")
	}
}
