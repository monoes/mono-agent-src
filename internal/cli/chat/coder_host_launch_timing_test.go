package chat

import (
	"fmt"
	"strings"
	"testing"
	"time"
)

func TestLaunchStepTimerLogsOnlySlowSteps(t *testing.T) {
	clock := time.Unix(0, 0)
	now := func() time.Time { return clock }
	var lines []string
	logf := func(f string, a ...any) { lines = append(lines, fmt.Sprintf(f, a...)) }
	mark := launchStepTimer(logf, "t1", 2*time.Second, now)

	clock = clock.Add(10 * time.Millisecond)
	mark("process started")
	clock = clock.Add(5 * time.Second)
	mark("identity read")
	clock = clock.Add(time.Millisecond)
	mark("released")

	if len(lines) != 1 || !strings.Contains(lines[0], `"identity read"`) || !strings.Contains(lines[0], "t1") {
		t.Fatalf("want one line naming the slow step, got %q", lines)
	}
	launchStepTimer(nil, "t2", 0, now)("x") // a nil logf is safe
}
