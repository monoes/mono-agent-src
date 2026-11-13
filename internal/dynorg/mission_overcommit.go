package dynorg

import (
	"context"
	"fmt"

	"github.com/monoes/mono-agent/internal/ai/chatevents"
	"github.com/monoes/mono-agent/internal/tasks"
)

// missionOverCommitted is the note for the lead when the session or a budgeted ancestor is committed past its budget (its
// spend, what is held and what its children hold exceed it): nothing can start under it, so the operator is told once per
// over-committed session and the lead is told why. "" when there is room, or the state cannot be read (the refusal on
// the run itself still holds).
func (c *Conductor) missionOverCommitted(ctx context.Context, se tasks.Session) string {
	mc, err := c.missions()
	if err != nil {
		return ""
	}
	oc, err := mc.Store.OverCommitOf(ctx, mc.ProfileID, se.ID)
	if err != nil || !oc.Over {
		return ""
	}
	c.msched.mu.Lock()
	c.msched.init()
	first := !c.msched.overNoted[oc.Ancestor]
	c.msched.overNoted[oc.Ancestor] = true
	c.msched.mu.Unlock()
	if first {
		c.notice("mission.over_committed", fmt.Sprintf("Session #%d is over-committed: its budget of $%.2f is exceeded by $%.2f (its own spend plus what was carved to helpers). No new task starts under it; the operator raises the budget to resume.",
			oc.Ancestor, oc.BudgetUSD, oc.OverByUSD), chatevents.SeverityWarning)
	}
	return oc.Words()
}
