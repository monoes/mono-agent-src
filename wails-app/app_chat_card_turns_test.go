//go:build !windows

package main

import (
	"path/filepath"
	"strings"
	"testing"
)

func TestGetChatCardTurnsListsTheCardTurns(t *testing.T) {
	log := filepath.Join(t.TempDir(), "args.log")
	bin := fakeCLI(t, `echo "$*" >> '`+log+"'\n"+`echo '{"items":[{"id":"system-uisurface-1","status":"completed"}],"next_cursor":""}'`+"\n")
	a, _ := newCLIChatApp(t, bin)
	if out := a.GetChatCardTurns("c1"); !strings.Contains(out, `"system-uisurface-1"`) {
		t.Fatalf("%s", out)
	}
	a.GetChatTurns("c1", "", 1)
	want := []string{
		"--profile default --json chat history turns c1 --limit 200 --cards",
		"--profile default --json chat history turns c1 --limit 1",
	}
	if got := loggedArgs(t, log); strings.Join(got, "\n") != strings.Join(want, "\n") {
		t.Fatalf("calls = %q\nwant %q", got, want)
	}
}
