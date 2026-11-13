package task

import (
	"strings"
	"testing"

	"github.com/monoes/mono-agent/internal/coderhost"
)

// The first-run notice tells the person to pause with `task session pursuit pause`: that verb must exist under that name.
func TestThePauseCommandTheNoticeNamesExists(t *testing.T) {
	want := "task session pursuit " + (Deps{}).newPursuitPauseCmd().Name()
	if want != coderhost.PursuitPauseCommand {
		t.Fatalf("the notice names %q but the verb is %q: the strip's Pause button would point nowhere", coderhost.PursuitPauseCommand, want)
	}
	if !strings.Contains(coderhost.PursuitNotice(), want) {
		t.Fatal("the notice does not name the pause command")
	}
}

func TestTheHostLineSaysIdleWithTheFixWhenNoJudgeIsChosen(t *testing.T) {
	h := coderhost.PursuitHostInfo{Missing: coderhost.PursuitMissingJudgePin, Reason: "judge-pin missing: run `coder host pursuit judge set RUNTIME/MODEL`"}
	line := hostPursuitLine(h, true, true)
	if !strings.Contains(line, "ON by default but idle") || !strings.Contains(line, "judge-pin missing") || strings.Contains(line, "OFF") {
		t.Fatalf("line = %q", line)
	}
	if off := hostPursuitLine(coderhost.PursuitHostInfo{OperatorOff: true, Reason: "turned off by the operator"}, true, true); !strings.Contains(off, "OFF") {
		t.Fatalf("explicit off line = %q", off)
	}
}
