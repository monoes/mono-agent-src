package dynorg

import (
	"context"
	"fmt"
	"time"

	"github.com/monoes/mono-agent/internal/ai/chatevents"
	"github.com/monoes/mono-agent/internal/tasks"
)

// missionWatch makes sure the watcher looks after the session. One goroutine per
// Conductor, ended with it.
func (c *Conductor) missionWatch(sid int64) {
	mc, err := c.missions()
	if err != nil {
		return
	}
	c.msched.mu.Lock()
	c.msched.init()
	c.msched.sessions[sid] = true
	c.msched.mu.Unlock()
	c.msched.once.Do(func() {
		if c.ctx.Err() != nil {
			return
		}
		c.wg.Add(1)
		go func() {
			defer c.wg.Done()
			tick := time.NewTicker(mc.poll())
			defer tick.Stop()
			for {
				select {
				case <-c.ctx.Done():
					return
				case <-tick.C:
					c.missionTick(mc)
				}
			}
		}()
	})
}

// missionTick is one look at every session the Conductor works: an ended session stops its
// workers, a spent budget halts the mission, and a running session has its workers' leases
// renewed, its overlong runs cut and its orphan claims given back.
func (c *Conductor) missionTick(mc *MissionRun) {
	c.msched.mu.Lock()
	var sids []int64
	for sid := range c.msched.sessions {
		sids = append(sids, sid)
	}
	c.msched.mu.Unlock()
	for _, sid := range sids {
		for _, w := range c.sessionWorkers(sid) {
			c.chargeSession(w) // what a worker has cost so far, even between its events
		}
		se, err := mc.Store.GetSession(c.ctx, mc.ProfileID, sid, missionActor())
		if err != nil {
			continue
		}
		switch {
		case se.State == tasks.SessionStopped || se.State == tasks.SessionFailed || se.State == tasks.SessionDone || se.Archived:
			c.stopSessionWorkers(sid, "the session is "+string(se.State))
		case se.BudgetUSD > 0 && se.SpentUSD >= se.BudgetUSD*budgetHaltAt:
			c.missionHalt(c.ctx, se)
		case se.State == tasks.SessionRunning:
			c.missionBudget(c.ctx, se) // the 90 percent warning
			c.renewLeases(c.ctx, mc, sid)
			c.renewLeadLeases(c.ctx, mc, sid)
			c.cutOverlong(mc, sid)
		}
		c.missionReconcile(c.ctx, sid)
	}
}

// missionHalt is the 100 percent stop: the session is paused (only the operator resumes it,
// after raising the budget) and its workers are stopped; their claims are given back.
func (c *Conductor) missionHalt(ctx context.Context, se tasks.Session) {
	mc, err := c.missions()
	if err != nil {
		return
	}
	c.msched.mu.Lock()
	c.msched.init()
	first := !c.msched.halted[se.ID]
	c.msched.halted[se.ID] = true
	c.msched.mu.Unlock()
	if se.State == tasks.SessionRunning {
		_, _ = mc.Store.PauseSession(context.WithoutCancel(ctx), mc.ProfileID, se.ID, missionActor())
	}
	c.stopSessionWorkers(se.ID, "the session's budget is spent")
	if first {
		c.notice("mission.budget_halt", fmt.Sprintf("Mission %q has spent its budget ($%.2f of $%.2f) and is halted. The operator raises the budget and resumes it.",
			se.Title, se.SpentUSD, se.BudgetUSD), chatevents.SeverityError)
	}
}

// stopSessionWorkers stops the running workers on a session's tasks, remembering that the
// mission did it (so their stopping is not counted as their failure).
func (c *Conductor) stopSessionWorkers(sid int64, why string) {
	for _, w := range c.sessionWorkers(sid) {
		c.msched.mu.Lock()
		w.task.stopped = why
		c.msched.mu.Unlock()
		_, _ = c.Stop(w.id)
	}
}

// sessionWorkers are the workers on a session's tasks that still run.
func (c *Conductor) sessionWorkers(sid int64) []*worker {
	c.mu.Lock()
	defer c.mu.Unlock()
	var out []*worker
	for _, id := range c.order {
		if w := c.workers[id]; w != nil && w.task != nil && w.task.session == sid && running(w.status) {
			out = append(out, w)
		}
	}
	return out
}

// renewLeases renews the claim of every live worker, a third of a lease apart.
func (c *Conductor) renewLeases(ctx context.Context, mc *MissionRun, sid int64) {
	now := c.cfg.Now()
	for _, w := range c.sessionWorkers(sid) {
		if w.task.verifier {
			continue // it holds no claim to renew
		}
		c.msched.mu.Lock()
		due := now.Sub(w.task.renewedAt) >= mc.lease()/3
		c.msched.mu.Unlock()
		if !due {
			continue
		}
		if _, err := mc.Store.SessionClaim(ctx, mc.ProfileID, sid, w.task.task.ID, w.task.actor, mc.lease()); err == nil {
			c.msched.mu.Lock()
			w.task.renewedAt = now
			c.msched.mu.Unlock()
		}
	}
}

// renewLeadLeases renews the claims of the tasks the lead does itself, a third of a lease
// apart: the lead has no process of its own to renew them, and its Conductor lives as long as
// it does. Without it a long lead task goes stale and a worker may be started on it.
func (c *Conductor) renewLeadLeases(ctx context.Context, mc *MissionRun, sid int64) {
	now := c.cfg.Now()
	c.msched.mu.Lock()
	c.msched.init()
	var due []int64
	for id, oc := range c.msched.owned {
		if oc.session == sid && oc.name == missionLeadName && now.Sub(c.msched.leadRenewed[id]) >= tasks.DefaultLease/3 {
			due = append(due, id)
		}
	}
	c.msched.mu.Unlock()
	for _, id := range due {
		// only a task the lead still holds: one it finished or gave back is not claimed again
		cur, _, err := mc.Store.Get(ctx, mc.ProfileID, id)
		if err != nil || cur.Status != tasks.StatusInProgress || cur.Claim == nil || cur.Claim.By != missionLeadName {
			c.msched.mu.Lock()
			delete(c.msched.owned, id)
			c.msched.mu.Unlock()
			continue
		}
		actor := tasks.Actor{Kind: tasks.Agent, Name: missionLeadName, Session: sid, Task: id}
		if _, err := mc.Store.SessionClaim(ctx, mc.ProfileID, sid, id, actor, tasks.DefaultLease); err == nil {
			c.msched.mu.Lock()
			c.msched.leadRenewed[id] = now
			c.msched.mu.Unlock()
		}
	}
}

// cutOverlong stops a worker that has been working on its task past TaskTimeout, and a
// verifier past VerifierTimeout.
func (c *Conductor) cutOverlong(mc *MissionRun, sid int64) {
	now := c.cfg.Now()
	for _, w := range c.sessionWorkers(sid) {
		c.mu.Lock()
		working := w.status == chatevents.AgentWorking
		c.mu.Unlock()
		if !working {
			continue
		}
		c.msched.mu.Lock()
		if w.task.workingSince.IsZero() {
			w.task.workingSince = now
		}
		limit := mc.timeout()
		if w.task.verifier {
			limit = mc.verifierTimeout()
		}
		over := now.Sub(w.task.workingSince) > limit
		if over {
			w.task.timedOut = true
		}
		c.msched.mu.Unlock()
		if over {
			_, _ = c.Stop(w.id)
		}
	}
}

// missionReconcile gives back the claims of a session whose worker is gone: the ones this
// Conductor made and no worker holds any more (a crashed or timed-out run), and the ones a
// dead process left, once their lease has ended. The task goes back to Ready with its retry
// count untouched, and the scheduler offers it again.
func (c *Conductor) missionReconcile(ctx context.Context, sid int64) {
	mc, err := c.missions()
	if err != nil {
		return
	}
	held, err := mc.Store.SessionTasks(ctx, mc.ProfileID, sid, tasks.Filter{Statuses: []tasks.Status{tasks.StatusInProgress}, Limit: 500}, missionActor())
	if err != nil {
		return
	}
	for _, t := range held {
		if t.Claim == nil || !isMissionName(t.Claim.By, sid) || c.taskWorker(t.ID) != nil {
			continue
		}
		c.msched.mu.Lock()
		oc, ours := c.msched.owned[t.ID]
		ours = ours && oc.name == t.Claim.By
		pending := c.msched.pending[t.ID]
		c.msched.mu.Unlock()
		switch {
		case pending:
			continue
		case ours && t.Claim.By == missionLeadName:
			continue // the lead is doing it itself; its lease ends if it never finishes
		case ours:
			c.releaseIfHeld(sid, t.ID, *t.Claim, "no worker holds this task any more: given back to the scheduler")
		case t.Claim.Stale:
			// The list above may be older than a release and a new claim by the same name
			// (a restarted turn's worker): read the claim again, and look at the workers
			// again, so a fresh claim is not given back on the strength of a stale one.
			cur, _, err := mc.Store.Get(ctx, mc.ProfileID, t.ID)
			if err != nil || cur.Status != tasks.StatusInProgress || cur.Claim == nil || !cur.Claim.Stale || c.taskWorker(t.ID) != nil {
				continue
			}
			c.msched.mu.Lock()
			pending = c.msched.pending[t.ID]
			c.msched.mu.Unlock()
			if pending {
				continue
			}
			c.releaseIfHeld(sid, t.ID, *cur.Claim, "the claim's lease ended and nobody renewed it: given back to the scheduler")
		}
	}
}
