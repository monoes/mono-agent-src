//go:build !windows

package main

import (
	"strings"
	"testing"
)

func TestApp_CoderSetPeercredBinding_ShellsOutWithOnOrOff(t *testing.T) {
	bin, argsLog := chatFakeCLI(t, fakeChatReply{match: "coder set", stdout: `{"enabled":true,"peercredBinding":true}` + "\n"})
	a, _ := newCLIChatApp(t, bin)
	a.selectForTest("work")
	if got := a.CoderSetPeercredBinding(true); got != `{"enabled":true,"peercredBinding":true}` {
		t.Errorf("CoderSetPeercredBinding = %s, want the status JSON verbatim", got)
	}
	a.CoderSetPeercredBinding(false)
	got := strings.Join(readArgsLog(t, argsLog), "\n")
	if !strings.Contains(got, "coder set --peercred-binding on") || !strings.Contains(got, "coder set --peercred-binding off") {
		t.Errorf("CLI calls = %s", got)
	}
}
