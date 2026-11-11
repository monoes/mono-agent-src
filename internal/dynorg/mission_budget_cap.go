package dynorg

import (
	"context"
	"errors"
	"fmt"

	"github.com/monoes/mono-agent/internal/tasks"
)

// missionExecCap is what one exec of a mission worker may still spend: the session's Commitment left (its
// budget less its spend, the reservations held on it and what its child sessions hold), read in one snapshot,
// the number a worker reservation is granted from. The session's budget_usd (the operator's, set before or
// after the approval) is the authority, and so is every budgeted ancestor's: the cap is the smaller of the
// session's own remainder and what its ancestors allow (Store.ExecCap), and an ancestor that is over its
// budget refuses (errBudgetRefused). The turn's own limit still applies on top (conductor_run.go takes the
// smaller). bounded is false when neither the session nor an ancestor has a budget.
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
	left, bounded, err = mc.Store.ExecCap(ctx, mc.ProfileID, w.task.session)
	var oe *tasks.OverCommitError
	if errors.As(err, &oe) {
		return 0, true, fmt.Errorf("%w: %s", errBudgetRefused, oe.Words())
	}
	if errors.Is(err, tasks.ErrLimit) {
		return 0, true, fmt.Errorf("%w: session #%d: %v", errBudgetRefused, w.task.session, err)
	}
	if err != nil {
		return 0, false, fmt.Errorf("reading the budget of session #%d: %w", w.task.session, err)
	}
	return left, bounded, nil
}
