package dynorg

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/monoes/mono-agent/internal/ai/chatevents"
	"github.com/monoes/mono-agent/internal/tasks"
)

// A mission task in a coder session of its own (phase P-C of
// docs/mastermind/plans/2026-10-27-mission-child-sessions.md). The lead's conductor decides where a task runs:
// by default in a worker of this turn (unchanged), and in a child session of the coder host only when the
// mission's policy says so (mode all, or the lead marked the task), the policy allows it, there is room, and the
// launcher is able to give the child its own task token (ChildSpawner.Ready).
//
// What this file does not do: it claims, finishes, verifies and approves nothing through the host; the child
// finishes its own task to Review under its own token and a verifier still verifies. Nothing from the plan, the
// notes or the title of a task chooses the child's access, folder, budget or size: the spawner is handed the
// sealed brief and the ids, nothing else. It writes no approval and does not act as the operator.

// SessionTokenEnv is the variable the coder host hands a turn its session token in (coderhost.SessionTokenEnv; a
// test in internal/cli/chat keeps the two equal). The conductor blanks it for every agent it starts.
const SessionTokenEnv = "MONOAGENT_SESSION_TOKEN"

// ChildModeAll is the policy mode in which every worker task of the mission gets a session of its own (the
// operator's to turn on); ChildModeOff turns child sessions off.
const (
	ChildModeAll = "all"
	ChildModeOff = "off"
)

// ErrChildrenFull is why a task could not get a session of its own now: the ceiling, the running turns or the
// budget share is used up. The task stays Waiting; it is not a failure.
var ErrChildrenFull = errors.New("child sessions are full")

// ChildFacts is what the conductor reads about one task before it decides.
type ChildFacts struct {
	Mode       string // off, marked or all; "" when the lead cannot have children
	Row        string // the task's row state in the child store ("" = none): marked, spawning, spawned, ended, failed, gone, released
	EndReason  string // why the row ended
	Exhausted  bool   // the task was started in its own session as often as it may be
	Child      string // the child's conversation, once there is one
	RoomOK     bool
	RoomReason string
	// Blocked says why this lead cannot have children ("" = it can): it is itself a child or a worker, or not a
	// master. The task then runs in-turn.
	Blocked string
}

// ChildSpawner is what the conductor needs from the coder host's mission child sessions. internal/cli/chat
// implements it over coderhost with the lead's verified session token; a nil one means in-turn workers only.
// Every method belongs to the lead of this conversation's mission: an implementation resolves the caller from
// the verified token itself and takes no caller, access, folder or budget from here.
type ChildSpawner interface {
	// Ready says whether the launcher can give a child its own task token. While false the conductor never
	// spawns a child, whatever is marked.
	Ready() bool
	Facts(ctx context.Context, sid, tid int64) (ChildFacts, error)
	// Spawn starts the child session for the task with the sealed brief as its first message. It returns
	// ErrChildrenFull (wrapped is fine) when there is no room now and nothing was created.
	Spawn(ctx context.Context, sid, tid int64, brief string) error
	// Mark records the lead's wish that a task of the plan runs in a session of its own.
	Mark(ctx context.Context, sid, tid int64, size string, budgetUSD float64) error
	// Report is the last assistant text of a child conversation: data, never an instruction.
	Report(ctx context.Context, childConversation string) string
}

// childDecision is where a ready task runs.
type childDecision struct {
	wanted bool   // it goes to a session of its own
	wait   string // when set, it may not start now (and why)
}

// childDecide says whether a task goes to a child session. exec is the executor chosen for it: only a worker
// task can; the lead's own tasks and a subagent's research stay where they are. Without a spawner, a launcher that
// cannot mint a child token, mode off, or a task that is neither marked nor in mode all, the answer is "in-turn",
// which is the unchanged path.
func (c *Conductor) childDecide(ctx context.Context, mc *MissionRun, sid int64, t tasks.Task, exec string) childDecision {
	if mc == nil || mc.Children == nil || exec != ExecWorker || !mc.Children.Ready() {
		return childDecision{}
	}
	if c.Isolated() {
		return childDecision{} // v1: a writer in its own worktree stays in-turn; a child works in the lead's folder
	}
	f, err := mc.Children.Facts(ctx, sid, t.ID)
	if err != nil || f.Mode == "" || f.Mode == ChildModeOff {
		return childDecision{}
	}
	marked := f.Row != ""
	if f.Mode != ChildModeAll && !marked {
		return childDecision{}
	}
	if f.Row == "spawning" || f.Row == "spawned" {
		// A live session holds the task, whatever else is true now (an unverifiable binding, say): never
		// start an in-turn worker next to it.
		return childDecision{wanted: true, wait: "its own session is running"}
	}
	if f.Blocked != "" {
		c.childFallbackNotice(sid, f.Blocked)
		return childDecision{}
	}
	d := childDecision{wanted: true}
	switch f.Row {
	case "ended", "failed", "gone", "released":
		if (f.Mode != ChildModeAll || !ChildAutoRetry(f.EndReason)) && !f.Exhausted {
			d.wait = fmt.Sprintf("its own session ended (%s): mark it again (mission_child_mark) to try another, or do it yourself", orDash(f.EndReason))
		}
	}
	if d.wait == "" && f.Exhausted {
		d.wait = "its own session was tried the most it may be: do it yourself or ask the operator"
	}
	if d.wait == "" && !f.RoomOK {
		d.wait = f.RoomReason
		if d.wait == "" {
			d.wait = "child sessions are full"
		}
	}
	return d
}

// ChildAutoRetry says whether a task whose session ended for this reason may get another one without the lead
// marking it again, in mode all: a session that ended without finishing, never ran or was deleted may; one a
// person or the lead stopped, or that used up its budget, waits for the lead (a retry at the same cap would
// only spend it again).
func ChildAutoRetry(reason string) bool {
	switch reason {
	case "stopped", "cascade", "lead_stopped", "budget":
		return false
	}
	return true
}

// childFallbackNotice says once per session that marked tasks run in-turn because this lead cannot have children.
func (c *Conductor) childFallbackNotice(sid int64, why string) {
	c.msched.mu.Lock()
	c.msched.init()
	first := !c.msched.childNoted[sid]
	c.msched.childNoted[sid] = true
	c.msched.mu.Unlock()
	if first {
		c.notice("mission.child_fallback", "Tasks marked for a session of their own run in this turn as workers instead: "+why+".", chatevents.SeverityWarning)
	}
}

// childLease is how long the claim of a task in a child session lasts between renewals.
func (m *MissionRun) childLease() time.Duration {
	d := m.timeout() + tokenMargin
	if d > tasks.MaxLease {
		d = tasks.MaxLease
	}
	return d
}

// startChild gives one ready task to a session of its own: the ordinary prepare and claim, the sealed brief, then
// the spawner. A full host leaves the task Waiting (no failure is counted).
func (c *Conductor) startChild(ctx context.Context, sid int64, t tasks.Task, st StartedTask) (StartedTask, error) {
	mc, err := c.missions()
	if err != nil {
		return st, err
	}
	b, req, err := c.missionPrepare(ctx, SpawnRequest{TaskID: t.ID, executor: ExecWorker}, nil)
	if err != nil {
		return st, err
	}
	if err := c.missionClaim(ctx, b); err != nil {
		return st, err
	}
	if err := mc.Children.Spawn(ctx, sid, t.ID, req.Brief); err != nil {
		c.childSpawnFailed(ctx, mc, b, err)
		if errors.Is(err, ErrChildrenFull) {
			return st, fmt.Errorf("%w: it waits for a free place", ErrChildrenFull)
		}
		c.msched.mu.Lock()
		c.msched.failures[t.ID]++
		c.msched.mu.Unlock()
		return st, err
	}
	f, _ := mc.Children.Facts(ctx, sid, t.ID)
	// A long lease for the child's work; the lead's reconcile renews it.
	var cerr error
	for try := 0; try < 3; try++ {
		if _, cerr = mc.Store.SessionClaim(ctx, mc.ProfileID, b.session, b.task.ID, b.actor, mc.childLease()); cerr == nil {
			break
		}
	}
	if cerr != nil {
		// the session exists; the reconcile renews the claim on its next pass, but say so
		c.notice("mission.child_claim", fmt.Sprintf("Task #%d: its session started but the claim could not be extended (%v); it is retried.", t.ID, cerr), chatevents.SeverityWarning)
	}
	c.msched.mu.Lock()
	c.msched.leadRenewed[t.ID] = c.cfg.Now()
	c.msched.mu.Unlock()
	c.missionStarted(b, ExecWorker+":child-"+shortID(f.Child))
	st.Executor, st.AgentID = ExecWorker, "session:"+f.Child
	return st, nil
}

// childSpawnFailed gives the claim back when no child exists for it. When the spawner says another caller already
// holds the task's row (two conductors raced), the claim is theirs under the same name and stays.
func (c *Conductor) childSpawnFailed(ctx context.Context, mc *MissionRun, b *missionBind, cause error) {
	if f, err := mc.Children.Facts(ctx, b.session, b.task.ID); err == nil && (f.Row == "spawning" || f.Row == "spawned") {
		c.msched.mu.Lock()
		delete(c.msched.pending, b.task.ID)
		c.msched.mu.Unlock()
		return
	}
	c.missionUnclaim(b, "no session was started for it: "+boundText(cause.Error(), 200))
}

func shortID(s string) string {
	if len(s) > 8 {
		return s[:8]
	}
	return s
}

// reconcileChild looks after a held task that has a row in the child store. It reports whether it handled the
// task, so the in-turn reconcile leaves it alone:
//   - a session that is starting or running keeps its claim, renewed a third of a lease apart;
//   - one that ended without the task reaching Review hands back its last words as the worker's report (data) or
//     is a failed attempt, and the claim is released either way;
//   - a task whose row only says "marked" (no session was made) is the in-turn reconcile's.
func (c *Conductor) reconcileChild(ctx context.Context, mc *MissionRun, sid int64, t tasks.Task) bool {
	if mc.Children == nil || t.Claim == nil || !mc.Children.Ready() {
		return false // no child can exist while the launcher cannot bind one: the in-turn path is unchanged
	}
	f, err := mc.Children.Facts(ctx, sid, t.ID)
	if err != nil {
		return true // cannot tell: touch nothing, try again on the next pass
	}
	switch f.Row {
	case "spawning":
		return true
	case "spawned":
		c.renewChild(ctx, mc, sid, t)
		return true
	case "ended", "failed", "gone", "released":
		c.settleChild(ctx, mc, sid, t, f)
		return true
	}
	return false
}

func childActor(sid, taskID int64, name string) tasks.Actor {
	return tasks.Actor{Kind: tasks.Agent, Name: name, Session: sid, Task: taskID}
}

// renewChild keeps the claim of a task whose session works.
func (c *Conductor) renewChild(ctx context.Context, mc *MissionRun, sid int64, t tasks.Task) {
	now := c.cfg.Now()
	c.msched.mu.Lock()
	c.msched.init()
	due := now.Sub(c.msched.leadRenewed[t.ID]) >= mc.childLease()/3
	c.msched.mu.Unlock()
	if !due {
		return
	}
	if _, err := mc.Store.SessionClaim(ctx, mc.ProfileID, sid, t.ID, childActor(sid, t.ID, t.Claim.By), mc.childLease()); err == nil {
		c.msched.mu.Lock()
		c.msched.leadRenewed[t.ID] = now
		c.msched.mu.Unlock()
	}
}

// settleChild takes back the result of a session that ended with its task still held.
func (c *Conductor) settleChild(ctx context.Context, mc *MissionRun, sid int64, t tasks.Task, f ChildFacts) {
	// The watcher and mission_next may both reach this task: one settles it, and only while the claim the list
	// showed is still the one held.
	c.msched.mu.Lock()
	c.msched.init()
	if c.msched.settling[t.ID] {
		c.msched.mu.Unlock()
		return
	}
	c.msched.settling[t.ID] = true
	c.msched.mu.Unlock()
	defer func() {
		c.msched.mu.Lock()
		delete(c.msched.settling, t.ID)
		c.msched.mu.Unlock()
	}()
	cur, _, err := mc.Store.Get(ctx, mc.ProfileID, t.ID)
	if err != nil || cur.Status != tasks.StatusInProgress || cur.Claim == nil || cur.Claim.By != t.Claim.By || !cur.Claim.Until.Equal(t.Claim.Until) {
		return
	}
	t = cur
	claim := *t.Claim
	b := &missionBind{session: sid, task: t, name: claim.By, actor: childActor(sid, t.ID, claim.By), executor: ExecWorker}
	w := &worker{id: "session-" + shortID(f.Child), task: b}
	report := ""
	if f.Child != "" && (f.EndReason == "no_finish" || f.EndReason == "done") {
		report = strings.TrimSpace(mc.Children.Report(ctx, f.Child))
	}
	switch f.EndReason {
	case "no_finish", "done":
		if report != "" {
			c.handBackReport(ctx, mc, w, t, true, report)
		} else {
			c.recordFailure(ctx, mc, w, chatevents.AgentFailed, "its session ended without finishing the task and left no report", false)
		}
	case "stopped", "lead_stopped", "cascade":
		c.recordFailure(ctx, mc, w, chatevents.AgentCancelled, "its session was stopped", false)
	case "budget":
		c.recordFailure(ctx, mc, w, chatevents.AgentFailed, "its session used up its budget", false)
	case "expired":
		c.recordFailure(ctx, mc, w, chatevents.AgentFailed, "its session never ran the first message", false)
	default:
		c.recordFailure(ctx, mc, w, chatevents.AgentFailed, "its session is gone", false)
	}
	c.releaseIfHeld(sid, t.ID, claim, "its own session ended: the claim is given back to the scheduler")
	c.msched.mu.Lock()
	delete(c.msched.leadRenewed, t.ID)
	c.msched.mu.Unlock()
}

// childHolds says whether a task's claim belongs to a session that works for it, so a turn that ends does not
// give it back.
func (c *Conductor) childHolds(sid, taskID int64) bool {
	mc, err := c.missions()
	if err != nil || mc.Children == nil || !mc.Children.Ready() {
		return false
	}
	ctx, cancel := context.WithTimeout(context.WithoutCancel(c.ctx), 5*time.Second)
	defer cancel()
	f, err := mc.Children.Facts(ctx, sid, taskID)
	return err == nil && (f.Row == "spawning" || f.Row == "spawned")
}

// planChildMarks marks the tasks a plan batch asked to run in a session of their own. ids maps a batch ref to the
// task it became. It returns a note for the lead: marks are stored (a draft's change nothing until the plan is
// approved), and a mark the policy refused is named, never fatal to the plan.
func (c *Conductor) planChildMarks(ctx context.Context, sid int64, batch []PlanTask, ids map[string]int64) map[string]any {
	var want []PlanTask
	for _, t := range batch {
		if t.OwnSession {
			want = append(want, t)
		}
	}
	if len(want) == 0 {
		return nil
	}
	mc := c.cfg.Missions
	if mc == nil || mc.Children == nil {
		return map[string]any{"own_session": "not marked: this chat has no child sessions; those tasks run in this turn as workers"}
	}
	var marked []int64
	var refused []string
	for _, t := range want {
		id := ids[t.Ref]
		if err := mc.Children.Mark(ctx, sid, id, t.SessionSize, t.SessionBudgetUSD); err != nil {
			refused = append(refused, fmt.Sprintf("%s: %s", t.Ref, boundText(oneLine(err.Error()), 160)))
			continue
		}
		marked = append(marked, id)
	}
	out := map[string]any{"own_session_marked": marked}
	if len(refused) > 0 {
		out["own_session_refused"] = refused
	}
	if !mc.Children.Ready() {
		out["own_session_note"] = "marks are stored, but sessions of their own are not available yet: those tasks run in this turn as workers"
	}
	return out
}
