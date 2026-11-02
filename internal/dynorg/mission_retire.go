package dynorg

import (
	"context"
	"fmt"
	"time"
)

// The operator can retire the idle worker of a mission task (`task session retire-worker`, which
// writes a `worker_retired` event of the session and ends the worker's stored row). The conductor
// of the lead's turn is a different process, so it learns of it from the store: before it resumes a
// mission worker it asks, and each look at the session (missionTick) retires the idle workers the
// operator retired. A retired worker keeps its report and the task keeps its evidence; it holds no
// session any more and takes no follow-up. A worker that is running is never retired from under it:
// the store refuses to retire while the task is claimed or a mission token is live.

// missionIdentity is the mission task an executor worked: its binding in this turn, or the link a veteran
// carries from an earlier one. A verifier has none, and nor has a worker of no task.
func (w *worker) missionIdentity() (session, task int64, gen int, ok bool) {
	if w.task != nil {
		if w.task.verifier {
			return 0, 0, 0, false
		}
		return w.task.session, w.task.task.ID, w.missionGen, true
	}
	if w.missionSession > 0 && w.missionTask > 0 {
		return w.missionSession, w.missionTask, w.missionGen, true
	}
	return 0, 0, 0, false
}

func retiredError(id string) error {
	return fmt.Errorf("%s was retired by the operator: its context is closed. Start a new worker for the task if more is needed", id)
}

// retiredRefusal refuses a follow-up to a mission worker the operator retired. It asks the
// store, so it holds before the conductor's next look at the session. A store that cannot answer
// refuses too: the follow-up would resume what the operator may have ended.
func (c *Conductor) retiredRefusal(ctx context.Context, id string) error {
	c.mu.Lock()
	w := c.workers[id]
	if w == nil {
		c.mu.Unlock()
		return nil
	}
	sid, tid, gen, bound := w.missionIdentity()
	if !bound {
		c.mu.Unlock()
		return nil
	}
	if w.retired {
		c.mu.Unlock()
		return retiredError(id)
	}
	if running(w.status) {
		c.mu.Unlock()
		return nil // the follow-up check says it is still working
	}
	c.mu.Unlock()
	mc, err := c.missions()
	if err != nil {
		return fmt.Errorf("could not check whether %s was retired: %w", id, err)
	}
	retired, err := mc.Store.WorkerRetired(ctx, mc.ProfileID, sid, tid, gen)
	if err != nil {
		return fmt.Errorf("could not check whether %s was retired: %w", id, err)
	}
	if retired {
		c.markRetired(w)
		return retiredError(id)
	}
	return nil
}

// markRetired drops what the idle worker kept to be resumed from.
func (c *Conductor) markRetired(w *worker) {
	c.mu.Lock()
	if running(w.status) {
		c.mu.Unlock()
		return
	}
	w.retired = true
	w.session = ""
	c.mu.Unlock()
}

// noteRetirements is the conductor's look at the idle mission workers of a session.
func (c *Conductor) noteRetirements(mc *MissionRun, sid int64) {
	type look struct {
		w         *worker
		task, gen int64
	}
	var idle []look
	c.mu.Lock()
	for _, id := range c.order {
		w := c.workers[id]
		if w == nil || w.retired || running(w.status) {
			continue
		}
		if s, t, g, ok := w.missionIdentity(); ok && s == sid {
			idle = append(idle, look{w, t, int64(g)})
		}
	}
	c.mu.Unlock()
	for _, l := range idle {
		ctx, cancel := context.WithTimeout(c.ctx, 5*time.Second)
		retired, err := mc.Store.WorkerRetired(ctx, mc.ProfileID, sid, l.task, int(l.gen))
		cancel()
		if err == nil && retired {
			c.markRetired(l.w)
		}
	}
}

// afterFollowupCheck, set by tests only, runs between the retirement check of a follow-up and its queueing.
var afterFollowupCheck func()

// afterMissionClaim, set by tests only, runs right after a worker's claim was written.
var afterMissionClaim func()
