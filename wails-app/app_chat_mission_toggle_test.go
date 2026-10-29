//go:build !windows

package main

import (
	"path/filepath"
	"strings"
	"testing"
)

func TestChatMissionToggleBindingsRunTheCLI(t *testing.T) {
	log := filepath.Join(t.TempDir(), "args.log")
	bin := fakeCLI(t, `echo "$*" >> '`+log+"'\n"+`echo '{"conversationId":"c1","mission":{"on":true,"locked":true,"sessionId":7}}'`+"\n")
	a, _ := newCLIChatApp(t, bin)
	if out := a.GetChatMissionToggle("c1"); !strings.Contains(out, `"locked":true`) {
		t.Fatalf("get = %s", out)
	}
	a.SetChatMissionToggle("c1", true)
	a.SetChatMissionToggle("-rf", false) // an id that reads as a flag stays after --
	want := []string{
		"--profile default --json coder sessions settings get -- c1",
		"--profile default --json coder sessions settings set --mission=on -- c1",
		"--profile default --json coder sessions settings set --mission=off -- -rf",
	}
	if got := loggedArgs(t, log); strings.Join(got, "\n") != strings.Join(want, "\n") {
		t.Fatalf("calls = %q\nwant %q", got, want)
	}
}
