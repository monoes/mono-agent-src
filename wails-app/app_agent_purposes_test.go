//go:build !windows

package main

import (
	"encoding/json"
	"strings"
	"testing"
)

const purposesJSON = `{"purposes":[{"id":"coding","title":"Coding","pairs":[],"rule":"any"}],"available":[]}`

// The purposes bindings shell out to `agent purposes …` and return the CLI's
// JSON verbatim; what the user typed stays inside one --flag=value argument and
// the purpose id goes after "--".
func TestApp_AgentPurposes_ShellOutAndReturnTheCLIJSON(t *testing.T) {
	bin, argsLog := chatFakeCLI(t,
		fakeChatReply{match: "agent purposes set", stdout: `{"purpose":{"id":"coding"}}` + "\n"},
		fakeChatReply{match: "agent purposes reset", stdout: `{"reset":["coding"]}` + "\n"},
		fakeChatReply{match: "agent purposes list", stdout: purposesJSON + "\n"},
	)
	a, _ := newCLIChatApp(t, bin)

	if got := a.AgentPurposes(); got != purposesJSON {
		t.Errorf("AgentPurposes = %s, want the CLI JSON verbatim", got)
	}
	a.SetAgentPurpose("coding", []string{"claude/opus", "codex/"}, "order", "high", true, 25, true)
	a.SetAgentPurpose("judging", nil, "any", "", false, 0, false)
	a.ResetAgentPurpose("coding")
	a.ResetAgentPurposes()
	want := []string{
		"agent purposes list",
		"agent purposes set --rule=order --budget-share=25 --pair=claude/opus --pair=codex/ --effort=high --different-model --fallback -- coding",
		"agent purposes set --rule=any --budget-share=0 -- judging",
		"agent purposes reset -- coding",
		"agent purposes reset --all",
	}
	var got []string
	for _, line := range readArgsLog(t, argsLog) {
		got = append(got, strings.TrimPrefix(line, "--profile default --json "))
	}
	if strings.Join(got, "|") != strings.Join(want, "|") {
		t.Errorf("CLI calls = %q\nwant %q", got, want)
	}
}

// Anything that could read as a flag, or is not a purpose, pair, rule or share,
// never reaches the CLI.
func TestApp_AgentPurposes_RefusesWhatCouldReadAsAFlag(t *testing.T) {
	bin, argsLog := chatFakeCLI(t)
	a, _ := newCLIChatApp(t, bin)
	for name, out := range map[string]string{
		"purpose":   a.SetAgentPurpose("--all", nil, "any", "", false, 0, false),
		"pair":      a.SetAgentPurpose("coding", []string{"--x"}, "any", "", false, 0, false),
		"rule":      a.SetAgentPurpose("coding", nil, "--all", "", false, 0, false),
		"share":     a.SetAgentPurpose("coding", nil, "any", "", false, 101, false),
		"reset":     a.ResetAgentPurpose("--all"),
		"reset-upp": a.ResetAgentPurpose("Coding"),
	} {
		var r struct{ Error, Code string }
		if err := json.Unmarshal([]byte(out), &r); err != nil || r.Code != "invalid_input" {
			t.Errorf("%s: got %s, want invalid_input", name, out)
		}
	}
	if lines := readArgsLog(t, argsLog); len(lines) != 0 {
		t.Errorf("the CLI was called: %q", lines)
	}
}

// A refusal (an agent shell) keeps its message and code.
func TestApp_AgentPurposes_RefusalKeepsTheCode(t *testing.T) {
	bin, _ := chatFakeCLI(t, fakeChatReply{match: "agent purposes set", code: 3, stderr: "refused\n",
		stdout: `{"error":"only the operator sets purposes","code":"operator_only"}` + "\n"})
	a, _ := newCLIChatApp(t, bin)
	var r struct{ Error, Code string }
	if err := json.Unmarshal([]byte(a.SetAgentPurpose("coding", nil, "any", "", false, 0, false)), &r); err != nil {
		t.Fatal(err)
	}
	if r.Code != "operator_only" {
		t.Errorf("refusal = %+v", r)
	}
}
