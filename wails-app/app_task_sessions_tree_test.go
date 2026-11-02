//go:build !windows

package main

import (
	"os"
	"strings"
	"testing"
)

// The create and rename bindings pass user text so that a leading dash is
// never a flag, and refuse what cannot be a session before the CLI is run.
func TestTaskSessionCreateAndRenameArgv(t *testing.T) {
	const doc = `{"session":{"id":9,"title":"t"}}`
	log, _ := taskCLIFake(t, doc, 0)
	a := newTaskTestApp(t)
	for i, got := range []string{
		a.TaskSessionCreate("-x plan", "--goal text", 0, ""),
		a.TaskSessionCreate("child", "", 4, "direction"),
		a.TaskSessionRename(9, "--new name"),
	} {
		if got != doc {
			t.Fatalf("call %d did not return the CLI's stdout verbatim: %q", i, got)
		}
	}
	p := "--profile|work|--json|task|session|"
	want := []string{
		p + "new|--title=-x plan|--goal=--goal text|",
		p + "new|--title=child|--parent=4|--kind=direction|",
		p + "rename|9|--|--new name|",
	}
	if got := loggedArgs(t, log); strings.Join(got, "\n") != strings.Join(want, "\n") {
		t.Fatalf("argv:\n%q\nwant:\n%q", got, want)
	}
}

func TestTaskSessionCreateAndRenameRefuseBadInputWithoutTheCLI(t *testing.T) {
	log, _ := taskCLIFake(t, `{}`, 0)
	a := newTaskTestApp(t)
	for i, got := range []string{
		a.TaskSessionCreate("  ", "", 0, ""),
		a.TaskSessionCreate("x", "", -1, ""),
		a.TaskSessionRename(0, "x"),
		a.TaskSessionRename(3, " "),
	} {
		if !strings.Contains(got, `"error"`) || !strings.Contains(got, "invalid_input") {
			t.Fatalf("call %d not refused: %q", i, got)
		}
	}
	if _, err := os.Stat(log); err == nil {
		t.Fatal("the CLI ran")
	}
}
