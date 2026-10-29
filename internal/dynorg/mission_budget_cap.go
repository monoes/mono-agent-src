package dynorg

import (
	"context"
	"errors"
	"fmt"

	"github.com/monoes/mono-agent/internal/ai/chatevents"
	"github.com/monoes/mono-agent/internal/tasks"
)

// missionReserve issues one exec of a mission worker its cap out of what the session has left: the
// session's budget less its spend, less what its other running workers were issued and less what its
// children hold (tasks.ReserveSpend, one atomic step, so a carve cannot take the same money). The
// session's budget_usd (the operator's) is the authority; want is the turn's own limit (0: none) and
// the grant is the smaller. granted is 0 for a session with no budget and no turn limit. What was
// issued is settled by settleReserve when the exec ends.
func (c *Conductor) missionReserve(ctx context.Context, w *worker, want float64) (granted float64, err error) {
	mc, err := c.missions()
	if err != nil || w.task == nil {
		return 0, err
	}
	granted, err = mc.Store.ReserveSpend(ctx, mc.ProfileID, w.task.session, want)
	if errors.Is(err, tasks.ErrLimit) {
		return 0, fmt.Errorf("%w: the mission's budget is spent (%v)", errBudgetRefused, err)
	}
	if err != nil {
		return 0, fmt.Errorf("reserving the budget of session #%d: %w", w.task.session, err)
	}
	w.task.spendMu.Lock()
	w.task.reserved += granted
	w.task.spendMu.Unlock()
	return granted, nil
}

// settleReserve ends a worker's reservation: what the run cost is recorded (recordSpend settles it
// against the reservation), then what is still reserved is given back. If the cost could not be
// recorded the reservation stays, so the money is not free before it is counted; missionFinish tries
// again, and a Conductor that never gets there (a crash) is cleaned up by missionReconcile.
func (c *Conductor) settleReserve(ctx context.Context, mc *MissionRun, w *worker) {
	if w.task == nil {
		return
	}
	c.recordSpend(ctx, mc, w)
	w.task.spendMu.Lock()
	defer w.task.spendMu.Unlock()
	c.mu.Lock()
	unrecorded := w.cost+w.liveCost > w.task.spent
	c.mu.Unlock()
	if unrecorded || w.task.reserved <= 0 {
		return
	}
	if err := mc.Store.SettleSpend(ctx, mc.ProfileID, w.task.session, w.task.reserved, 0, ""); err != nil {
		c.notice("mission.spend", fmt.Sprintf("The reservation of %s ($%.4f) could not be released: %v", w.id, w.task.reserved, err), chatevents.SeverityWarning)
		return
	}
	w.task.reserved = 0
}
