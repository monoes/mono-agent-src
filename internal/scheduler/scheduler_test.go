package scheduler

import (
	"sync/atomic"
	"testing"
	"time"

	"github.com/rs/zerolog"
)

func TestNextPeriod_BeforeWindowReturnsStart(t *testing.T) {
	now := time.Now().Truncate(time.Minute)
	start := now.Add(30 * time.Minute)
	end := now.Add(60 * time.Minute)
	got := NextPeriod(start, end, 15)
	if got == nil || !got.Equal(start) {
		t.Fatalf("NextPeriod before window = %v, want start %v", got, start)
	}
}

func TestNextPeriod_AfterWindowReturnsNil(t *testing.T) {
	now := time.Now().Truncate(time.Minute)
	start := now.Add(-60 * time.Minute)
	end := now.Add(-30 * time.Minute)
	if got := NextPeriod(start, end, 15); got != nil {
		t.Fatalf("NextPeriod after window = %v, want nil", got)
	}
}

func TestNextPeriod_WithinWindowAdvances(t *testing.T) {
	now := time.Now().Truncate(time.Minute)
	start := now.Add(-10 * time.Minute)
	end := now.Add(60 * time.Minute)
	got := NextPeriod(start, end, 15)
	if got == nil {
		t.Fatal("NextPeriod within window = nil, want a future time")
	}
	if got.Before(now) {
		t.Fatalf("NextPeriod within window = %v, is before now %v", got, now)
	}
}

func TestNextPeriod_NextIntervalPastEndReturnsNil(t *testing.T) {
	now := time.Now().Truncate(time.Minute)
	start := now.Add(-10 * time.Minute)
	end := now.Add(2 * time.Minute) // next 15-min boundary lands past end
	if got := NextPeriod(start, end, 15); got != nil {
		t.Fatalf("NextPeriod = %v, want nil when next interval exceeds end", got)
	}
}

func TestAddWorkflowJob_InvalidSpec(t *testing.T) {
	s := NewScheduler(zerolog.Nop())
	if _, err := s.AddWorkflowJob("not a cron spec", func() {}); err == nil {
		t.Error("expected an error for an invalid cron spec")
	}
}

func TestAddAndRemoveJob(t *testing.T) {
	s := NewScheduler(zerolog.Nop())
	id, err := s.AddWorkflowJob("@every 1h", func() {})
	if err != nil {
		t.Fatalf("AddWorkflowJob valid spec: %v", err)
	}
	s.RemoveJob(id) // must not panic
}

// A panic in a job must not take the process down, and the schedule keeps
// firing: the next tick of the same job and of other jobs still runs.
func TestScheduler_PanickingJobDoesNotKillTheScheduler(t *testing.T) {
	s := NewScheduler(zerolog.Nop())
	var ticks atomic.Int32
	var panics atomic.Int32
	if _, err := s.AddWorkflowJob("* * * * * *", func() {
		panics.Add(1)
		panic("boom")
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := s.AddWorkflowJob("* * * * * *", func() { ticks.Add(1) }); err != nil {
		t.Fatal(err)
	}
	s.Start()
	defer s.Stop()
	deadline := time.Now().Add(6 * time.Second)
	for time.Now().Before(deadline) && (ticks.Load() < 2 || panics.Load() < 2) {
		time.Sleep(50 * time.Millisecond)
	}
	if ticks.Load() < 2 || panics.Load() < 2 {
		t.Fatalf("ticks=%d panics=%d, want both >= 2 (scheduler survived the panic)", ticks.Load(), panics.Load())
	}
}

// R11-09: starting the scheduler applies the attachment retention.
func TestScheduler_PrunesAttachmentsOnStart(t *testing.T) {
	var n atomic.Int32
	old := prune
	prune = func() (int, error) { n.Add(1); return 0, nil }
	defer func() { prune = old }()
	s := NewScheduler(zerolog.Nop())
	s.Start()
	defer s.Stop()
	deadline := time.Now().Add(3 * time.Second)
	for n.Load() < 1 && time.Now().Before(deadline) {
		time.Sleep(10 * time.Millisecond)
	}
	if n.Load() < 1 {
		t.Fatal("the scheduler never applied the attachment retention")
	}
}

// R16-D8: a running scheduler reports its ticks to the daemon heartbeat.
func TestScheduler_ReportsTicks(t *testing.T) {
	var n atomic.Int32
	oldSpec, oldNote := tickSpec, noteTick
	tickSpec, noteTick = "@every 1s", func() { n.Add(1) }
	defer func() { tickSpec, noteTick = oldSpec, oldNote }()
	s := NewScheduler(zerolog.Nop())
	s.Start()
	defer s.Stop()
	deadline := time.Now().Add(4 * time.Second)
	for n.Load() < 2 && time.Now().Before(deadline) {
		time.Sleep(20 * time.Millisecond)
	}
	if n.Load() < 2 {
		t.Fatalf("scheduler reported %d ticks, want at least 2 (one at start, one from the loop)", n.Load())
	}
}
