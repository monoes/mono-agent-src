package dynorg

import (
	"context"
	"fmt"
)

// missionExecCap is what one exec of a mission worker may still spend: the session's Commitment left (its
// budget less its spend, the reservations held on it and what its child sessions hold), read in one snapshot,
// the number a worker reservation is granted from. The session's budget_usd (the operator's, set before or
// after the approval) is the authority; the turn's own limit still applies on top (conductor_run.go takes the
// smaller). bounded is false for a session with no budget set.
//
// Read the cap BEFORE the exec's own reservation is made: the Commitment counts every reservation held on the
// session, the exec's own included, so after the exec reserved its share the cap is that much smaller and the
// exec would be capped by its own hold (and refused, once the hold is all that is left). A caller that
// reserves for an exec reads this first and reserves from what it returned
// (TestMissionExecCapIsReadBeforeTheExecsOwnReserve pins the order).
func (c *Conductor) missionExecCap(ctx context.Context, w *worker) (left float64, bounded bool, err error) {
	mc, err := c.missions()
	if err != nil || w.task == nil {
		return 0, false, err
	}
	com, err := mc.Store.Commitment(ctx, mc.ProfileID, w.task.session)
	if err != nil {
		return 0, false, fmt.Errorf("reading the budget of session #%d: %w", w.task.session, err)
	}
	left, bounded = com.Left()
	return left, bounded, nil
}
