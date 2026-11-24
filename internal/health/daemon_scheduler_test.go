package health

import (
	"context"
	"strings"
	"testing"
)

// R16-D8: a live heartbeat with an old scheduler tick is a warning; a daemon
// that never reported a tick (0) is not.
func TestCheckDaemonWarnsWhenSchedulerTickIsOld(t *testing.T) {
	check := func(info DaemonInfo) Result {
		return checkDaemon(context.Background(), &Env{Daemon: func(context.Context) DaemonInfo { return info }})
	}
	old := check(DaemonInfo{Running: true, PID: 42, SchedulerAgeMS: (SchedulerStaleAfter.Milliseconds() + 1000)})
	if old.Status != StatusWarn || !strings.Contains(old.Summary, "scheduler") {
		t.Fatalf("old tick: %+v", old)
	}
	if fresh := check(DaemonInfo{Running: true, PID: 42, SchedulerAgeMS: 5000}); fresh.Status != StatusOK {
		t.Fatalf("fresh tick: %+v", fresh)
	}
	if none := check(DaemonInfo{Running: true, PID: 42}); none.Status != StatusOK {
		t.Fatalf("no tick reported: %+v", none)
	}
}
