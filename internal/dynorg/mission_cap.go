package dynorg

import "fmt"

// MissionMaxAgents is the per-turn worker cap a confirmed mission turn gets when the operator
// has set none (the org default of 6 is too small for a plan of a dozen tasks). An operator's
// own --org-max-agents stays what it is.
const MissionMaxAgents = 12

// missionTurn: this Conductor runs a mission (the lead's mission is confirmed, or the
// scheduler is driven without a lead, as in tests and the CLI).
func (c *Conductor) missionTurn() bool {
	if c.cfg.Missions == nil || c.cfg.Missions.Store == nil {
		return false
	}
	return c.cfg.Mission == nil || c.cfg.Mission.Confirmed
}

// capFreeLocked says whether this spawn is outside Limits.MaxAgents: in a mission turn a
// verifier, and a run of a task that already had a worker this turn (a retry after a failed
// verification or a crash). They still count against the concurrency cap and the budget.
// The caller holds c.mu.
func (c *Conductor) capFreeLocked(req SpawnRequest) bool {
	if req.TaskID == 0 || !c.missionTurn() {
		return false
	}
	return req.verify || c.taskRuns[req.TaskID]
}

// countSpawnLocked books a spawn: a counted one against the cap, a task's first worker in
// the retry record. The caller holds c.mu.
func (c *Conductor) countSpawnLocked(req SpawnRequest, free bool) {
	if !free {
		c.spawned++
	}
	if req.TaskID != 0 && !req.verify {
		if c.taskRuns == nil {
			c.taskRuns = map[int64]bool{}
		}
		c.taskRuns[req.TaskID] = true
	}
}

// capError is the refusal at the worker cap, worded for who reads it.
func (c *Conductor) capError() error {
	n := c.cfg.Limits.MaxAgents
	if c.missionTurn() {
		return fmt.Errorf("this turn already has its %d task workers (verifiers and retries of a task do not count). "+
			"The tasks left stay Ready in the session: say so in your summary, and the next message continues them with mission_next. "+
			"Only the operator raises the cap (coder set --org-max-agents)", n)
	}
	return fmt.Errorf("this turn already has its %d workers; wait for them and use org_message for follow-ups", n)
}
