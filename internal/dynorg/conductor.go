package dynorg

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/monoes/mono-agent/internal/ai/chatevents"
	"github.com/monoes/mono-agent/internal/monomind"
)

// MaxWait caps one org_wait (and a waiting org_spawn), so each call returns
// well inside the lead's tool timeout; the lead waits again for more.
const MaxWait = 100 * time.Second

// maxReport caps a worker's report journaled as agent.message.
const maxReport = 8000

// Config configures a turn's conductor.
type Config struct {
	Cwd        string
	Limits     Limits
	Staffer    *Staffer
	Base       monomind.ExecOptions // Bin, Settings, MaxTurns, Timeout, EffortFlag for every worker
	ReadAccess bool                 // monomind has --access read (agent-exec-access-read)
	Exec       ExecFunc
	Emit       Emitter
	Outcome    Outcome // nil = don't record
	Now        func() time.Time
	// Answers brings the user's answers to workers' questions (#256); nil
	// gives workers no ask_user tool.
	Answers AnswerSource
	// Remember keeps a worker for the conversation's later turns after
	// each of its runs (#230, veterans.go); nil keeps nothing.
	Remember func(Veteran)

	// Quality records worker results and the lead's ratings (#230); nil
	// records nothing.
	Quality *Quality

	// Writers is WritersIsolated to give each writer its own git worktree
	// (worktree.go); anything else keeps the write lease. TurnID names the
	// worktrees and branches.
	Writers string
	TurnID  string

	// Mission turns the lead into a mission planner (mission_plan.go); nil
	// leaves it as it was.
	Mission *MissionConfig
	// Missions connects the conductor to a profile's task store for mission
	// tasks (mission_sched.go); nil: org_spawn has no task_id.
	Missions *MissionRun
}

// Conductor runs one lead turn's workers.
type Conductor struct {
	cfg     Config
	ctx     context.Context
	cancel  context.CancelFunc
	slots   chan struct{}
	write   *lease
	browser *lease
	wg      sync.WaitGroup

	mu sync.Mutex
	// closeMu serialises mission_finish proposals (mission_close.go).
	closeMu sync.Mutex
	workers map[string]*worker
	order   []string
	spawned int
	// taskRuns: tasks that already had a worker this turn; a retry of one does not count
	// against Limits.MaxAgents in a mission turn (mission_cap.go).
	taskRuns map[int64]bool
	seq      int // the highest worker number so far, veterans included
	cost     float64
	// costEstimated: some of cost is estimated from tokens (estimate.go).
	costEstimated bool
	// The lead's own file edits (#260): the call ids of its edit tool
	// calls in flight, whether it holds the write lease for them, and the
	// warnings for edits it made while a writer held the lease.
	leadEdits    map[string]bool
	leadHolds    bool
	leadWarnings []string
	// iso is set when writers get their own worktrees (worktree.go);
	// mergeMu runs org_merge calls one at a time.
	iso     *isolation
	mergeMu sync.Mutex
	// subagents journals the workers' native subagents (monomind#387).
	subagents Subagents
	// The workers' profile context (ProfileContext), kept once per turn and
	// asked for again after org_roster: its database queries and the
	// extension probe would otherwise run at every spawn (profileContext).
	profileMu    sync.Mutex
	profileText  string
	profileFresh bool
	// msched is the scheduler's state (mission_sched.go).
	msched missionState
}

type worker struct {
	id      string
	staff   Staff
	brief   string
	files   []string
	model   Model
	status  string
	report  string
	errText string
	session string
	cancel  context.CancelFunc
	done    chan struct{}
	changed map[string]bool
	inTok   int64
	outTok  int64
	hasTok  bool
	cost    float64
	hasCost bool
	// liveCost is what the running exec has cost so far (not yet in cost); a mission's
	// session is charged for it as it grows (recordSpend).
	liveCost float64
	// costEstimated: some of cost is estimated from tokens (estimate.go).
	costEstimated bool
	started       time.Time
	// followups counts org_message runs, capped at MaxFollowups.
	followups int
	// askedThisRun counts this run's ask_user calls (capped at
	// MaxQuestions); questionSeq numbers its questions across runs, so an
	// id never repeats within the turn (#256). hasSlot says whether it holds
	// a concurrency slot right now: it lets go of the slot and its leases
	// while it waits for an answer.
	askedThisRun int
	questionSeq  int
	openQuestion string
	hasSlot      bool
	// The spawn tree (#230, tree.go): the worker that spawned it (nil =
	// the lead), whether the lead let it spawn, and whether it must run
	// confined (a research worker's sub-worker). runCtx is its current
	// run's context, which its sub-workers' runs derive from, so they end
	// with it.
	parent      *worker
	allowSpawn  bool
	mustConfine bool
	runCtx      context.Context
	// suspended counts the waits (a question, its sub-workers) it holds
	// nothing for; held is what it gave up for them.
	suspended int
	held      []heldLease
	// veteran: loaded from an earlier turn of the conversation, and cwd
	// the folder its session ran in (veterans.go).
	veteran  bool
	cwd      string
	notReady bool // a veteran whose model is no longer ready
	treeDone bool // a veteran's worktree was tried (veteranWorktree)
	// unconfined: a research worker whose read-only sandbox could not be
	// applied at run time; it runs holding the write lease instead.
	unconfined bool
	// confinement is what confines its current run (chatevents.Confinement*).
	confinement string
	// unusable holds the models (runtime/model) that couldn't run this
	// worker (auth, quota, …), so a retry doesn't try them again.
	unusable map[string]bool
	stream   workerStream // its text and live usage (workerstream.go)
	leases   []heldLease  // the leases it holds, for agent.status (lease.go)
	// worktree, branch and workDir are set for an isolated writer
	// (worktree.go): it runs in workDir, inside worktree, on branch.
	// merging is set while org_merge commits and merges its branch.
	worktree, branch, workDir string
	verifyTree                string // a verifier's detached checkout of the executor's branch
	merging                   bool
	merged                    bool // org_merge brought its branch into the chat folder
	// task is the mission task it works, if any (mission_exec.go).
	task *missionBind
}

// MaxFollowups caps org_message runs per worker, so follow-ups can't stand
// in for new workers past the turn's agent cap.
const MaxFollowups = 3

// errBudgetRefused is execOnce's refusal to start a run once the workers'
// budget is spent. It is the org's cap, not the model's failure, so it is
// not recorded as a model outcome.
var errBudgetRefused = errors.New("budget")

// errUnconfined is execOnce's refusal of a research run whose read-only
// sandbox was not applied at run time (#261).
var errUnconfined = errors.New("the read-only sandbox was not applied")

// errBudgetSpent is returned when the workers' budget has run out.
func (c *Conductor) budgetErrLocked() error {
	if b := c.cfg.Limits.BudgetUSD; b > 0 && c.cost >= b {
		if c.costEstimated {
			return fmt.Errorf("the workers' budget of $%.2f for this turn is spent (≈$%.2f, partly estimated from token counts)", b, c.cost)
		}
		return fmt.Errorf("the workers' budget of $%.2f for this turn is spent ($%.2f)", b, c.cost)
	}
	return nil
}

// New starts a conductor for one lead turn; Close ends it.
func New(ctx context.Context, cfg Config) *Conductor {
	if cfg.Limits.MaxAgents <= 0 || cfg.Limits.MaxConcurrent <= 0 {
		d := DefaultLimits()
		if cfg.Limits.MaxAgents <= 0 {
			cfg.Limits.MaxAgents = d.MaxAgents
		}
		if cfg.Limits.MaxConcurrent <= 0 {
			cfg.Limits.MaxConcurrent = d.MaxConcurrent
		}
	}
	if cfg.Now == nil {
		cfg.Now = time.Now
	}
	if cfg.Exec == nil {
		cfg.Exec = monomind.Exec
	}
	cctx, cancel := context.WithCancel(ctx)
	c := &Conductor{
		cfg: cfg, ctx: cctx, cancel: cancel,
		slots: make(chan struct{}, cfg.Limits.MaxConcurrent),
		write: newLease(), browser: newLease(),
		workers: map[string]*worker{}, leadEdits: map[string]bool{},
	}
	c.initWriters()
	return c
}

// Close cancels every worker still running and waits for them to finish.
// The lead's turn is over, so nothing is left to read their reports.
func (c *Conductor) Close() {
	c.cancel()
	c.wg.Wait()
	c.missionClose()
	c.leadStopsEditing()
	c.cleanupWorktrees()
}

// WorkerInfo is what the lead's tools report about a worker.
type WorkerInfo struct {
	ID      string   `json:"agent_id"`
	Role    string   `json:"role"`
	Status  string   `json:"status"`
	Runtime string   `json:"runtime"`
	Model   string   `json:"model,omitempty"`
	Effort  string   `json:"effort,omitempty"`
	Access  string   `json:"access"`
	Skills  []string `json:"skills,omitempty"`
	Why     string   `json:"why,omitempty"`
	Report  string   `json:"report,omitempty"`
	Error   string   `json:"error,omitempty"`
	Files   []string `json:"files_changed,omitempty"`
	// ParentID is the worker that spawned it ("" = the lead); AllowSpawn
	// says it may spawn sub-workers; Veteran marks a worker from an earlier
	// turn (#230).
	ParentID   string `json:"parent_id,omitempty"`
	AllowSpawn bool   `json:"allow_spawn,omitempty"`
	Veteran    bool   `json:"veteran,omitempty"`
	// Question is the question a waiting_user worker asked the user (#256).
	Question string `json:"question,omitempty"`
	// Branch is an isolated writer's branch (#230).
	Branch string `json:"branch,omitempty"`
}

func (c *Conductor) infoLocked(w *worker, withReport bool) WorkerInfo {
	info := WorkerInfo{
		ID: w.id, Role: w.staff.Role, Status: w.status, Runtime: w.model.Runtime, Model: w.model.Model,
		Effort: w.staff.Effort, Access: w.staff.Access, Error: w.errText, Files: sortedKeys(w.changed),
		Question: w.openQuestion, Branch: w.branch,
	}
	info.ParentID, info.AllowSpawn, info.Veteran = parentID(w), w.allowSpawn, w.veteran
	for _, s := range w.staff.Skills {
		info.Skills = append(info.Skills, s.Name)
	}
	if withReport {
		info.Report = w.report
	}
	return info
}

// Spawn staffs and starts a worker. With req.Wait it also waits for it
// (up to MaxWait).
func (c *Conductor) Spawn(ctx context.Context, req SpawnRequest) (WorkerInfo, error) {
	return c.spawn(ctx, req, nil)
}

// spawn starts a worker for the lead (parent nil) or a sub-worker for
// parent (tree.go). The limits count the whole tree.
func (c *Conductor) spawn(ctx context.Context, req SpawnRequest, parent *worker) (WorkerInfo, error) {
	if parent != nil {
		var err error
		if req, err = childRequest(parent, req); err != nil {
			return WorkerInfo{}, err
		}
	}
	c.mu.Lock()
	free := c.capFreeLocked(req)
	if !free && c.spawned >= c.cfg.Limits.MaxAgents {
		c.mu.Unlock()
		return WorkerInfo{}, c.capError()
	}
	if err := c.budgetErrLocked(); err != nil {
		c.mu.Unlock()
		return WorkerInfo{}, err
	}
	c.mu.Unlock()

	var task *missionBind
	if req.TaskID != 0 {
		var err error
		prepare := func(ctx context.Context, r SpawnRequest, p *worker) (*missionBind, SpawnRequest, error) {
			return c.missionPrepare(ctx, r, p)
		}
		if req.verify {
			prepare = c.verifyPrepare
		}
		if task, req, err = prepare(ctx, req, parent); err != nil {
			return WorkerInfo{}, err
		}
	}
	st, err := c.cfg.Staffer.Staff(ctx, req)
	if err != nil {
		return WorkerInfo{}, err
	}
	if parent != nil {
		if err := c.fitChild(parent, req, &st); err != nil {
			return WorkerInfo{}, err
		}
	}
	if task != nil && !task.verifier { // a verifier holds no claim: it only checks
		if err := c.missionClaim(ctx, task); err != nil {
			return WorkerInfo{}, err
		}
	}
	c.mu.Lock()
	if !free && c.spawned >= c.cfg.Limits.MaxAgents { // another spawn won the race
		c.mu.Unlock()
		c.missionUnclaim(task, "the turn's worker cap was reached first")
		return WorkerInfo{}, c.capError()
	}
	c.countSpawnLocked(req, free)
	c.seq++
	w := &worker{
		id: fmt.Sprintf("w%d", c.seq), staff: st, brief: req.Brief, files: req.Files,
		model: st.Model, status: chatevents.AgentQueued, changed: map[string]bool{}, started: c.cfg.Now(),
		parent: parent, allowSpawn: req.AllowSpawn && parent == nil, cwd: c.cfg.Cwd,
		mustConfine: parent != nil && st.Access == ProfileResearch && parent.staff.Access == ProfileResearch,
		task:        task,
	}
	c.workers[w.id] = w
	c.order = append(c.order, w.id)
	c.mu.Unlock()
	if task != nil && !task.verifier {
		c.missionStarted(task, task.executor+":"+w.id)
	}

	c.addWorktree(w)
	skills := make([]string, len(st.Skills))
	for i, s := range st.Skills {
		skills[i] = s.Name
	}
	c.cfg.Emit.Emit(chatevents.EventAgentSpawned, chatevents.AgentSpawnedPayload{
		AgentID: w.id, ParentID: parentID(w), AllowSpawn: w.allowSpawn, Role: st.Role, AgentType: st.AgentType, Skills: skills,
		Runtime: st.Model.Runtime, Model: st.Model.Model, Fidelity: st.Model.Fidelity, Effort: st.Effort, Access: st.Access,
		Brief: boundText(req.Brief, 2000), Why: strings.Join(st.Why, "; "), PickConfidence: st.PickConf, JevConfidence: st.JevConf,
		Branch: w.branch,
	})
	c.emitMessage(w.id, "brief", orLead(parent), w.id, req.Brief)
	c.cfg.Emit.Emit(chatevents.EventAgentStatus, chatevents.AgentStatusPayload{AgentID: w.id, To: chatevents.AgentQueued, Branch: w.branch})

	c.mu.Lock()
	ctx2, cancel, done := c.prepareRunLocked(w)
	c.mu.Unlock()
	c.launch(w, ctx2, cancel, done, req.Brief, "", true)
	if req.Wait {
		infos := c.waitFor(ctx, parent, []string{w.id}, MaxWait)
		if len(infos) == 1 {
			return infos[0], nil
		}
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	info := c.infoLocked(w, false)
	info.Why = strings.Join(st.Why, "; ")
	return info, nil
}

// Wait blocks until the workers finish or timeout passes, then reports on
// each; ids empty means every worker.
func (c *Conductor) Wait(ctx context.Context, ids []string, timeout time.Duration) []WorkerInfo {
	return c.waitFor(ctx, nil, ids, timeout)
}

// wait is Wait for whoever is waiting; ids empty means every worker.
func (c *Conductor) wait(ctx context.Context, ids []string, timeout time.Duration) []WorkerInfo {
	if timeout <= 0 || timeout > MaxWait {
		timeout = MaxWait
	}
	c.mu.Lock()
	if len(ids) == 0 {
		ids = slices.Clone(c.order)
	}
	var waits []chan struct{}
	for _, id := range ids {
		if w := c.workers[id]; w != nil && w.done != nil {
			waits = append(waits, w.done)
		}
	}
	c.mu.Unlock()
	deadline := time.NewTimer(timeout)
	defer deadline.Stop()
	for _, ch := range waits {
		select {
		case <-ch:
		case <-deadline.C:
			goto report
		case <-ctx.Done():
			goto report
		}
	}
report:
	c.mu.Lock()
	defer c.mu.Unlock()
	out := []WorkerInfo{}
	for _, id := range ids {
		if w := c.workers[id]; w != nil {
			// A veteran that hasn't run this turn has no new report.
			out = append(out, c.infoLocked(w, !running(w.status) && w.done != nil))
		} else {
			out = append(out, WorkerInfo{ID: id, Status: "unknown", Error: "no such worker"})
		}
	}
	return out
}

// Message sends a follow-up to a worker that has finished: its session is
// resumed when its runtime can, else it gets its earlier report as context.
func (c *Conductor) Message(ctx context.Context, id, text string) (WorkerInfo, error) {
	return c.message(ctx, nil, id, text)
}

// message is Message from the lead (from nil), or from a worker to one of
// its own sub-workers.
func (c *Conductor) message(ctx context.Context, from *worker, id, text string) (WorkerInfo, error) {
	if strings.TrimSpace(text) == "" {
		return WorkerInfo{}, fmt.Errorf("text is required")
	}
	c.mu.Lock()
	var w *worker
	for {
		var err error
		if w, err = c.followupCheckLocked(from, id); err != nil {
			c.mu.Unlock()
			return WorkerInfo{}, err
		}
		if !c.needsVeteranTreeLocked(w) {
			break
		}
		// A writing veteran gets its worktree only once the follow-up is
		// allowed; the checks run again after the git call.
		w.treeDone = true
		c.mu.Unlock()
		c.addWorktree(w)
		c.mu.Lock()
	}
	prompt, resume := text, ""
	// A session resumes only in the folder it ran in (a veteran's, or an
	// isolated writer's worktree of an earlier turn, is another).
	if w.session != "" && w.model.Resume && w.cwd == orElse(w.workDir, c.cfg.Cwd) {
		resume = w.session
	} else if w.report != "" {
		prompt = "Your earlier report:\n" + w.report + "\n\nFollow-up from the lead:\n" + text
	}
	w.report, w.errText = "", ""
	w.followups++
	w.askedThisRun = 0
	w.started = c.cfg.Now()
	// Marked queued in the same critical section as the running check, so
	// two concurrent follow-ups can't both start a run.
	ctx2, cancel, done := c.prepareRunLocked(w)
	info := c.infoLocked(w, false)
	c.mu.Unlock()
	c.emitMessage(id, "followup", orLead(from), id, text)
	c.launch(w, ctx2, cancel, done, prompt, resume, false)
	return info, nil
}

// followupCheckLocked finds the worker id and checks a follow-up to it
// from the lead (from nil) or its parent may start now.
func (c *Conductor) followupCheckLocked(from *worker, id string) (*worker, error) {
	w := c.workers[id]
	if w == nil {
		return nil, fmt.Errorf("no worker %q", id)
	}
	if err := messageScope(from, w); err != nil {
		return nil, err
	}
	if err := c.veteranBlockedLocked(w); err != nil {
		return nil, err
	}
	if running(w.status) {
		return nil, fmt.Errorf("%s is still working; org_wait for it first", id)
	}
	if w.merging {
		return nil, fmt.Errorf("%s's branch is being merged; send the follow-up once org_merge returns", id)
	}
	if err := c.budgetErrLocked(); err != nil {
		return nil, err
	}
	if w.followups >= MaxFollowups {
		return nil, fmt.Errorf("%s already had its %d follow-ups this turn; spawn a new worker if the turn's limit allows", id, MaxFollowups)
	}
	return w, nil
}

// Stop cancels one worker.
func (c *Conductor) Stop(id string) (WorkerInfo, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	w := c.workers[id]
	if w == nil {
		return WorkerInfo{}, fmt.Errorf("no worker %q", id)
	}
	if w.cancel != nil && running(w.status) {
		w.cancel()
	}
	return c.infoLocked(w, false), nil
}

func running(status string) bool {
	switch status {
	case chatevents.AgentQueued, chatevents.AgentStarting, chatevents.AgentWorking, chatevents.AgentWaitingLease, chatevents.AgentWaitingUser:
		return true
	}
	return false
}
