//go:build !windows

package main

import (
	"strings"
	"testing"
)

func TestApp_CoderSetPeercredBinding_ShellsOutWithOnOff(t *testing.T) {
	bin, argsLog := chatFakeCLI(t, fakeChatReply{match: "coder set", stdout: `{"enabled":true,"peercredBinding":true}` + "\n"})
	a, _ := newCLIChatApp(t, bin)
	a.selectForTest("work")
	if got := a.CoderSetPeercredBinding(true); got != `{"enabled":true,"peercredBinding":true}` {
		t.Errorf("CoderSetPeercredBinding = %s, want the status JSON verbatim", got)
	}
	a.CoderSetPeercredBinding(false)
	got := strings.Join(readArgsLog(t, argsLog), "\n")
	for _, want := range []string{"coder set --peercred-binding on", "coder set --peercred-binding off"} {
		if !strings.Contains(got, want) {
			t.Errorf("CLI calls = %s, want %q", got, want)
		}
	}
}
