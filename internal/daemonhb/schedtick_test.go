package daemonhb

import (
	"context"
	"path/filepath"
	"testing"
	"time"
)

// R16-D8: the heartbeat carries the time the scheduler loop last ran.
func TestHeartbeatCarriesSchedulerTick(t *testing.T) {
	t.Setenv("MONOAGENT_DAEMON_HEARTBEAT", filepath.Join(t.TempDir(), "hb.json"))
	before := time.Now().Add(-time.Second)
	NoteSchedulerTick()
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() { RunWith(ctx, Heartbeat{}, nil); close(done) }()
	deadline := time.Now().Add(2 * time.Second)
	var hb Heartbeat
	for time.Now().Before(deadline) {
		if hb, _ = Read(); !hb.TS.IsZero() {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	cancel()
	<-done
	if hb.SchedulerTick.Before(before) {
		t.Fatalf("scheduler_tick = %v, want a recent time", hb.SchedulerTick)
	}
}
