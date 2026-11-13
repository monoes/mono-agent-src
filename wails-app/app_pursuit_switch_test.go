//go:build !windows

package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestPursuitSwitchBindingsRunOnlyTheirCLIVerbs(t *testing.T) {
	log := filepath.Join(t.TempDir(), "args.log")
	bin := fakeCLI(t, `echo "$*" >> '`+log+"'\n"+`echo '{}'`+"\n")
	a, _ := newCLIChatApp(t, bin)
	a.PursuitHostStatus()
	a.PursuitReadiness(7)
	a.PursuitPause(7)
	a.MissionChildrenState()
	a.SetMissionChildren(true)
	a.SetMissionChildren(false)
	a.SetAgentApproval("c1", true, 5)
	a.SetAgentApproval("-rf", false, 0)
	want := []string{
		"--profile default --json coder host pursuit status",
		"--profile default --json task session pursuit readiness 7",
		"--profile default --json task session pursuit pause 7",
		"--profile default --json coder sessions mission-children list",
		"--profile default --json coder sessions mission-children enable",
		"--profile default --json coder sessions mission-children disable",
		`--profile default --json coder sessions settings set --patch={"agent_approval":"lead","approval_ceiling_usd":5} -- c1`,
		`--profile default --json coder sessions settings set --patch={"agent_approval":"off"} -- -rf`,
	}
	if got := loggedArgs(t, log); strings.Join(got, "\n") != strings.Join(want, "\n") {
		t.Fatalf("calls = %q\nwant %q", got, want)
	}
}

func TestPursuitSwitchBindingsRefuseBadInputWithoutRunningTheCLI(t *testing.T) {
	log := filepath.Join(t.TempDir(), "args.log")
	bin := fakeCLI(t, `echo "$*" >> '`+log+"'\n"+`echo '{}'`+"\n")
	a, _ := newCLIChatApp(t, bin)
	if err := os.WriteFile(log, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	for _, out := range []string{a.PursuitReadiness(0), a.PursuitPause(-1), a.SetAgentApproval("c1", true, 0), a.SetAgentApproval("c1", true, -3), a.SetAgentApproval("c1", true, 1e9)} {
		if !strings.Contains(out, "error") {
			t.Errorf("not refused: %s", out)
		}
	}
	if got := loggedArgs(t, log); len(got) > 1 || (len(got) == 1 && got[0] != "") {
		t.Fatalf("the CLI ran for refused input: %q", got)
	}
}
