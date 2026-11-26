package dynorg

import (
	"context"
	"fmt"
	"time"

	"github.com/monoes/mono-agent/internal/ai/chatevents"
	"github.com/monoes/mono-agent/internal/tasks"
)

// noteLeadMission remembers the mission that is running as the lead's turn begins: the turn is charged to it even
// if it is paused by the time the turn ends.
func (c *Conductor) noteLeadMission(ctx context.Context) {
	if c.cfg.Mission == nil {
		return
	}
	mc, err := c.missions()
	if err != nil {
		return
	}
	sid, err := c.sessionOf(ctx, mc, 0)
	if err != nil {
		return
	}
	if se, err := mc.Store.GetSession(ctx, mc.ProfileID, sid, missionActor()); err == nil && se.State == tasks.SessionRunning {
		c.msched.leadSession = sid
	}
}

// ChargeLeadTurn adds usd, a cost of the lead's own turn, to the mission session the turn runs for, the way a
// worker's run is charged: without it the lead's work (a task it does itself, its planning and judging) never
// shows against the session's budget (#1751). That session is the one running when the turn began, else the one
// running now. The one the turn runs for is charged also once it is paused (a worker's spend reached its budget,
// or the operator paused it); a draft or a closed session is never charged, and a cost that is not above zero (the
// runtime reported none) adds nothing. It reports whether usd was recorded. The host calls it as soon as the lead's
// turn ends, and again with what a compaction added.
func (c *Conductor) ChargeLeadTurn(ctx context.Context, usd float64) bool {
	if usd <= 0 || c.cfg.Mission == nil {
		return false
	}
	mc, err := c.missions()
	if err != nil {
		return false
	}
	ctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 10*time.Second)
	defer cancel()
	sid := c.msched.leadSession
	ranFor := sid != 0
	if !ranFor {
		if sid, err = c.sessionOf(ctx, mc, 0); err != nil {
			return false
		}
	}
	se, err := mc.Store.GetSession(ctx, mc.ProfileID, sid, missionActor())
	if err != nil || se.Archived || !(se.State == tasks.SessionRunning || ranFor && se.State == tasks.SessionPaused) {
		return false
	}
	charged, err := mc.Store.AddSessionSpend(ctx, mc.ProfileID, sid, usd, "the lead's turn", operatorActor())
	if err != nil {
		c.notice("mission.spend", fmt.Sprintf("The cost of the lead's turn ($%.4f) could not be added to the session: %v", usd, err), chatevents.SeverityWarning)
		return false
	}
	c.msched.leadSession = sid
	c.missionBudget(ctx, charged) // the 90 percent warning and the 100 percent halt, as after a worker's charge
	return true
}
