//go:build !windows

package main

import (
	"encoding/json"
	"strings"
	"testing"
)

const browserPermissionsJSON = `{"levels":{"coder":"dev","org":"full","chat":"full","other":"dev"},"sites":[{"origin":"https://github.com","rule":"allow"}],"kinds":[{"kind":"coder","label":"Coder chats","level":"dev","isDefault":true}],"catalog":[{"id":"dev","label":"Dev pages + allowlist","description":"Local and dev pages, plus sites you allow."}],"you":{"kind":"human","level":""}}`

// The permission bindings shell out to `browser permissions …` and return
// the CLI's JSON verbatim; a typed origin stays a positional argument.
func TestApp_BrowserPermissions_ShellOutAndReturnTheCLIJSON(t *testing.T) {
	bin, argsLog := chatFakeCLI(t,
		fakeChatReply{match: "browser permissions set", stdout: browserPermissionsJSON + "\n"},
		fakeChatReply{match: "browser permissions site", stdout: browserPermissionsJSON + "\n"},
		fakeChatReply{match: "browser permissions", stdout: browserPermissionsJSON + "\n"},
	)
	a, _ := newCLIChatApp(t, bin)

	if got := a.BrowserPermissions(); got != browserPermissionsJSON {
		t.Errorf("BrowserPermissions = %s, want the CLI JSON verbatim", got)
	}
	if got := a.SetBrowserPermission("coder", "ask"); got != browserPermissionsJSON {
		t.Errorf("SetBrowserPermission = %s", got)
	}
	if got := a.SetBrowserSiteRule("--json=false", "block"); got != browserPermissionsJSON {
		t.Errorf("SetBrowserSiteRule = %s", got)
	}
	want := []string{
		"browser permissions",
		"browser permissions set -- coder ask",
		"browser permissions site -- --json=false block",
	}
	var got []string
	for _, line := range readArgsLog(t, argsLog) {
		// The commands are machine-wide; the --profile the app's CLI helper
		// adds is ignored by them (profile gate class global).
		got = append(got, strings.TrimPrefix(line, "--profile default --json "))
	}
	if strings.Join(got, "|") != strings.Join(want, "|") {
		t.Errorf("CLI calls = %q\nwant %q", got, want)
	}
}

// A preset is one CLI call (one save of every kind), the preset after "--";
// the CLI's JSON comes back verbatim, in SetBrowserPermission's shape.
func TestApp_ApplyBrowserPermissionPreset_OneCLICall(t *testing.T) {
	bin, argsLog := chatFakeCLI(t, fakeChatReply{match: "browser permissions preset", stdout: browserPermissionsJSON + "\n"})
	a, _ := newCLIChatApp(t, bin)
	if got := a.ApplyBrowserPermissionPreset("off"); got != browserPermissionsJSON {
		t.Errorf("ApplyBrowserPermissionPreset = %s, want the CLI JSON verbatim", got)
	}
	lines := readArgsLog(t, argsLog)
	if len(lines) != 1 || strings.TrimPrefix(lines[0], "--profile default --json ") != "browser permissions preset -- off" {
		t.Errorf("CLI calls = %q, want exactly one: browser permissions preset -- off", lines)
	}
}

// A refusal (e.g. the CLI saw an agent context) keeps its message and code.
func TestApp_BrowserPermissions_RefusalKeepsTheCode(t *testing.T) {
	bin, _ := chatFakeCLI(t, fakeChatReply{match: "browser permissions set", code: 3, stderr: "refused\n",
		stdout: `{"error":"agents cannot change browser permissions","code":"agent_context"}` + "\n"})
	a, _ := newCLIChatApp(t, bin)
	var r struct{ Error, Code string }
	if err := json.Unmarshal([]byte(a.SetBrowserPermission("coder", "full-no-approvals")), &r); err != nil {
		t.Fatal(err)
	}
	if r.Error != "agents cannot change browser permissions" || r.Code != "agent_context" {
		t.Errorf("refusal = %+v", r)
	}
}
