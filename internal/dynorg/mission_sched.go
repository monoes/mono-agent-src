package dynorg

import (
	"context"
	"fmt"
	"strings"
	"sync"
	"time"

	"github.com/monoes/mono-agent/internal/ai/chatevents"
	"github.com/monoes/mono-agent/internal/tasks"
)

// The mission scheduler (docs/mastermind/plans/2026-10-25-coder-missions-task-sessions.md,
// 8.3). It is deterministic and has no model in it: the lead decides the plan, the scheduler
// decides what starts, when. The session in the task store is the only state that matters;
// everything here is rebuilt from it on the next call, so a crashed or compacted lead (or a
// new Conductor) loses nothing.

// Executors a task can be carried out by.
const (
	ExecLead     = "lead"     // the lead does it itself; the tool hands it the brief
	ExecWorker   = "worker"   // a dynamic-org worker (the default)
	ExecSubagent = "subagent" // read only: research and checking
)

// MissionConfig connects a Conductor to a profile's task store. A nil one leaves the
// mission tools and org_spawn's task_id answering that no mission is set up.
type MissionRun struct {
	Store     *tasks.Store
	ProfileID string
	// Session is the session the mission tools work when a call names none.
	Session int64
	// Poll is how often the watcher looks at the session: its state, its budget, the
	// workers' leases and timeouts (default 2s).
	Poll time.Duration
	// Lease is a worker's claim on its task, renewed while the worker lives (default 5
	// minutes): the time a crashed turn's claim takes to be given back.
	Lease time.Duration
	// TaskTimeout bounds one worker run on one task (default 60 minutes).
	TaskTimeout time.Duration
	// VerifierTimeout bounds one verifier run (default 20 minutes): a verifier that is
	// still going past it (alive, no verdict) is stopped and counts as one run that ended
	// without a verdict.
	VerifierTimeout time.Duration
	// MaxAttempts is how many times a task's worker may crash or time out in this turn
	// before the scheduler leaves the task to the lead (default 3).
	MaxAttempts int
	// MaxVerifyAttempts is how many verifier runs of one task may end without a verdict
	// before the task is blocked for the operator (default 2).
	MaxVerifyAttempts int
	// MaxSpawnsPerTask is how many workers one task may get in a Conductor's turn
	// (default 6), whatever the failure counter says.
	MaxSpawnsPerTask int
}

func (m *MissionRun) poll() time.Duration { return orDur(m.Poll, 2*time.Second) }
func (m *MissionRun) lease() time.Duration {
	return orDur(m.Lease, 5*time.Minute)
}
func (m *MissionRun) timeout() time.Duration { return orDur(m.TaskTimeout, time.Hour) }
func (m *MissionRun) verifierTimeout() time.Duration {
	return orDur(m.VerifierTimeout, 20*time.Minute)
}
func (m *MissionRun) attempts() int {
	if m.MaxAttempts > 0 {
		return m.MaxAttempts
	}
	return 3
}

func orDur(d, def time.Duration) time.Duration {
	if d > 0 {
		return d
	}
	return def
}

// Thresholds of the session budget.
const (
	budgetWarnAt = 0.9 // stop starting tasks and ask the operator
	budgetHaltAt = 1.0 // stop everything
)

// missionLeadName is the name the lead holds the tasks it does itself under.
const missionLeadName = "lead"

// missionConductorName acts for the scheduler in the session's history.
const missionConductorName = "conductor"

// missionWorkerName is the name a worker holds its task under: one per session and task, so
// a retry renews the claim of the run before it.
func missionWorkerName(session, task int64) string { return fmt.Sprintf("m%d-t%d", session, task) }

func isMissionName(name string, session int64) bool {
	return name == missionLeadName || strings.HasPrefix(name, fmt.Sprintf("m%d-t", session))
}

// missionBind is a worker's hold on its task.
type missionBind struct {
	session  int64
	task     tasks.Task
	name     string
	actor    tasks.Actor // what the worker's process acts as: bound to the session and the task
	executor string
	verifier bool // the worker verifies the task in Review; it holds no claim
	// verifyBranch is, for a verifier, the unmerged branch of the executor it checks
	// (mission_verify_tree.go).
	verifyBranch string

	// spendMu serializes durable charges from streaming, watcher and finish
	// callbacks. spent advances only after the corresponding write commits.
	spendMu sync.Mutex
	spent   float64

	// guarded by missionState.mu
	stopped      string    // why the mission ended the run ("" = it was not)
	timedOut     bool      // the run was cut at TaskTimeout (VerifierTimeout for a verifier)
	workingSince time.Time // when the watcher first saw it working
	renewedAt    time.Time
}

// missionState is a Conductor's view of its missions, guarded by mu.
// ownedClaim is a claim this Conductor made: the name it is held under and its session.
type ownedClaim struct {
	name    string
	session int64
}

type missionState struct {
	mu       sync.Mutex
	once     sync.Once
	sessions map[int64]bool       // sessions the watcher looks after
	owned    map[int64]ownedClaim // task -> the claim this Conductor holds
	pending  map[int64]bool       // tasks claimed whose worker is not registered yet
	failures map[int64]int        // task -> crashed or timed-out runs this turn
	starts   map[int64]int        // task -> workers started for it this turn
	// leadRenewed: task -> when the watcher last renewed the lead's claim on it
	leadRenewed map[int64]time.Time
	warned      map[int64]bool // sessions the 90 percent notice went out for
	halted      map[int64]bool // sessions the 100 percent halt went out for
}

func (m *missionState) init() {
	if m.sessions == nil {
		m.sessions, m.owned, m.pending = map[int64]bool{}, map[int64]ownedClaim{}, map[int64]bool{}
		m.failures, m.warned, m.halted = map[int64]int{}, map[int64]bool{}, map[int64]bool{}
		m.starts, m.leadRenewed = map[int64]int{}, map[int64]time.Time{}
	}
}

// missions returns the mission config or says there is none.
func (c *Conductor) missions() (*MissionRun, error) {
	if c.cfg.Missions == nil || c.cfg.Missions.Store == nil {
		return nil, fmt.Errorf("no mission is set up in this chat: there is no task session to work")
	}
	return c.cfg.Missions, nil
}

// missionActor is the scheduler's own name in the session's history.
func missionActor() tasks.Actor { return tasks.Actor{Kind: tasks.Agent, Name: missionConductorName} }

// sessionOf resolves the session a mission call works: the one named, else the config's.
//
// A chat's lead (Config.Mission set) works only the session of its own conversation:
// the newest open one, else the newest of any state (for the final report). Naming
// another session is refused, so a lead can neither read nor start another's tasks.
func (c *Conductor) sessionOf(ctx context.Context, mc *MissionRun, named int64) (int64, error) {
	if pm := c.cfg.Mission; pm != nil && pm.Store != nil {
		se, err := pm.active(ctx)
		if err == nil && se == nil {
			se, err = pm.latest(ctx)
		}
		if err != nil {
			return 0, err
		}
		if se == nil {
			return 0, fmt.Errorf("this conversation has no task session yet: plan the mission first (mission_plan)")
		}
		if named > 0 && named != se.ID {
			return 0, fmt.Errorf("session #%d is not this conversation's mission", named)
		}
		return se.ID, nil
	}
	switch {
	case named > 0:
		return named, nil
	case mc.Session > 0:
		return mc.Session, nil
	}
	return 0, fmt.Errorf("say which task session: pass session (its number)")
}

// StepResult is what mission_next reports.
type StepResult struct {
	Session SessionView   `json:"session"`
	Started []StartedTask `json:"started"`
	Waiting []WaitingTask `json:"waiting"`
	Running []RunningTask `json:"running"`
	Open    int           `json:"open_tasks"`
	Note    string        `json:"note,omitempty"`
}

// SessionView is the part of a session the mission tools report.
type SessionView struct {
	ID            int64   `json:"id"`
	Title         string  `json:"title"`
	State         string  `json:"state"`
	SpentUSD      float64 `json:"spent_usd"`
	BudgetUSD     float64 `json:"budget_usd"`
	BudgetPercent float64 `json:"budget_percent,omitempty"`
}

// StartedTask is a task the scheduler gave to an executor. For the lead's own it carries the
// brief the lead works from.
type StartedTask struct {
	TaskID   int64  `json:"task_id"`
	Title    string `json:"title"`
	Executor string `json:"executor"`
	AgentID  string `json:"agent_id,omitempty"`
	Brief    string `json:"brief,omitempty"`
}

// WaitingTask is a ready task that did not start, and why.
type WaitingTask struct {
	TaskID int64  `json:"task_id"`
	Title  string `json:"title"`
	Reason string `json:"reason"`
}

// RunningTask is a task a worker is on.
type RunningTask struct {
	TaskID  int64  `json:"task_id"`
	AgentID string `json:"agent_id"`
	Status  string `json:"status"`
	Role    string `json:"role,omitempty"` // "worker" or "verifier"
}

func sessionView(se tasks.Session) SessionView {
	v := SessionView{ID: se.ID, Title: se.Title, State: string(se.State), SpentUSD: se.SpentUSD, BudgetUSD: se.BudgetUSD}
	if se.BudgetUSD > 0 {
		v.BudgetPercent = 100 * se.SpentUSD / se.BudgetUSD
	}
	return v
}

// notice journals a nonfatal condition of the mission for the user to see.
func (c *Conductor) notice(code, msg string, sev chatevents.NoticeSeverity) {
	if c.cfg.Emit != nil {
		c.cfg.Emit.Emit(chatevents.EventNotice, chatevents.NoticePayload{Code: code, Message: msg, Severity: sev})
	}
}

// taskWorker is the worker of a task that is still running, or nil.
func (c *Conductor) taskWorker(taskID int64) *worker {
	c.mu.Lock()
	defer c.mu.Unlock()
	for _, id := range c.order {
		if w := c.workers[id]; w != nil && w.task != nil && w.task.task.ID == taskID && running(w.status) {
			return w
		}
	}
	return nil
}

// runningWorkers counts the workers that run or wait to run: they hold or want a slot.
func (c *Conductor) runningWorkers() int {
	c.mu.Lock()
	defer c.mu.Unlock()
	n := 0
	for _, w := range c.workers {
		if running(w.status) {
			n++
		}
	}
	return n
}

// missionStep is one pass of the scheduler over a session: it gives back claims whose
// worker is gone, checks the session and its budget, and (with start) gives every ready
// task that may start to its executor. Without start it only reports what would.
func (c *Conductor) missionStep(ctx context.Context, sid int64, start bool) (StepResult, error) {
	mc, err := c.missions()
	if err != nil {
		return StepResult{}, err
	}
	se, err := mc.Store.GetSession(ctx, mc.ProfileID, sid, missionActor())
	if err != nil {
		return StepResult{}, err
	}
	c.missionWatch(sid)
	c.missionReconcile(ctx, sid)
	res := StepResult{Session: sessionView(se), Started: []StartedTask{}, Waiting: []WaitingTask{}, Running: c.runningTasks(sid)}
	res.Open = se.Counts.Total - se.Counts.Done
	if se.State != tasks.SessionRunning || se.Archived {
		res.Note = fmt.Sprintf("session #%d is %s: nothing starts until the operator resumes it", sid, se.State)
		return res, nil
	}
	if note := c.missionBudget(ctx, se); note != "" {
		res.Note = note
		return res, nil
	}
	if notes := c.verifyRetries(ctx, mc, sid, start); len(notes) > 0 {
		res.Note = strings.Join(notes, "; ")
	}
	ready, err := mc.Store.SessionReady(ctx, mc.ProfileID, sid, missionActor())
	if err != nil {
		return res, err
	}
	writers, err := c.activeWriters(ctx, mc, sid)
	if err != nil {
		return res, err
	}
	for _, t := range ready {
		if w := c.taskWorker(t.ID); w != nil {
			// its worker lives (an ending verifier too); a lapsed lease does not make it free
			res.Waiting = append(res.Waiting, WaitingTask{TaskID: t.ID, Title: t.Title, Reason: fmt.Sprintf("worker %s is still running for it: wait for it (org_wait)", w.id)})
			continue
		}
		exec, access := c.chooseExecutor(ctx, t)
		why := c.cannotStart(mc, t, exec, access, writers)
		if why == "" && !start {
			why = "ready to start"
		}
		if why != "" {
			res.Waiting = append(res.Waiting, WaitingTask{TaskID: t.ID, Title: t.Title, Reason: why})
			continue
		}
		st, err := c.startTask(ctx, sid, t, exec, SpawnRequest{Access: accessFor(exec, access)})
		if err != nil {
			res.Waiting = append(res.Waiting, WaitingTask{TaskID: t.ID, Title: t.Title, Reason: "could not start: " + err.Error()})
			continue
		}
		res.Started = append(res.Started, st)
		if isWriter(exec, access) {
			writers = append(writers, writerOf(t))
		}
	}
	res.Running = c.runningTasks(sid)
	if len(res.Started) == 0 {
		if why := c.stallNote(ctx, mc, sid, res); why != "" {
			res.Note = joinNotes(res.Note, why)
		}
	}
	return res, nil
}

// accessFor is the access a started worker is given: a subagent reads only, a worker's is
// the Staffer's pick (empty: staffing decides).
func accessFor(exec, access string) string {
	if exec == ExecSubagent {
		return ProfileResearch
	}
	return access
}

// isWriter says whether an executor on that access edits files.
func isWriter(exec, access string) bool {
	switch exec {
	case ExecSubagent:
		return false
	case ExecLead:
		return true
	}
	return access == "" || writes(access)
}

// chooseExecutor decides who carries a task out: the plan's suggestion, else the Staffer
// (an empty suggested team means the task is small enough for the lead), else a worker.
func (c *Conductor) chooseExecutor(ctx context.Context, t tasks.Task) (exec, access string) {
	switch s := strings.ToLower(strings.TrimSpace(t.SuggestedExecutor)); s {
	case ExecLead, ExecWorker, ExecSubagent:
		return s, ""
	}
	if c.cfg.Staffer == nil {
		return ExecWorker, ""
	}
	team, err := c.cfg.Staffer.SuggestTeam(ctx, t.Title+"\n"+t.Notes+"\n"+t.Acceptance)
	if err != nil {
		return ExecWorker, ""
	}
	if len(team) == 0 {
		return ExecLead, ""
	}
	return ExecWorker, team[0].Access
}

// writerClaim is the files a running writer declared.
type writerClaim struct {
	task  int64
	files []string
}

func writerOf(t tasks.Task) writerClaim { return writerClaim{task: t.ID, files: TaskFiles(t.Notes)} }

// activeWriters are the tasks in progress that edit files, read from the store so a claim
// of another process counts too: every in-progress task but a read-only subagent's.
func (c *Conductor) activeWriters(ctx context.Context, mc *MissionRun, sid int64) ([]writerClaim, error) {
	ts, err := mc.Store.SessionTasks(ctx, mc.ProfileID, sid, tasks.Filter{Statuses: []tasks.Status{tasks.StatusInProgress}, Limit: 500}, missionActor())
	if err != nil {
		return nil, err
	}
	var out []writerClaim
	for _, t := range ts {
		if t.Claim != nil && !t.Claim.Stale && !strings.HasPrefix(t.Assignee, ExecSubagent+":") {
			out = append(out, writerOf(t))
		}
	}
	return out, nil
}

// cannotStart is why a ready task may not start now ("" = it may): the agent cap, the
// concurrency cap, a task that keeps failing, or a writer on the same files.
func (c *Conductor) cannotStart(mc *MissionRun, t tasks.Task, exec, access string, writers []writerClaim) string {
	if why := c.failureCap(mc, t); why != "" {
		return why
	}
	if exec != ExecLead {
		c.mu.Lock()
		spawned, maxAgents, retry := c.spawned, c.cfg.Limits.MaxAgents, c.taskRuns[t.ID] && c.missionTurn()
		c.mu.Unlock()
		if spawned >= maxAgents && !retry {
			return fmt.Sprintf("this turn already has its %d workers", maxAgents)
		}
		if n := c.runningWorkers(); n >= c.cfg.Limits.MaxConcurrent {
			return fmt.Sprintf("%d workers run already (the concurrency cap)", n)
		}
	}
	return writerOverlap(t, exec, access, writers)
}

// runningTasks lists the workers on a session's tasks.
func (c *Conductor) runningTasks(sid int64) []RunningTask {
	c.mu.Lock()
	defer c.mu.Unlock()
	out := []RunningTask{}
	for _, id := range c.order {
		if w := c.workers[id]; w != nil && w.task != nil && w.task.session == sid && running(w.status) {
			role := "worker"
			if w.task.verifier {
				role = "verifier"
			}
			out = append(out, RunningTask{TaskID: w.task.task.ID, AgentID: w.id, Status: w.status, Role: role})
		}
	}
	return out
}

// missionBudget applies the session's budget: at 90 percent nothing new starts and the
// operator is asked; at 100 percent everything stops. It returns the note for the lead, "" when
// the budget leaves room.
func (c *Conductor) missionBudget(ctx context.Context, se tasks.Session) string {
	if se.BudgetUSD <= 0 {
		return ""
	}
	frac := se.SpentUSD / se.BudgetUSD
	switch {
	case frac >= budgetHaltAt:
		c.missionHalt(ctx, se)
		return fmt.Sprintf("the session has spent its budget ($%.2f of $%.2f): the mission is halted until the operator raises it", se.SpentUSD, se.BudgetUSD)
	case frac >= budgetWarnAt:
		c.msched.mu.Lock()
		c.msched.init()
		first := !c.msched.warned[se.ID]
		c.msched.warned[se.ID] = true
		c.msched.mu.Unlock()
		if first {
			c.notice("mission.budget_90", fmt.Sprintf("Mission %q has spent %.0f%% of its budget ($%.2f of $%.2f). No new task starts; the operator decides whether to raise the budget.",
				se.Title, 100*frac, se.SpentUSD, se.BudgetUSD), chatevents.SeverityWarning)
		}
		return fmt.Sprintf("the session has spent %.0f%% of its budget: no new task starts; ask the operator to raise the budget (only they can)", 100*frac)
	}
	return ""
}
