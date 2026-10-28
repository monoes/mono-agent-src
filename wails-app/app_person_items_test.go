//go:build !windows

package main

import (
	"os"
	"strings"
	"testing"
)

func TestPersonItemBindingsRunTheCLI(t *testing.T) {
	const doc = `{"items":[]}`
	log, _ := taskCLIFake(t, doc, 0)
	a := newTaskTestApp(t)
	for i, got := range []string{
		a.PersonItemsList(),
		a.PersonItemApprove(7),
		a.PersonItemDeny(7, "--split it"),
		a.PersonItemAsk(7, "why 30 tasks?"),
	} {
		if got != doc {
			t.Fatalf("call %d did not return the CLI's stdout verbatim: %q", i, got)
		}
	}
	p := "--profile|work|--json|task|session|person|"
	want := []string{
		p + "inbox|",
		p + "approve|7|",
		p + "deny|7|--note=--split it|",
		p + "ask|7|--text=why 30 tasks?|",
	}
	if got := loggedArgs(t, log); strings.Join(got, "\n") != strings.Join(want, "\n") {
		t.Fatalf("argv:\n%q\nwant:\n%q", got, want)
	}
}

func TestPersonItemBindingsRefuseBadInputWithoutTheCLI(t *testing.T) {
	log, _ := taskCLIFake(t, `{}`, 0)
	a := newTaskTestApp(t)
	for i, got := range []string{
		a.PersonItemApprove(0),
		a.PersonItemDeny(-1, "x"),
		a.PersonItemDeny(3, "  "),
		a.PersonItemAsk(3, ""),
	} {
		if !strings.Contains(got, `"error"`) || !strings.Contains(got, "invalid_input") {
			t.Fatalf("call %d not refused: %q", i, got)
		}
	}
	if _, err := os.Stat(log); err == nil {
		t.Fatal("the CLI ran for input the binding refuses")
	}
}
