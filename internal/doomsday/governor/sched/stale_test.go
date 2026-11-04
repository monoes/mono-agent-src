package sched

import (
	"context"
	"testing"

	"github.com/monoes/mono-agent/internal/doomsday/governor"
)

// A queued launch whose send is gone is cancelled, not admitted: it reserves no slot, and the rows behind it are served.
func TestAStaleRowIsCancelledAndPinsNoSlot(t *testing.T) {
	e := newEnv(t, 1)
	s := e.sched(t, func(c *Config) {
		c.Stale = func(_ context.Context, it governor.Item) bool { return string(payloadMember(it)) == "gone" }
	})
	e.enq(t, s, "L1", governor.ClassNormal, "", "gone")
	e.enq(t, s, "L2", governor.ClassNormal, "", "live")
	res, err := s.Pump(ctx)
	if err != nil || len(res.Admitted) != 1 || members(res.Admitted)[0] != "live" {
		t.Fatalf("admitted %v (%v)", members(res.Admitted), err)
	}
	if e.live(t) != 1 || e.depth(t) != 0 {
		t.Fatalf("live %d, queued %d: the stale row must be cancelled and hold no slot", e.live(t), e.depth(t))
	}
}

func payloadMember(it governor.Item) string {
	_, r, _ := decode(it)
	return r.MemberID
}
