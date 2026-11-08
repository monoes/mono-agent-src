//go:build !windows

package main

import (
	"strings"
	"testing"
)

func TestApp_CoderSetDefaultAccess_ShellsOutWithTheLevel(t *testing.T) {
	bin, argsLog := chatFakeCLI(t, fakeChatReply{match: "coder set", stdout: `{"enabled":true,"defaultAccess":"full"}` + "\n"})
	a, _ := newCLIChatApp(t, bin)
	a.selectForTest("work")
	if got := a.CoderSetDefaultAccess("full"); got != `{"enabled":true,"defaultAccess":"full"}` {
		t.Errorf("CoderSetDefaultAccess = %s, want the status JSON verbatim", got)
	}
	if got := strings.Join(readArgsLog(t, argsLog), "\n"); !strings.Contains(got, "coder set --default-access full") {
		t.Errorf("CLI calls = %s", got)
	}
}

func TestApp_CoderSetDefaultAccess_RefusesUnknownLevelBeforeTheCLI(t *testing.T) {
	bin, argsLog := chatFakeCLI(t, fakeChatReply{match: "coder set", stdout: "{}\n"})
	a, _ := newCLIChatApp(t, bin)
	a.selectForTest("work")
	for _, lvl := range []string{"", "root", "--x"} {
		if got := a.CoderSetDefaultAccess(lvl); !strings.Contains(got, "invalid_access") {
			t.Errorf("level %q: %s", lvl, got)
		}
	}
	if got := readArgsLog(t, argsLog); len(got) != 0 {
		t.Errorf("CLI was called: %v", got)
	}
}
