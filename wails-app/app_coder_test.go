//go:build !windows

package main

import (
	"encoding/json"
	"sort"
	"strings"
	"testing"
)

const coderStatusJSON = `{"enabled":true,"workspaceRoot":"/home/u/monoagent-coder","maxTurns":200,"timeout":"60m","budgetUsd":0,"ready":false,"missingCapabilities":["init-json"],"monomindVersion":"1.2.3","runtime":"claude"}`

func TestApp_CoderSettings_ShellOutAndReturnTheCLIJSON(t *testing.T) {
	bin, argsLog := chatFakeCLI(t,
		fakeChatReply{match: "coder status", stdout: coderStatusJSON + "\n"},
		fakeChatReply{match: "coder enable", stdout: coderStatusJSON + "\n"},
		fakeChatReply{match: "coder disable", stdout: coderStatusJSON + "\n"},
		fakeChatReply{match: "coder set", stdout: coderStatusJSON + "\n"},
		fakeChatReply{match: "coder workspace root", stdout: `{"path":"/home/u/monoagent-coder","created":true,"git":true,"init":{"created":["CLAUDE.md"],"skipped":[]}}`},
		fakeChatReply{match: "coder workspace default", stdout: `{"path":"/home/u/.monoagent/profiles/work","profile":{"id":"work","name":"Work"},"source":"profile","created":false}`},
		fakeChatReply{match: "coder workspace list", stdout: `[{"path":"/w/a","lastUsed":"2026-09-27T10:00:00Z","conversations":3,"exists":true}]`},
	)
	a, _ := newCLIChatApp(t, bin)
	a.selectForTest("work")

	for name, got := range map[string]string{
		"CoderStatus":  a.CoderStatus(),
		"CoderEnable":  a.CoderEnable(),
		"CoderDisable": a.CoderDisable(),
		"CoderSet":     a.CoderSet("/w", 50, "30m", 2.5, "jev", "3m"),
	} {
		if got != coderStatusJSON {
			t.Errorf("%s = %s, want the status JSON verbatim", name, got)
		}
	}
	if got := a.CoderWorkspaceRoot("codex"); !strings.Contains(got, `"path":"/home/u/monoagent-coder"`) {
		t.Errorf("CoderWorkspaceRoot = %s", got)
	}
	if got := a.CoderWorkspaceDefault("codex"); !strings.Contains(got, `"profile":{"id":"work","name":"Work"}`) {
		t.Errorf("CoderWorkspaceDefault = %s", got)
	}
	if got := a.CoderWorkspaceList(); !strings.HasPrefix(got, `[{"path":"/w/a"`) {
		t.Errorf("CoderWorkspaceList = %s", got)
	}

	var calls []string
	for _, line := range readArgsLog(t, argsLog) {
		calls = append(calls, strings.TrimPrefix(line, "--profile work --json "))
	}
	want := []string{
		"coder status",
		"coder enable --yes-i-understand",
		"coder disable",
		"coder set --workspace-root /w --max-turns 50 --timeout 30m --budget-usd 2.5 --org-router jev --startup-timeout 3m",
		"coder workspace root --runtime codex",
		"coder workspace default --runtime codex",
		"coder workspace list",
	}
	// Map iteration above runs the four status calls in any order.
	if strings.Join(sortedCopy(calls[:4]), "|") != strings.Join(sortedCopy(want[:4]), "|") ||
		strings.Join(calls[4:], "|") != strings.Join(want[4:], "|") {
		t.Errorf("CLI calls = %q\nwant %q", calls, want)
	}
}

func TestApp_CoderSetAppAccess_ShellsOutToCoderSet(t *testing.T) {
	bin, argsLog := chatFakeCLI(t, fakeChatReply{match: "coder set", stdout: `{"enabled":true,"appAccess":false}` + "\n"})
	a, _ := newCLIChatApp(t, bin)
	a.selectForTest("work")
	if got := a.CoderSetAppAccess(false); got != `{"enabled":true,"appAccess":false}` {
		t.Errorf("CoderSetAppAccess = %s, want the status JSON verbatim", got)
	}
	a.CoderSetAppAccess(true)
	got := strings.Join(readArgsLog(t, argsLog), "\n")
	if !strings.Contains(got, "coder set --app-access off") || !strings.Contains(got, "coder set --app-access on") {
		t.Errorf("CLI calls = %s", got)
	}
}

func sortedCopy(s []string) []string {
	out := append([]string(nil), s...)
	sort.Strings(out)
	return out
}

func TestCoderSetArgs_LeavesUnsetValuesAlone(t *testing.T) {
	cases := []struct {
		root     string
		turns    int
		timeout  string
		budget   float64
		router   string
		startup  string
		wantArgs string
	}{
		{"", 0, "", -1, "", "", "coder set"},
		{"", 0, "", 0, "", "", "coder set --budget-usd 0"},
		{"/r", 10, "5m", 0.75, "", "", "coder set --workspace-root /r --max-turns 10 --timeout 5m --budget-usd 0.75"},
		// The org router alone, as the coder-org settings change it.
		{"", 0, "", -1, "model", "", "coder set --org-router model"},
		{"/r", 0, "", -1, "auto", "", "coder set --workspace-root /r --org-router auto"},
		// The start-up limit; "0" returns it to the default.
		{"", 0, "", -1, "", "3m", "coder set --startup-timeout 3m"},
		{"", 0, "", -1, "", "0", "coder set --startup-timeout 0"},
	}
	for _, c := range cases {
		if got := strings.Join(coderSetArgs(c.root, c.turns, c.timeout, c.budget, c.router, c.startup), " "); got != c.wantArgs {
			t.Errorf("coderSetArgs(%q,%d,%q,%v,%q,%q) = %q, want %q", c.root, c.turns, c.timeout, c.budget, c.router, c.startup, got, c.wantArgs)
		}
	}
}

func TestApp_CreateCoderConversation_BuildsModeAndFolderArgs(t *testing.T) {
	bin, argsLog := chatFakeCLI(t, fakeChatReply{match: "chat history create", stdout: `{"id":"c9","profile_id":"default","backend":"agent","workflow_context":"general","runtime_id":"codex","model":"gpt-5-codex","effort":"high","mode":"coder","cwd":"/w/proj","created_at":"2026-09-27T10:00:00Z","updated_at":"2026-09-27T10:00:00Z"}`})
	a, _ := newCLIChatApp(t, bin)

	var conv struct {
		ID, Mode, Cwd, WorkflowContext string
	}
	if err := json.Unmarshal([]byte(a.CreateCoderConversation("codex", "gpt-5-codex", "high", "/w/proj", false)), &conv); err != nil {
		t.Fatal(err)
	}
	if conv.ID != "c9" || conv.Mode != "coder" || conv.Cwd != "/w/proj" || conv.WorkflowContext != "general" {
		t.Errorf("conversation = %+v", conv)
	}
	a.CreateCoderConversation("claude", "", "", "", true)
	got := readArgsLog(t, argsLog)
	want := []string{
		"--profile default --json chat history create --runtime codex --workflow general --mode coder --cwd /w/proj --model gpt-5-codex --effort high",
		"--profile default --json chat history create --runtime claude --workflow general --mode coder --new-workspace",
	}
	if strings.Join(got, "|") != strings.Join(want, "|") {
		t.Errorf("argv = %q\nwant %q", got, want)
	}
}

func TestApp_CreateCoderConversation_NeedsAFolder(t *testing.T) {
	bin, argsLog := chatFakeCLI(t)
	a, _ := newCLIChatApp(t, bin)
	if got := a.CreateCoderConversation("claude", "", "", "", false); !strings.Contains(got, `"error"`) {
		t.Errorf("CreateCoderConversation without a folder = %s, want an error", got)
	}
	if calls := readArgsLog(t, argsLog); len(calls) != 0 {
		t.Errorf("CLI called without a folder: %q", calls)
	}
}

// A --json failure prints {"error","code"} on stdout; the code must reach
// the frontend so it can explain coder_disabled / needs_monomind_update.
func TestApp_CoderCLIErrorsKeepTheCode(t *testing.T) {
	bin, _ := chatFakeCLI(t,
		fakeChatReply{match: "chat history create", code: 3, stderr: "coder mode is off\n",
			stdout: `{"error":"coder mode is off; enable it in Settings","code":"coder_disabled"}` + "\n"},
		fakeChatReply{match: "coder enable", code: 1, stderr: "boom\n"},
	)
	a, _ := newCLIChatApp(t, bin)
	var r struct{ Error, Code string }
	if err := json.Unmarshal([]byte(a.CreateCoderConversation("claude", "", "", "/w", false)), &r); err != nil {
		t.Fatal(err)
	}
	if r.Code != "coder_disabled" || r.Error != "coder mode is off; enable it in Settings" {
		t.Errorf("create refusal = %+v", r)
	}
	r = struct{ Error, Code string }{}
	if err := json.Unmarshal([]byte(a.CoderEnable()), &r); err != nil {
		t.Fatal(err)
	}
	if r.Error != "boom" || r.Code != "" {
		t.Errorf("plain failure = %+v, want the stderr line and no code", r)
	}
}

// A coder conversation's turn runs without --tools (the CLI refuses it);
// the frontend starts it with tools=false.
func TestChatTurnArgs_CoderTurnHasNoToolsFlag(t *testing.T) {
	got := strings.Join(chatTurnArgs("default", "conv-1", "turn-1", "inst", "write a program", false, false), " ")
	want := "--profile default --json chat --conversation conv-1 --turn turn-1 --instance inst -- write a program"
	if got != want {
		t.Errorf("chatTurnArgs = %q\nwant %q", got, want)
	}
}

func TestApp_CoderStopBackground_ShellsOut(t *testing.T) {
	bin, argsLog := chatFakeCLI(t, fakeChatReply{match: "coder stop-background", stdout: `{"stopped":[41822],"gone":[41830],"refused":[]}` + "\n"})
	a, _ := newCLIChatApp(t, bin)
	if got := a.CoderStopBackground("conv-1", "turn-1"); got != `{"stopped":[41822],"gone":[41830],"refused":[]}` {
		t.Errorf("CoderStopBackground = %s", got)
	}
	want := "--profile default --json coder stop-background --conversation conv-1 --turn turn-1"
	if got := readArgsLog(t, argsLog); len(got) != 1 || got[0] != want {
		t.Errorf("argv = %q, want %q", got, want)
	}
}
