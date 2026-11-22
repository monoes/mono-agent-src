package dynorg

import (
	"context"
	"fmt"
	"time"

	"github.com/monoes/mono-agent/internal/ai/chatevents"
	"github.com/monoes/mono-agent/internal/tasks"
)

// ChargeLeadTurn adds what the lead's own turn cost to the mission session its conversation is running, the way
// a worker's run is charged: without it the lead's work (a task it does itself, its planning and judging) never
// shows against the session's budget (#1751). A draft or a closed session is not charged, and a cost that is not
// above zero (the runtime reported none) adds nothing. The host calls it once, after the lead's turn.
func (c *Conductor) ChargeLeadTurn(ctx context.Context, usd float64) {
	if usd <= 0 || c.cfg.Mission == nil {
		return
	}
	mc, err := c.missions()
	if err != nil {
		return
	}
	ctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 10*time.Second)
	defer cancel()
	sid, err := c.sessionOf(ctx, mc, 0)
	if err != nil {
		return
	}
	se, err := mc.Store.GetSession(ctx, mc.ProfileID, sid, missionActor())
	if err != nil || se.State != tasks.SessionRunning || se.Archived {
		return
	}
	charged, err := mc.Store.AddSessionSpend(ctx, mc.ProfileID, sid, usd, "the lead's turn", operatorActor())
	if err != nil {
		c.notice("mission.spend", fmt.Sprintf("The cost of the lead's turn ($%.4f) could not be added to the session: %v", usd, err), chatevents.SeverityWarning)
		return
	}
	c.missionBudget(ctx, charged) // the 90 percent warning and the 100 percent halt, as after a worker's charge
}
