//go:build !windows

package main

import (
	"os"
	"strings"
	"testing"
)

func TestPersonItemAnswerAskRunsTheCLI(t *testing.T) {
	const doc = `{"askId":4,"status":"answered"}`
	log, _ := taskCLIFake(t, doc, 0)
	a := newTaskTestApp(t)
	if got := a.PersonItemAnswerAsk(4, "--yes, go"); got != doc {
		t.Fatalf("not the CLI's stdout verbatim: %q", got)
	}
	want := "--profile|work|--json|task|session|person|answer-ask|--ask=4|--text=--yes, go|"
	if got := loggedArgs(t, log); strings.Join(got, "\n") != want {
		t.Fatalf("argv:\n%q\nwant:\n%q", got, want)
	}
}

func TestPersonItemAnswerAskRefusesBadInputWithoutTheCLI(t *testing.T) {
	log, _ := taskCLIFake(t, `{}`, 0)
	a := newTaskTestApp(t)
	for i, got := range []string{a.PersonItemAnswerAsk(0, "x"), a.PersonItemAnswerAsk(-2, "x"), a.PersonItemAnswerAsk(3, "  ")} {
		if !strings.Contains(got, `"error"`) || !strings.Contains(got, "invalid_input") {
			t.Fatalf("call %d not refused: %q", i, got)
		}
	}
	if _, err := os.Stat(log); err == nil {
		t.Fatal("the CLI ran for input the binding refuses")
	}
}
