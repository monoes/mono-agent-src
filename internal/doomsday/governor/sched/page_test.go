package sched

import (
	"fmt"
	"testing"
	"time"

	"github.com/monoes/mono-agent/internal/doomsday/governor"
)

// Items passed over for a backed-off provider must not hide the ones behind them: the pump pages past them.
func TestAPageOfBackedOffItemsDoesNotStarveAHealthyProvider(t *testing.T) {
	e := newEnv(t, 1)
	s := e.sched(t)
	for i := 0; i < 70; i++ {
		e.enq(t, s, fmt.Sprintf("lead-%03d", i), governor.ClassNormal, "A", fmt.Sprintf("mA%d", i))
	}
	e.enq(t, s, "lead-zzz", governor.ClassNormal, "B", "mB")
	for i := 0; i < 3; i++ {
		s.ReportRateLimit("A", 5*time.Minute)
	}
	res, err := s.Pump(ctx)
	if err != nil {
		t.Fatal(err)
	}
	got := members(res.Admitted)
	found := false
	for _, m := range got {
		found = found || m == "mB"
	}
	if !found {
		t.Fatalf("the healthy provider was starved by 70 backed-off items: admitted %v, %d of %d slots live", got, e.live(t), capProcess)
	}
}

// A second row naming a member that already holds its slot is a duplicate: it is consumed and the rows behind it are served.
func TestADuplicateMemberDoesNotStopThePump(t *testing.T) {
	e := newEnv(t, 1)
	s := e.sched(t)
	e.enq(t, s, "L1", governor.ClassNormal, "", "m1")
	e.enq(t, s, "L2", governor.ClassNormal, "", "m1")
	e.enq(t, s, "L3", governor.ClassNormal, "", "m3")
	res, err := s.Pump(ctx)
	if err != nil || len(res.Admitted) != 2 {
		t.Fatalf("a duplicate reference stops the pump: err=%v admitted=%v", err, members(res.Admitted))
	}
	if e.depth(t) != 0 {
		t.Fatalf("%d rows still queued: the duplicate must be consumed", e.depth(t))
	}
	if e.live(t) != 2 {
		t.Fatalf("%d live slots, want one per distinct member", e.live(t))
	}
}

// The charter cap is the ceiling whatever a policy answers: a policy that returns more than the cap changes nothing.
type loosePolicy struct{ governor.Policy }

func (p loosePolicy) Decide(l governor.Limits, s governor.Sample, err error) governor.Effective {
	e := p.Policy.Decide(l, s, err)
	e.Value, e.Admitting = l.MaxConcurrent+5, true
	return e
}

func TestTheSchedulerClampsAPolicyThatAnswersAboveTheCap(t *testing.T) {
	e := newEnv(t, 1)
	e.l.MaxConcurrent = 2 // the scheduler's cap; the slots store and the trigger would allow capProcess
	s := e.sched(t, func(c *Config) { c.Policy = loosePolicy{e.pol} })
	if v := s.Effective(ctx); v.Value > 2 {
		t.Fatalf("effective %d above the cap 2", v.Value)
	}
	if s.Effective(ctx).Value != 2 {
		t.Fatalf("effective %d", s.Effective(ctx).Value)
	}
}
