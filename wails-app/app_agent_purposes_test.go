//go:build !windows

package main

import (
	"encoding/json"
	"strings"
	"testing"
)

const purposesJSON = `{"purposes":[{"id":"coding","title":"Coding","category":"code","description":"d","rule":"any","pairs":[],"effort":"","differentFromAuthor":false,"fallback":"any","budgetSharePct":0,"isDefault":true}],"roster":[{"runtime":"claude","model":"opus","status":"ok"}]}`

func stripPurposeArgs(t *testing.T, argsLog string) []string {
	var got []string
	for _, line := range readArgsLog(t, argsLog) {
		got = append(got, strings.TrimPrefix(line, "--profile default --json "))
	}
	return got
}

// The bindings shell out to `agent purposes …` and return the CLI JSON
// verbatim; what the user chose stays inside --flag=value arguments.
func TestApp_AgentPurposes_ShellOutAndReturnTheCLIJSON(t *testing.T) {
	bin, argsLog := chatFakeCLI(t,
		fakeChatReply{match: "agent purposes set", stdout: `{"ok":true}` + "\n"},
		fakeChatReply{match: "agent purposes reset", stdout: `{"ok":true}` + "\n"},
		fakeChatReply{match: "agent purposes list", stdout: purposesJSON + "\n"},
	)
	a, _ := newCLIChatApp(t, bin)

	if got := a.AgentPurposesList(); got != purposesJSON {
		t.Errorf("AgentPurposesList = %s, want the CLI JSON verbatim", got)
	}
	a.AgentPurposeSet("review", []string{"claude:opus", "ollama:llama3:8b"}, "order", "high", true, "none", 12.5)
	a.AgentPurposeSet("coding", nil, "any", "", false, "any", 0)
	a.AgentPurposeReset("review")
	want := []string{
		"agent purposes list",
		"agent purposes set review --pair=claude:opus --pair=ollama:llama3:8b --rule=order --fallback=none --different-from-author=on --effort=high --budget-share=12.5",
		"agent purposes set coding --rule=any --fallback=any --different-from-author=off",
		"agent purposes reset review",
	}
	if got := strings.Join(stripPurposeArgs(t, argsLog), "|"); got != strings.Join(want, "|") {
		t.Errorf("CLI calls = %q\nwant %q", got, want)
	}
}

// Bad input never reaches the CLI.
func TestApp_AgentPurposes_RefusesBadInputBeforeTheCLI(t *testing.T) {
	bin, argsLog := chatFakeCLI(t, fakeChatReply{match: "agent purposes", stdout: `{"ok":true}` + "\n"})
	a, _ := newCLIChatApp(t, bin)
	bad := []struct {
		name string
		got  string
	}{
		{"flag-like id", a.AgentPurposeSet("--rule=x", nil, "any", "", false, "any", 0)},
		{"upper id", a.AgentPurposeSet("Coding", nil, "any", "", false, "any", 0)},
		{"rule", a.AgentPurposeSet("coding", nil, "fastest", "", false, "any", 0)},
		{"fallback", a.AgentPurposeSet("coding", nil, "any", "", false, "maybe", 0)},
		{"effort", a.AgentPurposeSet("coding", nil, "any", "--x", false, "any", 0)},
		{"share", a.AgentPurposeSet("coding", nil, "any", "", false, "any", 101)},
		{"negative share", a.AgentPurposeSet("coding", nil, "any", "", false, "any", -1)},
		{"no colon", a.AgentPurposeSet("coding", []string{"claude"}, "any", "", false, "any", 0)},
		{"flag runtime", a.AgentPurposeSet("coding", []string{"--x:m"}, "any", "", false, "any", 0)},
		{"empty model", a.AgentPurposeSet("coding", []string{"claude: "}, "any", "", false, "any", 0)},
		{"newline model", a.AgentPurposeSet("coding", []string{"claude:a\nb"}, "any", "", false, "any", 0)},
		{"duplicate", a.AgentPurposeSet("coding", []string{"claude:a", "claude:a"}, "any", "", false, "any", 0)},
		{"reset id", a.AgentPurposeReset("-rf")},
	}
	for _, b := range bad {
		var r struct{ Error string }
		if err := json.Unmarshal([]byte(b.got), &r); err != nil || r.Error == "" {
			t.Errorf("%s: got %s, want an error", b.name, b.got)
		}
	}
	if calls := stripPurposeArgs(t, argsLog); len(calls) != 0 {
		t.Errorf("CLI ran for bad input: %q", calls)
	}
}

// A refusal keeps its message and code.
func TestApp_AgentPurposes_RefusalKeepsTheCode(t *testing.T) {
	bin, _ := chatFakeCLI(t, fakeChatReply{match: "agent purposes set", code: 3, stderr: "refused\n",
		stdout: `{"error":"only the operator edits purposes","code":"operator_only"}` + "\n"})
	a, _ := newCLIChatApp(t, bin)
	var r struct{ Error, Code string }
	if err := json.Unmarshal([]byte(a.AgentPurposeSet("coding", nil, "any", "", false, "any", 0)), &r); err != nil {
		t.Fatal(err)
	}
	if r.Error != "only the operator edits purposes" || r.Code != "operator_only" {
		t.Errorf("refusal = %+v", r)
	}
}
