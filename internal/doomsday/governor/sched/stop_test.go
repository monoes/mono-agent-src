package sched

import (
	"errors"
	"fmt"
	"testing"
	"time"

	"github.com/monoes/mono-agent/internal/doomsday/governor"
	"github.com/monoes/mono-agent/internal/doomsday/governor/machine"
)

func TestNothingIsAdmittedAfterStopAndTheQueueIsCancelled(t *testing.T) {
	e := newEnv(t, 2)
	s := e.sched(t)
	for _, m := range []string{"a", "b", "c"} {
		e.enq(t, s, "l", governor.ClassNormal, "", m)
	}
	if err := s.TryAdmit(ctx, "", proc("running")); err != nil {
		t.Fatal(err)
	}
	n, err := s.Stop(ctx)
	if err != nil || n != 3 {
		t.Fatalf("stop cancelled %d: %v", n, err)
	}
	if err := s.TryAdmit(ctx, "", proc("late")); !errors.Is(err, ErrStopped) {
		t.Fatalf("TryAdmit after Stop: %v", err)
	}
	if res, err := s.Pump(ctx); !errors.Is(err, ErrStopped) || len(res.Admitted) != 0 {
		t.Fatalf("Pump after Stop: %+v %v", res, err)
	}
	if _, err := s.Enqueue(ctx, governor.Item{Lead: "l", Class: governor.ClassNormal, Payload: payload("", "x")}); !errors.Is(err, ErrStopped) {
		t.Fatalf("Enqueue after Stop: %v", err)
	}
	if e.depth(t) != 0 || e.live(t) != 1 {
		t.Fatalf("depth %d live %d: the running member is not touched, the queue is empty", e.depth(t), e.live(t))
	}
	if err := s.Release(ctx, "running"); err != nil || e.live(t) != 0 {
		t.Fatalf("release after Stop must still work: %v live %d", err, e.live(t))
	}
}

func TestStopNeverWaitsOnAWedgedQueue(t *testing.T) {
	e := newEnv(t, 2)
	s := e.sched(t, func(c *Config) { c.Queue = wedgedQueue{e.queue} })
	bound(t, 3*time.Second, func() {
		if _, err := s.Stop(ctx); err == nil {
			t.Error("a wedged cancel should report its error")
		}
	})
	if err := s.TryAdmit(ctx, "", proc("x")); !errors.Is(err, ErrStopped) {
		t.Fatalf("admission after a Stop with a wedged queue: %v", err)
	}
}

func TestNotifyStopTakesNoLockEvenWhileAnAdmissionIsInFlight(t *testing.T) {
	e := newEnv(t, 2)
	entered, release := make(chan struct{}), make(chan struct{})
	hook := &slotsHook{Slots: e.slots, after: func() { close(entered); <-release }}
	s := e.sched(t, func(c *Config) { c.Slots = hook })
	errc := make(chan error, 1)
	go func() { errc <- s.TryAdmit(ctx, "", proc("a")) }()
	select {
	case <-entered:
	case <-time.After(3 * time.Second):
		t.Fatal("admission never reached the reserve")
	}
	bound(t, time.Second, s.NotifyStop)
	close(release)
	select {
	case err := <-errc:
		if !errors.Is(err, ErrStopped) {
			t.Fatalf("an admission that straddled Stop must lose: %v", err)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("admission never returned")
	}
	if e.live(t) != 0 {
		t.Fatalf("slot leaked: %d", e.live(t))
	}
}

func TestAHungSamplerNeverBlocksAdmissionAndLowersToTheSafeMinimum(t *testing.T) {
	e := newEnv(t, 2, machine.Step{Hang: true})
	s := e.sched(t, func(c *Config) { c.SampleTimeout = 50 * time.Millisecond })
	bound(t, 3*time.Second, func() {
		for i := 0; i < 4; i++ {
			_ = s.TryAdmit(ctx, "", proc(fmt.Sprintf("m%d", i)))
		}
	})
	if n := e.live(t); n != 2 {
		t.Fatalf("hung sampler: live %d, want the safe minimum 2", n)
	}
}

func TestAPanickingSamplerIsTheSafeMinimum(t *testing.T) {
	e := newEnv(t, 2, machine.Step{Panic: true})
	s := e.sched(t)
	if got := s.Effective(ctx); got.Value != 2 {
		t.Fatalf("%+v", got)
	}
}
