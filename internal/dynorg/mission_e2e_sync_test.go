package dynorg

import (
	"time"

	"github.com/monoes/mono-agent/internal/tasks"
)

// The end-to-end tests used to learn that a worker or a verifier was done by polling the
// task's state against a five second deadline. Under -race on a loaded machine a run can end
// later than that, and a step taken while the run's tail (its result record, its event, its
// place in the veterans) was still going saw a half-finished picture. A run's end is an event:
// its done channel closes after the hand-back, the status change and the result are all in
// place. These helpers wait for that event; the guard below only turns a hang into a failure
// that names the worker, it is not a deadline the tests depend on.
const e2eHangGuard = 2 * time.Minute

// workerEnded blocks until the run of the worker that works task id on c (if there is one
// now) has finished completely.
func (e *e2eRig) workerEnded(c *Conductor, id int64) {
	e.t.Helper()
	w := c.taskWorker(id)
	if w == nil {
		return
	}
	c.mu.Lock()
	done, wid := w.done, w.id
	c.mu.Unlock()
	if done == nil {
		return
	}
	select {
	case <-done:
	case <-time.After(e2eHangGuard):
		e.t.Fatalf("worker %s of task #%d never ended (a hang, not a slow run)", wid, id)
	}
}

// workersEnded is workerEnded for several tasks.
func (e *e2eRig) workersEnded(c *Conductor, ids ...int64) {
	e.t.Helper()
	for _, id := range ids {
		e.workerEnded(c, id)
	}
}

// waitStatus waits for the end of the task's worker (the event that moves a task to review,
// back to ready, or to a verdict), then checks the status; a status nothing runs behind (the
// lead's own write, a conductor being closed) is waited for the old way.
func (e *e2eRig) waitStatus(id int64, st tasks.Status) {
	e.t.Helper()
	e.workerEnded(e.c, id)
	e.missionRig.waitStatus(id, st)
}

// verified waits for the verifier's run to end, then checks the verdict is on the task.
func (e *e2eRig) verified(id int64) {
	e.t.Helper()
	e.workerEnded(e.c, id)
	if !e.task(id).Verified {
		e.t.Fatalf("task #%d is not verified after its verifier ended: %+v", id, e.task(id))
	}
}
