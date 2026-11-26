//go:build !windows

package main

import (
	"encoding/json"
	"strings"
	"testing"
)

const budgetsJSON = `{"operatorUsd":5,"caps":[{"scope":"global","key":"","capUsd":10,"usedUsd":1.5,"updatedBy":"operator","updatedAt":"x"}],"spentUsd":1.5}`

// The budget bindings shell out to `aitask budget …` and return the CLI's JSON
// verbatim; what the user typed stays inside one --flag=value argument.
func TestApp_AITaskBudgets_ShellOutAndReturnTheCLIJSON(t *testing.T) {
	bin, argsLog := chatFakeCLI(t,
		fakeChatReply{match: "aitask budget set", stdout: `{"scope":"kind","capUsd":2}` + "\n"},
		fakeChatReply{match: "aitask budget show --task", stdout: `{"taskId":"at-1","capUsd":2,"spentUsd":0.5,"reported":true}` + "\n"},
		fakeChatReply{match: "aitask budget show", stdout: budgetsJSON + "\n"},
	)
	a, _ := newCLIChatApp(t, bin)

	if got := a.AITaskBudgets(); got != budgetsJSON {
		t.Errorf("AITaskBudgets = %s, want the CLI JSON verbatim", got)
	}
	if got := a.SetAITaskBudget("kind", "--scope=global", 2.5); !strings.Contains(got, `"capUsd":2`) {
		t.Errorf("SetAITaskBudget = %s", got)
	}
	a.SetAITaskBudget("global", "", 0)
	a.ClearAITaskBudget("role", "writer")
	if got := a.AITaskBudgetFor("at-1"); !strings.Contains(got, `"spentUsd":0.5`) {
		t.Errorf("AITaskBudgetFor = %s", got)
	}
	want := []string{
		"aitask budget show",
		"aitask budget set --scope=kind --usd=2.5 --key=--scope=global",
		"aitask budget set --scope=global --usd=0",
		"aitask budget set --scope=role --clear --key=writer",
		"aitask budget show --task=at-1",
	}
	var got []string
	for _, line := range readArgsLog(t, argsLog) {
		got = append(got, strings.TrimPrefix(line, "--profile default --json "))
	}
	if strings.Join(got, "|") != strings.Join(want, "|") {
		t.Errorf("CLI calls = %q\nwant %q", got, want)
	}
}

// The pane's Spend row reads the CLI's effective cap of one chat (#1766).
func TestApp_ChatSpendCap_ShellsOutAndReturnsTheCLIJSON(t *testing.T) {
	const capJSON = `{"v":1,"conversationId":"c1","bounded":true,"capUsd":0.5,"source":"message"}`
	bin, argsLog := chatFakeCLI(t, fakeChatReply{match: "chat history spend-cap", stdout: capJSON + "\n"})
	a, _ := newCLIChatApp(t, bin)
	if got := a.ChatSpendCap("--c1"); got != capJSON {
		t.Errorf("ChatSpendCap = %s, want the CLI JSON verbatim", got)
	}
	want := "--profile default --json chat history spend-cap -- --c1"
	if got := readArgsLog(t, argsLog); len(got) != 1 || got[0] != want {
		t.Errorf("CLI calls = %q, want %q", got, want)
	}
}

// A refusal (an agent shell, a raise) keeps its message and code.
func TestApp_AITaskBudgets_RefusalKeepsTheCode(t *testing.T) {
	bin, _ := chatFakeCLI(t, fakeChatReply{match: "aitask budget set", code: 3, stderr: "refused\n",
		stdout: `{"error":"only the user sets a budget","code":"operator_only"}` + "\n"})
	a, _ := newCLIChatApp(t, bin)
	var r struct{ Error, Code string }
	if err := json.Unmarshal([]byte(a.SetAITaskBudget("global", "", 9)), &r); err != nil {
		t.Fatal(err)
	}
	if r.Error != "only the user sets a budget" || r.Code != "operator_only" {
		t.Errorf("refusal = %+v", r)
	}
}
