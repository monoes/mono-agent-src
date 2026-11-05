package workflow

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/monoes/mono-agent/internal/logging"

	"github.com/monoes/mono-agent/internal/connections"
	"github.com/monoes/mono-agent/internal/fsconfine"
	"github.com/monoes/mono-agent/internal/profiledir"
	"github.com/monoes/mono-agent/internal/secrets"
	"github.com/monoes/mono-agent/internal/vault"
	"github.com/rs/zerolog"
)

// runner holds the working state of one RunExecution call.
type runner struct {
	ctx       context.Context
	exec      *WorkflowExecution
	wf        *Workflow
	dag       *DAG
	registry  *NodeTypeRegistry
	store     WorkflowStore
	connStore *connections.Store
	expr      *ExpressionEngine
	logger    zerolog.Logger

	order          []WorkflowNode
	index          map[string]int // nodeID → position in order
	triggerNodeIDs map[string]bool
	triggerItems   []Item

	// nodeOutputs accumulates the "main" handle output for each node by name.
	// Used for $node["Name"] expression access.
	nodeOutputs map[string][]Item
	// pendingInputs accumulates items routed to each node by its ID.
	pendingInputs map[string][]Item
	// pendingFrom names, by target node ID, the one node whose items are
	// in pendingInputs ($prevNode); "" when several nodes contributed.
	pendingFrom map[string]string
	// completedNodes tracks which nodes have finished (by ID) so we can
	// manage the merge counters. Inside a loop it covers the current
	// iteration only.
	completedNodes map[string]bool
	// mergeWaiting tracks how many predecessors still need to complete
	// before a merge node runs.  A node is treated as a merge node when it
	// has more than one incoming (forward) edge.  The counter is initialised
	// to its in-degree - 1 on the first predecessor completion, then
	// decremented by each subsequent predecessor until it reaches zero.
	mergeWaiting map[string]int
	// nodeState is each node's private state (SetNodeState), by node ID.
	nodeState map[string]json.RawMessage
	// loops holds the active (started, not yet done) loop nodes.
	loops map[string]*loopState

	// nonFatalFailures collects nodes that failed but let the run continue
	// (on_error=continue/skip/error_branch), so the final status can reflect
	// that the run had failures instead of reporting a misleading SUCCESS.
	nonFatalFailures []string

	// parallel is how many independent nodes may run at once (1 = sequential).
	parallel int

	// hasHIL: the workflow has a Human-in-Loop node, so outbound messages
	// may be queued in Communications and the nodes they wait for report
	// how they ended.
	hasHIL bool

	// activeBefore is the active time of the run's earlier segments (before
	// a pause); segmentStart is when this segment started. Together they
	// bound the run by its executionTimeout setting.
	activeBefore time.Duration
	segmentStart time.Time
}

// RunExecution executes a workflow against its DAG. Called by WorkflowEngine.
//
// Nodes run in topological order. A loop node (LoopNodeType) that emits a
// batch keeps its body re-running: when the walk reaches the end, it
// rewinds to the innermost active loop node, which then takes the items its
// body fed back and emits the next batch or, after the last one, "done".
func RunExecution(
	ctx context.Context,
	exec *WorkflowExecution,
	wf *Workflow,
	dag *DAG,
	registry *NodeTypeRegistry,
	store WorkflowStore,
	connStore *connections.Store,
	expr *ExpressionEngine,
	logger zerolog.Logger,
) error {
	// Phase 1: Initialize
	triggerNodes := dag.TriggerNodes()
	if len(triggerNodes) == 0 {
		return ErrNoTriggerNode
	}

	r := &runner{
		exec:      exec,
		wf:        wf,
		dag:       dag,
		registry:  registry,
		store:     store,
		connStore: connStore,
		expr:      expr,
		logger:    logger,
		// Build initial trigger items — wrap TriggerData as a single Item.
		// The webhook chain trace and the AI-caller mark stay internal (the
		// run's context has them): a workflow that echoes $json must not hand
		// them on.
		triggerItems:   buildTriggerItems(withoutTrace(withoutTrace(withoutTrace(exec.TriggerData, WebhookTraceKey), AICallerKey), SecretNamesKey)),
		nodeOutputs:    make(map[string][]Item),
		pendingInputs:  make(map[string][]Item),
		pendingFrom:    make(map[string]string),
		triggerNodeIDs: make(map[string]bool, len(triggerNodes)),
		mergeWaiting:   make(map[string]int),
		nodeState:      make(map[string]json.RawMessage),
		loops:          make(map[string]*loopState),
		parallel:       1,
		hasHIL:         dagHasHIL(dag),
	}
	for _, tn := range triggerNodes {
		r.triggerNodeIDs[tn.ID] = true
	}
	if parallelBranchesEnabled(wf) {
		r.parallel = maxParallelBranches
	}

	// Enrich context with workflow and execution IDs for vault registration.
	ctx = vault.ContextWithExecIDs(ctx, wf.ID, exec.ID)
	// The execution id is the run_id of every log line of this run, here and
	// in the child processes its nodes start (logging.ChildEnv).
	ctx = logging.WithRunID(ctx, exec.ID)
	r.logger = r.logger.With().Str(logging.KeyRunID, exec.ID).Logger()
	// C-46: a run an org role started through a grant carries the role's
	// workdir as org.workdir; file-touching nodes confine their paths to it.
	ctx = fsconfine.FromTriggerData(ctx, exec.TriggerData)
	// Org nodes continue the chain trace of whatever started the run, even
	// when a node between the trigger and them drops the `trace` field.
	ctx = WithTrigger(ctx, exec.TriggerType, snapshotTrigger(exec.TriggerData))
	// The execution owns its profile even if a multi-profile daemon's caller
	// context still holds a process-wide profile.
	if exec.ProfileID != "" {
		ctx = vault.ContextWithProfileID(ctx, exec.ProfileID)
	}
	// A run of a definition the operator never approved is an AI run,
	// whatever started it, and its file steps keep to FileGuard
	// (approvals.go). So do those of a workflow such a run called, whose own
	// approval still decides its program steps.
	profile, _ := vault.ProfileIDFromContext(ctx)
	project := profiledir.Root(vault.DBFromContext(ctx), profile)
	if approved, err := operatorApproved(ctx, store, wf); !approved {
		ctx = WithUnapproved(withApprovalCheckError(ctx, err), project, ApproveHint(profile, wf.ID))
		// The profile's owner may have let agent-written workflows read the vault
		// (`secret agent-access on`). Not when the approval check itself failed.
		if g, _ := secrets.GetAgentAccess(ctx, vault.DBFromContext(ctx), profile); g != nil && err == nil {
			ctx = secrets.AllowResolving(ctx, vault.DBFromContext(ctx), profile, secrets.AccessOrigin{WorkflowID: wf.ID, ExecutionID: exec.ID})
			// What the run stores of its steps never shows the values it read.
			r.store = &maskingStore{WorkflowStore: store, run: ctx}
			// Secrets a calling run read before it called this one.
			if err := secrets.SeedResolved(ctx, secretNamesOf(exec.TriggerData[SecretNamesKey])); err != nil {
				return err
			}
		}
	} else if caller, ok := CalledByUnapproved(exec.TriggerData); ok {
		hint := "this workflow was called by one you haven't approved: save or activate that one in the app or your terminal to let it"
		if caller != "" {
			hint = "this workflow was called by " + caller + ", which you haven't approved: " + ApproveHint(profile, caller)
		}
		ctx = fsconfine.GuardHiddenHome(ctx, FileGuard(project, hint))
	}
	// The run's token (runtoken.go): approved only when nothing makes the run an AI's. Its children carry this and
	// nothing else to prove it; a run that cannot record itself hands none, and they are held.
	if db := vault.DBFromContext(ctx); db != nil {
		aiStarted, _ := exec.TriggerData[AICallerKey].(string)
		_, called := CalledByUnapproved(exec.TriggerData)
		approved := !AIAuthoredFrom(ctx) && !called && strings.TrimSpace(aiStarted) == ""
		if tok, err := IssueRunToken(ctx, db, exec.ID, approved); err == nil {
			ctx = WithRunToken(ctx, tok)
			defer func() { _ = EndRunToken(context.Background(), db, tok) }()
		} else {
			r.logger.Warn().Err(err).Msg("run token not issued: this run's child processes are held as unapproved")
		}
	}
	r.ctx = ctx

	// Phase 2: execution loop — process nodes in topological order.
	order, err := dag.TopologicalSort()
	if err != nil {
		return err
	}
	r.order = order
	r.index = make(map[string]int, len(order))
	for i, n := range order {
		r.index[n.ID] = i
	}
	r.completedNodes = make(map[string]bool, len(order))

	// If this is a resume of a paused (WAITING) execution, seed the working
	// state from the persisted snapshot so already-completed nodes are skipped
	// (not re-run) and the pause point continues with its captured inputs.
	if exec.ResumeState != "" {
		if err := r.restore(exec.ResumeState); err != nil {
			return err
		}
	}

	ctx, cancel := r.withExecutionTimeout(r.ctx)
	defer cancel()
	r.ctx = ctx

	return maskRunError(ctx, r.timeoutErr(r.run()))
}

// restore seeds the working state from a persisted executionResumeState.
func (r *runner) restore(raw string) error {
	profile, _ := vault.ProfileIDFromContext(r.ctx)
	plain, err := openResumeState(r.ctx, vault.DBFromContext(r.ctx), profile, raw)
	if err != nil {
		return fmt.Errorf("resume: open state for execution %s: %w", r.exec.ID, err)
	}
	var rs executionResumeState
	if err := json.Unmarshal(plain, &rs); err != nil {
		return fmt.Errorf("resume: decode state for execution %s: %w", r.exec.ID, err)
	}
	if err := secrets.SeedResolved(r.ctx, rs.SecretNames); err != nil {
		return err
	}
	if rs.PendingInputs != nil {
		r.pendingInputs = rs.PendingInputs
	}
	if rs.PendingFrom != nil {
		r.pendingFrom = rs.PendingFrom
	}
	if rs.NodeOutputs != nil {
		r.nodeOutputs = rs.NodeOutputs
	}
	if rs.CompletedNodes != nil {
		r.completedNodes = rs.CompletedNodes
	}
	if rs.MergeWaiting != nil {
		r.mergeWaiting = rs.MergeWaiting
	}
	if rs.NodeState != nil {
		r.nodeState = rs.NodeState
	}
	if rs.Loops != nil {
		r.loops = rs.Loops
	}
	r.activeBefore = time.Duration(rs.ActiveMs) * time.Millisecond
	return nil
}

// run walks the order, rewinding into active loops until none is left.
func (r *runner) run() error {
	for i := 0; ; {
		for i < len(r.order) {
			batch := r.nextBatch(i)
			i += len(batch)
			if err := r.runBatch(batch); err != nil {
				return err
			}
		}
		next, ok := r.rewind()
		if !ok {
			break
		}
		i = next
	}

	if len(r.nonFatalFailures) > 0 {
		return &PartialFailureError{Nodes: r.nonFatalFailures}
	}
	return nil
}

// runBatch prepares, executes and finishes a set of mutually independent
// nodes (a single node unless parallel branches are enabled). Preparation and
// finishing run in order on this goroutine; only node execution overlaps.
func (r *runner) runBatch(batch []WorkflowNode) error {
	var tasks []*nodeTask
	for _, node := range batch {
		t, err := r.prepare(node)
		if err != nil {
			r.abandon(tasks, "not run: the run stopped before this node started")
			return err
		}
		if t != nil {
			tasks = append(tasks, t)
		}
	}

	r.executeTasks(tasks)

	var firstErr error
	var paused []*nodeTask
	for _, t := range tasks {
		isPaused, err := r.finish(t)
		if err != nil && firstErr == nil {
			firstErr = err
		}
		if isPaused {
			paused = append(paused, t)
		}
	}
	if firstErr != nil {
		r.abandon(paused, "not resumed: the run failed at another node")
		return firstErr
	}
	if len(paused) > 0 {
		return r.pause(paused)
	}
	return nil
}

// abandon closes the RUNNING records of tasks that will not finish because
// the run stopped elsewhere (only possible with parallel branches).
func (r *runner) abandon(tasks []*nodeTask, msg string) {
	for _, t := range tasks {
		c, cancel := dbCtx()
		_ = r.store.SetExecutionNodeFinished(c, t.execNode.ID, "SKIPPED", nil, msg)
		cancel()
	}
}

// pause persists the working state and suspends the execution as WAITING
// rather than failing — resume picks up here once the pause is resolved.
func (r *runner) pause(paused []*nodeTask) error {
	state, mErr := json.Marshal(executionResumeState{
		SecretNames:    secrets.ResolvedNames(r.ctx),
		PendingInputs:  r.pendingInputs,
		PendingFrom:    r.pendingFrom,
		NodeOutputs:    r.nodeOutputs,
		CompletedNodes: r.completedNodes,
		MergeWaiting:   r.mergeWaiting,
		NodeState:      r.nodeState,
		Loops:          r.loops,
		ActiveMs:       r.activeTime().Milliseconds(),
	})
	if mErr != nil {
		return fmt.Errorf("pause: encode resume state for execution %s: %w", r.exec.ID, mErr)
	}
	profile, _ := vault.ProfileIDFromContext(r.ctx)
	sealed, sErr := sealResumeState(r.ctx, vault.DBFromContext(r.ctx), profile, state, len(secrets.ResolvedNames(r.ctx)) > 0)
	if sErr != nil {
		return sErr
	}
	pauseCtx, pauseCancel := dbCtx()
	werr := r.store.SetExecutionWaiting(pauseCtx, r.exec.ID, sealed)
	if werr == nil {
		recordPauseWindow(pauseCtx, r.store, r.exec.ID, earliestPause(paused))
	}
	// Mark the node records PAUSED rather than leaving them stuck RUNNING
	// in past-run views (resume adds a fresh record when it continues).
	for _, t := range paused {
		_ = r.store.SetExecutionNodeFinished(pauseCtx, t.execNode.ID, "PAUSED", nil, "")
	}
	pauseCancel()
	if werr != nil {
		return fmt.Errorf("pause: persist waiting state for execution %s: %w", r.exec.ID, werr)
	}
	for _, t := range paused {
		r.logger.Info().Str("node_id", t.node.ID).Str("node_name", t.node.Name).Msg("execution paused, awaiting resume")
	}
	return ErrExecutionPaused
}

// earliestPause picks the pause error that should set the resume window: a
// pause with no window (Human-in-Loop) wins, otherwise the shortest window.
func earliestPause(paused []*nodeTask) error {
	var best error
	var bestAfter time.Duration
	for _, t := range paused {
		var pe *PauseError
		if !errors.As(t.execErr, &pe) || pe.ResumeAfter <= 0 {
			return t.execErr
		}
		if best == nil || pe.ResumeAfter < bestAfter {
			best, bestAfter = t.execErr, pe.ResumeAfter
		}
	}
	return best
}

// route queues items as input of node targetID, remembering which node
// fed it for $prevNode. Routing no items leaves the record alone.
func (r *runner) route(from WorkflowNode, targetID string, items []Item) {
	r.pendingInputs[targetID] = append(r.pendingInputs[targetID], items...)
	if len(items) == 0 {
		return
	}
	if prev, ok := r.pendingFrom[targetID]; ok && prev != from.Name {
		r.pendingFrom[targetID] = ""
	} else {
		r.pendingFrom[targetID] = from.Name
	}
}

// complete marks a node finished and updates merge counters downstream.
func (r *runner) complete(nodeID string) {
	r.completedNodes[nodeID] = true
	decrementMergeWaiting(nodeID, r.dag, r.mergeWaiting, r.completedNodes)
}

// recordSkipped persists a SKIPPED record for a node that did not run.
func (r *runner) recordSkipped(node WorkflowNode) {
	skipNow := time.Now().UTC()
	skipCtx, skipCancel := dbCtx()
	_ = r.store.CreateExecutionNode(skipCtx, &WorkflowExecutionNode{
		ExecutionID: r.exec.ID,
		NodeID:      node.ID,
		NodeName:    node.Name,
		Status:      "SKIPPED",
		InputItems:  []Item{},
		OutputItems: []Item{},
		StartedAt:   &skipNow,
		FinishedAt:  &skipNow,
	})
	skipCancel()
}

// recordNotStarted persists a FAILED record for a node that failed before
// it could start (its type is not installed), so the run's detail names the
// node rather than leaving it to the run's error text.
func (r *runner) recordNotStarted(node WorkflowNode, input []Item, err error) {
	now := time.Now().UTC()
	en := &WorkflowExecutionNode{
		ExecutionID: r.exec.ID,
		NodeID:      node.ID,
		NodeName:    node.Name,
		Status:      "RUNNING",
		InputItems:  input,
		StartedAt:   &now,
	}
	c, cancel := dbCtx()
	defer cancel()
	if r.store.CreateExecutionNode(c, en) == nil {
		_ = recordNodeFailure(c, r.store, en, err, nil)
	}
}

// decrementMergeWaiting decrements the mergeWaiting counter for each direct
// successor of completedNodeID that is a merge node (forward in-degree > 1).
// On the first predecessor completion of a merge node the counter is
// initialised to in-degree - 1 (because the just-completed predecessor
// already "arrived").  On subsequent completions it is decremented by 1.
// Loop back-edges do not count.
func decrementMergeWaiting(completedNodeID string, dag *DAG, mergeWaiting map[string]int, completedNodes map[string]bool) {
	for _, succ := range dag.forwardSuccessors(completedNodeID) {
		if dag.forwardInDegree(succ.ID) <= 1 {
			continue
		}
		if _, initialised := mergeWaiting[succ.ID]; !initialised {
			// First predecessor completing: initialise to InDegree - 1.
			mergeWaiting[succ.ID] = dag.forwardInDegree(succ.ID) - 1
		} else {
			mergeWaiting[succ.ID]--
		}
	}
}
