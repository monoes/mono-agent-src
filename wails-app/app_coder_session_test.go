//go:build !windows

package main

import (
	"path/filepath"
	"strings"
	"testing"
)

func TestCoderSessionSettingsBindingsRunTheCLI(t *testing.T) {
	log := filepath.Join(t.TempDir(), "args.log")
	bin := fakeCLI(t, `echo "$*" >> '`+log+"'\n"+`echo '{"conversationId":"c1","unattended":true}'`+"\n")
	a, _ := newCLIChatApp(t, bin)
	if out := a.GetCoderSessionSettings("c1"); !strings.Contains(out, `"unattended":true`) {
		t.Fatalf("get = %s", out)
	}
	a.SetCoderSessionSettings("c1", `{"checkin_minutes":5}`, "false")
	a.SetCoderSessionSettings("-rf", "", "true") // an id that reads as a flag stays after --
	want := []string{
		"--profile default --json coder sessions settings get -- c1",
		`--profile default --json coder sessions settings set --patch={"checkin_minutes":5} --unattended=false -- c1`,
		"--profile default --json coder sessions settings set --unattended=true -- -rf",
	}
	if got := loggedArgs(t, log); strings.Join(got, "\n") != strings.Join(want, "\n") {
		t.Fatalf("calls = %q\nwant %q", got, want)
	}
}
