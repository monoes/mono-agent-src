//go:build doomsday

package doomsday

import (
	"context"
	"errors"
	"sync/atomic"
	"testing"
	"time"
)

// The loop never ends the process and never spins on a fault: a host that cannot be built is retried every tick and the
// persistent fault is logged once; the loop ends when its context does.
func TestStrategyLoopRetriesABuildFaultLogsItOnceAndStopsWithItsContext(t *testing.T) {
	var builds, logs atomic.Int32
	l := &StrategyLoop{
		Every: 5 * time.Millisecond,
		Build: func(context.Context) (*StrategyHost, error) { builds.Add(1); return nil, errors.New("no witness yet") },
		Logf:  func(string, ...any) { logs.Add(1) },
	}
	ctx, cancel := context.WithCancel(bg)
	done := make(chan struct{})
	go func() { l.Run(ctx); close(done) }()
	deadline := time.After(10 * time.Second)
	for builds.Load() < 4 {
		select {
		case <-deadline:
			t.Fatal("the loop did not retry the build")
		case <-time.After(5 * time.Millisecond):
		}
	}
	cancel()
	select {
	case <-done:
	case <-time.After(10 * time.Second):
		t.Fatal("the loop outlived its context")
	}
	if logs.Load() != 1 {
		t.Fatalf("the same fault was logged %d times", logs.Load())
	}
}

// With no run open a step is a no-op (the host is built once and ticks idle) over a real database.
func TestStrategyLoopStepWithNoRunDoesNothing(t *testing.T) {
	w, db := newWire(t, func() error { return nil }, fakeKernel{}, nil)
	w.w.DB = db.DB
	l := &StrategyLoop{Build: func(ctx context.Context) (*StrategyHost, error) {
		return w.w.StrategyHost(ctx, StrategyExtras{JudgeExec: (&fakeJudge{}).exec})
	}}
	rows := func() int {
		var n int
		_ = db.DB.QueryRow(`SELECT COUNT(*) FROM doomsday_audit`).Scan(&n)
		return n
	}
	for i := 0; i < 3; i++ {
		tk, err := l.Step(bg)
		if err != nil || tk.Idle != "no-run" || rows() != 0 {
			t.Fatalf("%+v %v rows %d", tk, err, rows())
		}
	}
}
