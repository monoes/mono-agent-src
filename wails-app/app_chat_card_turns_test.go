//go:build !windows

package main

import (
	"path/filepath"
	"strings"
	"testing"
)

// The cards come with their events from one command, whatever their number.
func TestGetChatCardTurnsListsTheCardTurns(t *testing.T) {
	log := filepath.Join(t.TempDir(), "args.log")
	bin := fakeCLI(t, `echo "$*" >> '`+log+"'\n"+`echo '{"items":[{"id":"system-uisurface-1","status":"completed","events":[{"turn_id":"system-uisurface-1","seq":1,"type":"ui.surface","payload":{"surfaceId":"hil-1"}}]}],"next_cursor":""}'`+"\n")
	a, _ := newCLIChatApp(t, bin)
	out := a.GetChatCardTurns("c1", nil)
	for _, want := range []string{`"id":"system-uisurface-1"`, `"events":[{`, `"turnId":"system-uisurface-1"`, `"surfaceId":"hil-1"`} {
		if !strings.Contains(out, want) {
			t.Fatalf("no %s in %s", want, out)
		}
	}
	a.GetChatCardTurns("c1", []string{"system-uisurface-1", "-bad", "system-uiaction-2"})
	a.GetChatTurns("c1", "", 1)
	want := []string{
		"--profile default --json chat history turns c1 --limit 200 --cards --events",
		"--profile default --json chat history turns c1 --limit 200 --cards --events --skip system-uisurface-1,system-uiaction-2",
		"--profile default --json chat history turns c1 --limit 1",
	}
	if got := loggedArgs(t, log); strings.Join(got, "\n") != strings.Join(want, "\n") {
		t.Fatalf("calls = %q\nwant %q", got, want)
	}
}
