package dynorg

import (
	"context"
	"fmt"

	"github.com/monoes/mono-agent/internal/tasks"
)

// The lead's side of the wrap-up allowance (tasks.WrapUp, #1751): a mission that has spent its budget keeps its
// lead, to wrap up or finish. The lead is told so (WrapUpPrompt), hands back its own tasks and replans, and starts
// nothing new that costs money.

// WrapUp is the open mission session of this conversation and its wrap-up allowance; both nil when none applies.
func (m *MissionConfig) WrapUp(ctx context.Context) (*tasks.Session, *tasks.WrapUp, error) {
	if !m.Enabled() {
		return nil, nil, nil
	}
	se, err := m.active(ctx)
	if err != nil || se == nil {
		return nil, nil, err
	}
	w, err := m.Store.WrapUpOf(ctx, m.ProfileID, se.ID, m.actor(se.ID))
	if err != nil || w == nil {
		return nil, nil, err
	}
	return se, w, nil
}

// HaltIfSpent halts the lead's mission when it still runs although its budget is spent, as the charge of a lead turn
// does: the host calls it after a turn that ran under the wrap-up allowance, whose cost it records itself.
func (c *Conductor) HaltIfSpent(ctx context.Context) {
	if c.cfg.Mission == nil {
		return
	}
	if se, _, err := c.cfg.Mission.WrapUp(ctx); err == nil && se != nil && se.State == tasks.SessionRunning {
		c.missionBudget(context.WithoutCancel(ctx), *se)
	}
}

// wrapUpKeepsLeadClaims says whether the session is on its wrap-up allowance with some of it left: the lead then
// keeps the tasks it holds across its turns and whatever their lease says, because nothing could hand them out again
// and only their holder hands them back.
func (c *Conductor) wrapUpKeepsLeadClaims(ctx context.Context, sid int64) bool {
	mc, err := c.missions()
	if err != nil {
		return false
	}
	w, err := mc.Store.WrapUpOf(ctx, mc.ProfileID, sid, missionActor())
	return err == nil && w != nil && w.Lasts()
}

// paidTools are the org tools that start work that costs money.
var paidTools = map[string]bool{
	ToolSpawn: true, ToolMessage: true, ToolCompactWorker: true,
	ToolMissionAssign: true, ToolMissionVerify: true, ToolMissionSessionSpawn: true,
}

// refusePaidWork refuses a tool that starts paid work while the lead's mission has spent its budget.
func (c *Conductor) refusePaidWork(ctx context.Context, tool string) error {
	if !paidTools[tool] || c.cfg.Mission == nil {
		return nil
	}
	se, w, err := c.cfg.Mission.WrapUp(ctx)
	if err != nil {
		return fmt.Errorf("%s is refused: the mission's budget could not be read: %w", tool, err)
	}
	if w == nil {
		return nil
	}
	return fmt.Errorf("%s is refused: mission #%d has spent its budget ($%.2f of $%.2f) and starts no new paid work. "+
		"Under the wrap-up allowance you report (mission_report), hand back your own tasks (mission_task_finish), replan (mission_replan) and ask the user to raise the budget",
		tool, se.ID, se.SpentUSD, se.BudgetUSD)
}

// WrapUpPrompt tells the lead, for a turn admitted under the allowance, that the budget is used up, what is left of
// the allowance and what is still open to it. profile is the id the user's commands name.
func WrapUpPrompt(profile string, se tasks.Session, w tasks.WrapUp) string {
	return fmt.Sprintf("\n\nMISSION BUDGET USED UP. Mission #%d %q has spent $%.2f of its $%.2f budget. You have a wrap-up allowance of $%.2f, "+
		"of which $%.2f is left; what this turn costs counts against it, and once it is used up no further message runs until the user raises the budget. "+
		"Wrap up or finish now, in as few steps as you can: report where the mission stands (mission_report), hand back the tasks you hold (%s, with your result or a question), "+
		"bring the plan up to date for whoever goes on (%s), and tell the user what is left and ask them for more budget: they raise it with "+
		"`monoagentcli --profile %s task session budget %d --usd AMOUNT` and then `monoagentcli --profile %s task session resume %d` (you cannot). "+
		"No worker, verifier or session starts, and you must not start other paid work from your shell.",
		se.ID, se.Title, se.SpentUSD, se.BudgetUSD, w.AllowanceUSD, w.LeftUSD, ToolMissionTaskFinish, ToolMissionReplan, profile, se.ID, profile, se.ID)
}
