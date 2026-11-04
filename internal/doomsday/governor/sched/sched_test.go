package sched

import (
	"errors"
	"reflect"
	"testing"
	"time"

	"github.com/monoes/mono-agent/internal/doomsday/governor"
	"github.com/monoes/mono-agent/internal/doomsday/governor/machine"
)

func proc(m string) governor.Request {
	return governor.Request{Kind: governor.KindProcess, MemberID: m}
}

func TestEffectiveIsThePolicyDecisionAndNeverAboveTheCap(t *testing.T) {
	e := newEnv(t, 2)
	s := e.sched(t)
	if got := s.Effective(ctx); got.Value != capProcess || !got.Admitting {
		t.Fatalf("healthy machine: %+v", got)
	}
	e2 := newEnv(t, 2, failSample)
	if got := e2.sched(t).Effective(ctx); got.Value != 2 || got.Cap != capProcess {
		t.Fatalf("a failed read is the safe minimum: %+v", got)
	}
}

func TestTryAdmitNeverAdmitsAboveTheEffectiveValue(t *testing.T) {
	e := newEnv(t, 2, failSample) // effective value 2, charter cap 4
	s := e.sched(t)
	for i, m := range []string{"a", "b"} {
		if err := s.TryAdmit(ctx, "", proc(m)); err != nil {
			t.Fatalf("admit %d: %v", i, err)
		}
	}
	for _, m := range []string{"c", "d"} {
		if err := s.TryAdmit(ctx, "", proc(m)); !errors.Is(err, governor.ErrNoSlot) {
			t.Fatalf("above the effective value: %v", err)
		}
	}
	if n := e.live(t); n != 2 {
		t.Fatalf("live %d, want 2", n)
	}
}

func TestNothingIsAdmittedWhileNotAdmitting(t *testing.T) {
	low := machine.Healthy()
	low.MemAvailFrac = 0.05 // under MemStopFrac
	e := newEnv(t, 2, machine.Step{Sample: low})
	s := e.sched(t)
	if eff := s.Effective(ctx); eff.Admitting {
		t.Fatalf("setup: %+v", eff)
	}
	if err := s.TryAdmit(ctx, "", proc("a")); !errors.Is(err, governor.ErrNoSlot) {
		t.Fatalf("TryAdmit while not admitting: %v", err)
	}
	e.enq(t, s, "l", governor.ClassNormal, "", "q1")
	res, err := s.Pump(ctx)
	if err != nil || len(res.Admitted) != 0 {
		t.Fatalf("Pump while not admitting: %+v %v", res, err)
	}
	if e.live(t) != 0 || e.depth(t) != 1 {
		t.Fatalf("live %d depth %d", e.live(t), e.depth(t))
	}
}

func TestNothingIsAdmittedWhenTheRunIsNotRunning(t *testing.T) {
	e := newEnv(t, 2)
	s := e.sched(t)
	e.enq(t, s, "l", governor.ClassNormal, "", "q1")
	e.view.notLive.Store(true)
	res, err := s.Pump(ctx)
	if !errors.Is(err, governor.ErrNotRunning) || len(res.Admitted) != 0 || e.live(t) != 0 {
		t.Fatalf("Pump on a run that is not running: %+v %v live %d", res, err, e.live(t))
	}
}

func TestPumpAdmitsUpToTheValueAndAReleaseWakesThenAdmitsTheNext(t *testing.T) {
	e := newEnv(t, 2, failSample)
	s := e.sched(t)
	for _, m := range []string{"m1", "m2", "m3"} {
		e.enq(t, s, "l", governor.ClassNormal, "", m)
	}
	select {
	case <-s.Wake():
	default:
		t.Fatal("an enqueue should signal the scheduler")
	}
	res, err := s.Pump(ctx)
	if err != nil || !reflect.DeepEqual(members(res.Admitted), []string{"m1", "m2"}) || res.Deferred != 1 {
		t.Fatalf("first pump: %v %+v", err, res)
	}
	if e.live(t) != 2 || e.depth(t) != 1 {
		t.Fatalf("live %d depth %d", e.live(t), e.depth(t))
	}
	if err := s.Release(ctx, "m1"); err != nil {
		t.Fatal(err)
	}
	select {
	case <-time.After(2 * time.Second):
		t.Fatal("a release should wake the scheduler")
	case <-s.Wake():
	}
	res, err = s.Pump(ctx)
	if err != nil || !reflect.DeepEqual(members(res.Admitted), []string{"m3"}) || e.live(t) != 2 {
		t.Fatalf("second pump: %v %+v live %d", err, res, e.live(t))
	}
}

// failing MarkAdmitted must not leak the slot the scheduler reserved first.
func TestFailedMarkAdmittedReleasesTheSlot(t *testing.T) {
	cases := map[string]func(e *env) governor.Queue{
		"item cancelled between peek and admit": func(e *env) governor.Queue {
			return &markHook{Queue: e.queue, before: func() { _, _ = e.queue.CancelAll(ctx, "p", e.l.Generation) }}
		},
		"database error": func(e *env) governor.Queue {
			return &markHook{Queue: e.queue, fail: errors.New("disk I/O error")}
		},
	}
	for name, wrap := range cases {
		t.Run(name, func(t *testing.T) {
			e := newEnv(t, 2)
			s := e.sched(t)
			e.enq(t, s, "l", governor.ClassNormal, "", "m1")
			s2 := e.sched(t, func(c *Config) { c.Queue = wrap(e) })
			res, _ := s2.Pump(ctx)
			if len(res.Admitted) != 0 {
				t.Fatalf("admitted despite the failed mark: %+v", res)
			}
			if n := e.live(t); n != 0 {
				t.Fatalf("slot leaked: live %d", n)
			}
		})
	}
}

func TestStopDuringTheReserveLosesTheAdmissionAndLeaksNothing(t *testing.T) {
	e := newEnv(t, 2)
	s := e.sched(t)
	e.enq(t, s, "l", governor.ClassNormal, "", "m1")
	var self *Scheduler
	hook := &slotsHook{Slots: e.slots, after: func() { self.NotifyStop() }}
	self = e.sched(t, func(c *Config) { c.Slots = hook })
	res, err := self.Pump(ctx)
	if !errors.Is(err, ErrStopped) || len(res.Admitted) != 0 {
		t.Fatalf("%+v %v", res, err)
	}
	if e.live(t) != 0 || e.depth(t) != 1 {
		t.Fatalf("live %d depth %d (the item must stay queued, the slot must be freed)", e.live(t), e.depth(t))
	}
}

func TestBossClassIsAdmittedBeforeNormalWork(t *testing.T) {
	e := newEnv(t, 1, failSample) // value 1: exactly one admission per round
	s := e.sched(t)
	for i := 0; i < 5; i++ {
		e.enq(t, s, "a", governor.ClassNormal, "", "n"+string(rune('0'+i)))
	}
	e.enq(t, s, "b", governor.ClassBoss, "", "boss1")
	res, err := s.Pump(ctx)
	if err != nil || !reflect.DeepEqual(members(res.Admitted), []string{"boss1"}) {
		t.Fatalf("%v %+v", err, res)
	}
}

func TestProviderBackoffNarrowsOnlyThatProviderUntilItExpires(t *testing.T) {
	e := newEnv(t, 2)
	s := e.sched(t)
	s.ReportRateLimit("slow", time.Second)
	s.ReportRateLimit("slow", time.Second) // second 429: value cap>>2 == 1
	if err := s.TryAdmit(ctx, "slow", proc("s1")); err != nil {
		t.Fatalf("the first launch fits: %v", err)
	}
	if err := s.TryAdmit(ctx, "slow", proc("s2")); !errors.Is(err, governor.ErrNoSlot) {
		t.Fatalf("a backing-off provider must be held to its narrowed value: %v", err)
	}
	if err := s.TryAdmit(ctx, "fast", proc("f1")); err != nil {
		t.Fatalf("another provider is unaffected: %v", err)
	}
	e.clock.Advance(20 * time.Minute)
	if err := s.TryAdmit(ctx, "slow", proc("s2")); err != nil {
		t.Fatalf("after the window the provider is back: %v", err)
	}
}

func TestPumpPassesOverABackingOffProviderWithoutBlockingOthers(t *testing.T) {
	e := newEnv(t, 2)
	s := e.sched(t)
	s.ReportRateLimit("slow", time.Second)
	s.ReportRateLimit("slow", time.Second)
	if err := s.TryAdmit(ctx, "slow", proc("s0")); err != nil {
		t.Fatal(err)
	}
	e.enq(t, s, "a", governor.ClassNormal, "slow", "s1")
	e.enq(t, s, "a", governor.ClassNormal, "fast", "f1")
	res, err := s.Pump(ctx)
	if err != nil || !reflect.DeepEqual(members(res.Admitted), []string{"f1"}) {
		t.Fatalf("%v %+v", err, res)
	}
}

func TestUndecodableItemStaysQueuedAndDoesNotBlockTheRest(t *testing.T) {
	e := newEnv(t, 2)
	s := e.sched(t)
	if _, err := s.Enqueue(ctx, governor.Item{Lead: "a", Class: governor.ClassNormal, Payload: []byte("garbage")}); err != nil {
		t.Fatal(err)
	}
	e.enq(t, s, "b", governor.ClassNormal, "", "ok1")
	res, err := s.Pump(ctx)
	if err != nil || res.Skipped != 1 || !reflect.DeepEqual(members(res.Admitted), []string{"ok1"}) {
		t.Fatalf("%v %+v", err, res)
	}
}

func TestSnapshotIsBoundedAndCarriesNoPayloadOrMember(t *testing.T) {
	e := newEnv(t, 2)
	s := e.sched(t)
	e.enq(t, s, "a", governor.ClassNormal, "p1", "secretish-member")
	for i := 0; i < 3*MaxProviders; i++ {
		s.ReportRateLimit("prov-"+string(rune('a'+i%26))+string(rune('a'+i/26)), time.Second)
	}
	snap, err := s.Snapshot(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if snap.Cap != capProcess || snap.Effective != capProcess || snap.QueueRows != 1 || snap.QueueMaxRows != governor.DefaultQueueMaxRows {
		t.Fatalf("%+v", snap)
	}
	if len(snap.Providers) > MaxProviders {
		t.Fatalf("providers %d exceed the bound", len(snap.Providers))
	}
	if snap.LastSample.IsZero() {
		t.Fatal("LastSample not set")
	}
}

func TestNewRefusesMissingPartsAndACapUnderOne(t *testing.T) {
	e := newEnv(t, 2)
	if _, err := New(Config{Limits: e.l}); err == nil {
		t.Fatal("missing parts accepted")
	}
	zero := e.l
	zero.MaxConcurrent = 0
	if _, err := New(Config{Limits: zero, Sampler: e.smp, Policy: e.pol, Slots: e.slots, Queue: e.queue, View: e.view, Clock: e.clock}); err == nil {
		t.Fatal("a zero cap accepted")
	}
}
