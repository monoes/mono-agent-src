package dynorg

import (
	"context"
	"fmt"
)

// missionExecCap is what one exec of a mission worker may still spend: the session's budget
// less what it has spent. The session's budget_usd (the operator's, set before or after the
// approval) is the authority; the turn's own limit still applies on top (conductor_run.go
// takes the smaller). bounded is false for a session with no budget set.
func (c *Conductor) missionExecCap(ctx context.Context, w *worker) (left float64, bounded bool, err error) {
	mc, err := c.missions()
	if err != nil || w.task == nil {
		return 0, false, err
	}
	se, err := mc.Store.GetSession(ctx, mc.ProfileID, w.task.session, missionActor())
	if err != nil {
		return 0, false, fmt.Errorf("reading the budget of session #%d: %w", w.task.session, err)
	}
	if se.BudgetUSD <= 0 {
		return 0, false, nil
	}
	return se.BudgetUSD - se.SpentUSD, true, nil
}
