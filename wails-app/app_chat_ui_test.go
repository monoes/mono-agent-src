//go:build !windows

package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestChatUIBindingsRunTheCLI(t *testing.T) {
	log := filepath.Join(t.TempDir(), "args.log")
	stdin := filepath.Join(t.TempDir(), "stdin.log")
	bin := fakeCLI(t, `echo "$*" >> '`+log+"'\n"+`case "$*" in *" ui action "*) cat >> '`+stdin+"'; echo >> '"+stdin+"';; esac\n"+`echo '{"outcome":"accepted","delivered":"next_turn","message":"ok"}'`+"\n")
	a, _ := newCLIChatApp(t, bin)
	// Shell metacharacters in the envelope stay one argument.
	env := `{"version":"1.0","action":{"name":"x; echo pwned $(id)","surfaceId":"s","sourceComponentId":"b","context":{}}}`
	if out := a.ChatUIAction("c1", env); !strings.Contains(out, `"next_turn"`) {
		t.Fatalf("action = %s", out)
	}
	a.ChatUIAction("-flag", env) // an id that reads as a flag stays after --
	a.ChatUISyncHIL("c1")
	want := []string{
		"--profile default --json chat ui action --action - -- c1",
		"--profile default --json chat ui action --action - -- -flag",
		"--profile default --json chat ui sync-hil -- c1",
	}
	if got := loggedArgs(t, log); strings.Join(got, "\n") != strings.Join(want, "\n") {
		t.Fatalf("calls = %q\nwant %q", got, want)
	}
	// The envelope, which carries the person's edits, goes over stdin and never into an argument.
	if got, err := os.ReadFile(stdin); err != nil || string(got) != env+"\n"+env+"\n" {
		t.Fatalf("stdin = %q err=%v", got, err)
	}
}
