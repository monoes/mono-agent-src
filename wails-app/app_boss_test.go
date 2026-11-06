//go:build !windows

package main

import (
	"path/filepath"
	"strings"
	"testing"
)

// The steering binding passes the chat and the text after "--" (a text that reads as a flag stays text) and returns what
// the CLI said, a refusal included.
func TestSteerBossRunsTheCLIAndReturnsItsAnswer(t *testing.T) {
	log := filepath.Join(t.TempDir(), "args.log")
	bin := fakeCLI(t, `echo "$*" >> '`+log+"'\n"+`echo '{"commandId":3,"conversationId":"boss-conv","turnId":"t-1"}'`+"\n")
	a, _ := newCLIChatApp(t, bin)
	if out := a.SteerBoss("c1", "--lift-everything"); !strings.Contains(out, `"conversationId":"boss-conv"`) {
		t.Fatalf("steer = %s", out)
	}
	want := "--profile default --json coder sessions steer -- c1 --lift-everything"
	if got := loggedArgs(t, log); len(got) != 1 || got[0] != want {
		t.Fatalf("calls = %q, want %q", got, want)
	}
}
