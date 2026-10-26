package dynorg

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/monoes/mono-agent/internal/ai/chatevents"
	"github.com/monoes/mono-agent/internal/monomind"
	"github.com/monoes/mono-agent/internal/tasks"
)

// Giving a mission's tasks to executors and taking the results back. A worker is staffed
// and run by the ordinary spawn path; what this file adds is the task around it: the claim,
// the brief, the binding of the worker's process to its one task, the report that becomes an
// artifact, the failure that becomes a comment, and the spend that goes on the session.

// startTask gives one ready task to an executor.
func (c *Conductor) startTask(ctx context.Context, sid int64, t tasks.Task, exec string, req SpawnRequest) (StartedTask, error) {
	st := StartedTask{TaskID: t.ID, Title: t.Title, Executor: exec}
	if exec != ExecLead {
		req.TaskID, req.executor = t.ID, exec
		if exec == ExecSubagent {
			req.Access = ProfileResearch
		}
		info, err := c.spawn(ctx, req, nil)
		if err != nil {
			return st, err
		}
		st.AgentID = info.ID
		return st, nil
	}
	b, brief, err := c.missionPrepare(ctx, SpawnRequest{TaskID: t.ID, executor: ExecLead}, nil, missionLeadName)
	if err != nil {
		return st, err
	}
	if err := c.missionClaim(ctx, b); err != nil {
		return st, err
	}
	c.missionStarted(b, ExecLead)
	st.Brief = brief.Brief
	return st, nil
}

// missionPrepare reads the task a spawn is for and checks it may be worked: of a running
// session, Ready (or a claim gone stale), not an epic, not blocked, every dependency done,
// no live worker on it. It builds the brief. name is the name the claim will be held under
// ("" for a worker's own).
func (c *Conductor) missionPrepare(ctx context.Context, req SpawnRequest, parent *worker, names ...string) (*missionBind, SpawnRequest, error) {
	mc, err := c.missions()
	if err != nil {
		return nil, req, err
	}
	if parent != nil {
		return nil, req, fmt.Errorf("a sub-worker cannot take a mission task: only the lead hands tasks out")
	}
	t, _, err := mc.Store.Get(ctx, mc.ProfileID, req.TaskID)
	if err != nil {
		return nil, req, err
	}
	if t.SessionID == 0 {
		return nil, req, fmt.Errorf("task #%d is on the personal board, not in a mission", t.ID)
	}
	// org_spawn also accepts task_id, so enforce the conversation binding here,
	// below every tool route, before a task can be claimed or a worker started.
	if c.cfg.Mission != nil {
		if _, err := c.sessionOf(ctx, mc, t.SessionID); err != nil {
			return nil, req, err
		}
	}

	own, err := mc.Store.SessionTask(ctx, mc.ProfileID, t.SessionID, t.ID, missionActor())
	if err != nil {
		return nil, req, err
	}
	t = own.Task // with its dependencies
	se, err := mc.Store.GetSession(ctx, mc.ProfileID, t.SessionID, missionActor())
	if err != nil {
		return nil, req, err
	}
	if se.State != tasks.SessionRunning || se.Archived {
		return nil, req, fmt.Errorf("session #%d is %s: its tasks are not started", se.ID, se.State)
	}
	if t.Kind == tasks.KindEpic || t.Blocked {
		return nil, req, fmt.Errorf("task #%d is an epic or is blocked: it is not started", t.ID)
	}
	if t.Status != tasks.StatusReady && (t.Status != tasks.StatusInProgress || t.Claim == nil || !t.Claim.Stale) {
		return nil, req, fmt.Errorf("task #%d is %s: only a Ready task is started", t.ID, t.Status)
	}
	if c.taskWorker(t.ID) != nil {
		return nil, req, fmt.Errorf("task #%d already has a worker running", t.ID)
	}
	var deps []tasks.SessionTaskDetail
	for _, id := range t.DependsOn {
		d, err := mc.Store.SessionTask(ctx, mc.ProfileID, se.ID, id, missionActor())
		if err != nil {
			return nil, req, err
		}
		if !tasks.DepSettled(d.Task) {
			return nil, req, fmt.Errorf("task #%d waits for #%d, which is %s", t.ID, id, d.Task.Status)
		}
		deps = append(deps, d)
	}
	name := missionWorkerName(se.ID, t.ID)
	actor := tasks.Actor{Kind: tasks.Agent, Name: name, Session: se.ID, Task: t.ID}
	if len(names) > 0 && names[0] != "" {
		name = names[0]
		actor = tasks.Actor{Kind: tasks.Agent, Name: name, Session: se.ID, Task: t.ID}
	}
	exec := req.executor
	if exec == "" {
		exec = ExecWorker
		if req.Access == ProfileResearch {
			exec = ExecSubagent
		}
	}
	if exec == ExecWorker && req.NeedsWrite == nil {
		write := true // a task worker carries the task out: it edits, whatever words its brief has
		req.NeedsWrite = &write
	}
	if why := c.startRefusal(ctx, mc, se, t, own.Events, exec, req.Access); why != "" {
		return nil, req, fmt.Errorf("task #%d does not start now: %s", t.ID, why)
	}
	req.Brief = buildTaskBrief(se, t, deps, name, req.Brief)
	if len(req.Files) == 0 {
		req.Files = TaskFiles(t.Notes)
	}
	return &missionBind{session: se.ID, task: t, name: name, actor: actor, executor: exec}, req, nil
}

// missionClaim takes the task for the bind's name; the claim is a lease the watcher renews.
func (c *Conductor) missionClaim(ctx context.Context, b *missionBind) error {
	mc, _ := c.missions()
	lease := mc.lease()
	if b.name == missionLeadName {
		lease = tasks.DefaultLease
	}
	c.msched.mu.Lock()
	c.msched.init()
	c.msched.pending[b.task.ID] = true
	c.msched.mu.Unlock()
	if _, err := mc.Store.SessionClaim(ctx, mc.ProfileID, b.session, b.task.ID, b.actor, lease); err != nil {
		c.msched.mu.Lock()
		delete(c.msched.pending, b.task.ID)
		c.msched.mu.Unlock()
		return err
	}
	b.renewedAt = c.cfg.Now()
	c.msched.mu.Lock()
	c.msched.owned[b.task.ID] = ownedClaim{name: b.name, session: b.session}
	c.msched.mu.Unlock()
	c.missionWatch(b.session)
	return nil
}

// missionUnclaim gives the claim back when the worker could not be registered.
func (c *Conductor) missionUnclaim(b *missionBind, why string) {
	if b == nil {
		return
	}
	c.releaseClaim(b.session, b.task.ID, b.name, why)
	c.msched.mu.Lock()
	delete(c.msched.pending, b.task.ID)
	c.msched.mu.Unlock()
}

// missionStarted records who carries the task out, once the executor exists.
func (c *Conductor) missionStarted(b *missionBind, label string) {
	mc, _ := c.missions()
	c.msched.mu.Lock()
	c.msched.init()
	delete(c.msched.pending, b.task.ID)
	if label != missionLeadName {
		c.msched.starts[b.task.ID]++
	}
	c.msched.mu.Unlock()
	_, _ = mc.Store.SetAssignee(context.WithoutCancel(c.ctx), mc.ProfileID, b.session, b.task.ID, label, b.actor)
}

// releaseClaim gives a task held under name back to Ready (the retry count is untouched).
func (c *Conductor) releaseClaim(sid, taskID int64, name, why string) {
	c.release(sid, taskID, name, nil, why)
}

// releaseIfHeld is releaseClaim for a claim read earlier by the reconcile: it gives the task
// back only while that very claim is held (same name, same lease end), atomically in the
// store, so a claim a restarted turn took again under the same name is left alone.
func (c *Conductor) releaseIfHeld(sid, taskID int64, held tasks.Claim, why string) {
	c.release(sid, taskID, held.By, &held, why)
}

func (c *Conductor) release(sid, taskID int64, name string, held *tasks.Claim, why string) {
	mc, err := c.missions()
	if err != nil {
		return
	}
	actor := tasks.Actor{Kind: tasks.Agent, Name: name, Session: sid, Task: taskID}
	ctx, cancel := context.WithTimeout(context.WithoutCancel(c.ctx), 10*time.Second)
	defer cancel()
	done := false
	if held != nil {
		done, _ = mc.Store.SessionReleaseIfHeld(ctx, mc.ProfileID, sid, taskID, *held, why, actor)
	} else {
		_, err := mc.Store.SessionRelease(ctx, mc.ProfileID, sid, taskID, why, actor)
		done = err == nil
	}
	if done {
		c.msched.mu.Lock()
		delete(c.msched.owned, taskID)
		c.msched.mu.Unlock()
	}
}

// missionEnv binds a task worker's process to its session and task: the task commands it
// runs then act in that session on that one task, as that name. The binding is a convention
// of the environment, not a sandbox: a shell-capable worker can unset the variables and act
// as an unbound agent. What the store refuses by role, and the checks the Conductor makes
// afterwards (a verdict by an unexpected actor is not trusted), do not rely on it.
func missionEnv(w *worker, env map[string]string) {
	if w.task == nil {
		return
	}
	env["MONOAGENT_TASK_SESSION"] = fmt.Sprint(w.task.session)
	env["MONOAGENT_TASK_ID"] = fmt.Sprint(w.task.task.ID)
	env["MONOAGENT_ACTOR"] = w.task.name
	if w.task.verifier {
		env["MONOAGENT_TASK_ROLE"] = "verifier" // verify, comment and attach evidence to this task only
	}
}

// missionRule is added to a task worker's system prompt.
func missionRule(w *worker) string {
	if w.task == nil {
		return ""
	}
	if w.task.verifier {
		return fmt.Sprintf("\n## Your task\n\nYou are verifying task #%d of a mission: you check it against its acceptance criteria and record one verdict. "+
			"You read and run checks; you change nothing, and you do not claim, finish or release any task.\n", w.task.task.ID)
	}
	return fmt.Sprintf("\n## Your task\n\nYou are working on task #%d of a mission. The brief is the task, how it is judged and what earlier tasks produced; "+
		"your last message is your report and is attached to the task. Do nothing outside the task.\n", w.task.task.ID)
}

// missionFinish takes a task worker's run back into the task. A worker that finished has
// its report attached as an artifact and the task goes to Review with it. One that failed,
// timed out or was stopped leaves the claim: a comment says what happened, and the
// scheduler gives the task back (its retry count is untouched). What the run cost goes on
// the session.
func (c *Conductor) missionFinish(w *worker, outcome, report, errText string) {
	b := w.task
	if b == nil {
		return
	}
	mc, err := c.missions()
	if err != nil {
		return
	}
	ctx, cancel := context.WithTimeout(context.WithoutCancel(c.ctx), 15*time.Second)
	defer cancel()
	c.recordSpend(ctx, mc, w)
	if b.verifier {
		c.removeVerifierTree(w)
		c.verifierFinished(ctx, mc, w, outcome, errText)
		return
	}
	c.msched.mu.Lock()
	stopped, timedOut := b.stopped, b.timedOut
	c.msched.mu.Unlock()
	cur, _, err := mc.Store.Get(ctx, mc.ProfileID, b.task.ID)
	if err != nil {
		return
	}
	held := cur.Status == tasks.StatusInProgress && cur.Claim != nil && cur.Claim.By == b.name
	switch {
	case outcome == chatevents.AgentDone:
		c.handBackReport(ctx, mc, w, cur, held, report)
	case stopped != "":
		// the session ended it: the session already gave the claim back
	case held:
		c.recordFailure(ctx, mc, w, outcome, errText, timedOut)
	}
}

// handBackReport attaches the report and, when the worker did not finish the task itself,
// sends it to Review with the report as the result.
func (c *Conductor) handBackReport(ctx context.Context, mc *MissionRun, w *worker, cur tasks.Task, held bool, report string) {
	b := w.task
	if !held && cur.Status != tasks.StatusReview {
		return
	}
	text := strings.TrimSpace(report)
	if text == "" {
		text = "(the worker gave no report)"
	}
	if _, err := mc.Store.AddArtifact(ctx, mc.ProfileID, b.session, b.task.ID, tasks.ArtifactInput{Kind: "report", Ref: "report:" + w.id, Note: text}, b.actor); err != nil {
		c.notice("mission.artifact", fmt.Sprintf("Task #%d: the report of %s could not be attached: %v", b.task.ID, w.id, err), chatevents.SeverityWarning)
	}
	if !held {
		c.recordBranch(ctx, mc, w)
		return
	}
	if _, err := mc.Store.SessionFinish(ctx, mc.ProfileID, b.session, b.task.ID, tasks.Outcome{Result: text}, b.actor); err != nil {
		if errors.Is(err, tasks.ErrNoEvidence) {
			// the evidence rule: a code task goes to review with a commit, diff or test output.
			// Give the task back with the reason so the next worker (or the lead) attaches it.
			_, _ = mc.Store.SessionComment(ctx, mc.ProfileID, b.session, b.task.ID, "worker "+w.id+" finished without evidence: attach a commit, diff or test output (task artifact) before finishing", b.actor)
			c.msched.mu.Lock()
			c.msched.failures[b.task.ID]++
			c.msched.mu.Unlock()
			c.releaseClaim(b.session, b.task.ID, b.name, "finished without evidence")
			c.notice("mission.evidence", fmt.Sprintf("Task #%d: %s finished a code task without evidence (a commit, diff or test output): the task is given back.", b.task.ID, w.id), chatevents.SeverityWarning)
			return
		}
		c.notice("mission.finish", fmt.Sprintf("Task #%d: %s could not hand it to review: %v", b.task.ID, w.id, err), chatevents.SeverityWarning)
		return
	}
	c.recordBranch(ctx, mc, w)
	c.msched.mu.Lock()
	delete(c.msched.owned, b.task.ID)
	delete(c.msched.failures, b.task.ID)
	c.msched.mu.Unlock()
}

// recordFailure writes why a run ended without a result as a comment on the task.
func (c *Conductor) recordFailure(ctx context.Context, mc *MissionRun, w *worker, outcome, errText string, timedOut bool) {
	b := w.task
	what := fmt.Sprintf("worker %s %s", w.id, outcome)
	switch {
	case timedOut:
		what = fmt.Sprintf("worker %s timed out after %s", w.id, mc.timeout())
	case outcome == chatevents.AgentCancelled:
		what = fmt.Sprintf("worker %s was stopped", w.id)
	}
	if e := strings.TrimSpace(errText); e != "" {
		what += ": " + boundText(e, 600)
	}
	_, _ = mc.Store.SessionComment(ctx, mc.ProfileID, b.session, b.task.ID, what, b.actor)
	if outcome != chatevents.AgentCancelled || timedOut {
		c.msched.mu.Lock()
		c.msched.failures[b.task.ID]++
		c.msched.mu.Unlock()
	}
}

// liveSpend is called on every event of a running mission worker: it notes what the exec
// has cost so far and adds the growth to the session, so a long worker cannot overrun the
// session's budget between finishes. The 90 percent warning and the 100 percent halt are
// applied at once.
func (c *Conductor) liveSpend(w *worker, m Model, run *monomind.TurnResult) {
	cost, _, ok := liveCost(m, run)
	if !ok {
		return
	}
	c.mu.Lock()
	grew := cost > w.liveCost
	if grew {
		w.liveCost = cost
	}
	c.mu.Unlock()
	if grew {
		c.chargeSession(w)
	}
}

// chargeSession records the worker's unrecorded cost and applies the session's budget.
func (c *Conductor) chargeSession(w *worker) {
	mc, err := c.missions()
	if err != nil || w.task == nil {
		return
	}
	ctx, cancel := context.WithTimeout(context.WithoutCancel(c.ctx), 10*time.Second)
	defer cancel()
	if !c.recordSpend(ctx, mc, w) {
		return
	}
	if se, err := mc.Store.GetSession(ctx, mc.ProfileID, w.task.session, missionActor()); err == nil && se.State == tasks.SessionRunning {
		c.missionBudget(ctx, se)
	}
}

// recordSpend adds what the worker cost since the last time to the session, and says
// whether it added anything. Only this process records spend: a worker could otherwise hide
// its cost.
func (c *Conductor) recordSpend(ctx context.Context, mc *MissionRun, w *worker) bool {
	// Hold only this worker's accounting lock across persistence. Advancing
	// spent before the write commits would lose a failed charge permanently;
	// dropping the lock around the write would double-charge concurrent reports.
	w.task.spendMu.Lock()
	c.mu.Lock()
	cost := w.cost + w.liveCost
	c.mu.Unlock()
	delta := cost - w.task.spent
	if delta <= 0 {
		w.task.spendMu.Unlock()
		return false
	}
	note := fmt.Sprintf("%s on task #%d", w.id, w.task.task.ID)
	if _, err := mc.Store.AddSessionSpend(ctx, mc.ProfileID, w.task.session, delta, note, operatorActor()); err != nil {
		w.task.spendMu.Unlock()
		c.notice("mission.spend", fmt.Sprintf("The cost of %s ($%.4f) could not be added to the session: %v", w.id, delta, err), chatevents.SeverityWarning)
		return false
	}
	w.task.spent = cost
	w.task.spendMu.Unlock()
	return true
}

// missionClose gives back what this Conductor still holds: its turn is over.
func (c *Conductor) missionClose() {
	c.msched.mu.Lock()
	held := map[int64]ownedClaim{}
	for id, oc := range c.msched.owned {
		held[id] = oc
	}
	c.msched.mu.Unlock()
	for id, oc := range held {
		c.releaseClaim(oc.session, id, oc.name, "the turn that held this task ended")
	}
}
