package sched

import (
	"fmt"
	"testing"

	"github.com/monoes/mono-agent/internal/doomsday/governor"
)

// A hostile lead floods the queue; with room for one launch per round, another lead's first item and the boss item
// are admitted within one scheduling round each, and the flood still makes progress afterwards.
func TestAHostileLeadFloodCannotStarveAnotherLeadOrTheBossClass(t *testing.T) {
	e := newEnv(t, 1, failSample) // one slot per round
	s := e.sched(t)
	for i := 0; i < 300; i++ {
		e.enq(t, s, "hostile", governor.ClassNormal, "", fmt.Sprintf("h%03d", i))
	}
	e.enq(t, s, "victim", governor.ClassNormal, "", "v1")
	e.enq(t, s, "victim", governor.ClassNormal, "", "v2")
	e.enq(t, s, "boss", governor.ClassBoss, "", "b1")

	var order []string
	for round := 0; round < 6; round++ {
		res, err := s.Pump(ctx)
		if err != nil {
			t.Fatal(err)
		}
		for _, a := range res.Admitted {
			order = append(order, a.Request.MemberID)
			if err := s.Release(ctx, a.Request.MemberID); err != nil {
				t.Fatal(err)
			}
		}
	}
	if len(order) < 6 || order[0] != "b1" {
		t.Fatalf("the boss item must go first: %v", order)
	}
	pos := map[string]int{}
	for i, m := range order {
		pos[m] = i
	}
	if p, ok := pos["v1"]; !ok || p > 2 {
		t.Fatalf("the victim's first item waited more than one round: %v", order)
	}
	if p, ok := pos["v2"]; !ok || p > 4 {
		t.Fatalf("the victim's second item waited more than one round: %v", order)
	}
	if e.depth(t) != 300+3-6 {
		t.Fatalf("depth %d: each round admits exactly one", e.depth(t))
	}
}
