package workflow

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/monoes/mono-agent/internal/secrets"
	"github.com/monoes/mono-agent/internal/vault"
)

// nodeTask is one node run that prepare set up and finish completes.
type nodeTask struct {
	node        WorkflowNode
	executor    NodeExecutor
	input       NodeInput
	config      map[string]interface{}
	retryPolicy RetryPolicy
	onError     string
	execNode    *WorkflowExecutionNode
	ctx         context.Context // the run's context plus this node's state slot

	perItem      *perItemPlan // non-nil: run once per input item
	itemFailures int          // per-item failures on_error tolerated
	attempts     int          // node attempts run, retries included
	runs         int          // executeWithRetry calls (one per item when perItem)

	outputs []NodeOutput
	execErr error
}

// prepare decides whether node runs now. It returns a task to execute, or
// nil when the node was handled without executing (already completed,
// disabled, skipped, deferred, waiting for merge inputs, or a trigger).
func (r *runner) prepare(node WorkflowNode) (*nodeTask, error) {
	ctx := r.ctx
	dag := r.dag
	logger := r.logger

	// On resume, skip nodes that already completed in the earlier run so
	// their side effects don't fire twice; their outputs were captured in
	// pendingInputs/nodeOutputs above.
	if r.completedNodes[node.ID] {
		return nil, nil
	}

	// Check context cancellation.
	select {
	case <-ctx.Done():
		return nil, ErrExecutionCancelled
	default:
	}

	// A locked account ends the run here, before this node starts; the node
	// before it has finished (account_node.go).
	if nodeErr := requireAccountForNode(ctx); nodeErr != nil {
		return nil, nodeErr
	}

	// Skip disabled nodes; still mark their successors so mergeWaiting
	// is decremented correctly.
	if node.Disabled {
		logger.Debug().
			Str("node_id", node.ID).
			Str("node_name", node.Name).
			Msg("skipping disabled node")
		r.passDisabled(node)
		r.recordSkipped(node)
		r.complete(node.ID)
		return nil, nil
	}

	// A node downstream of an active loop's "done" output waits until the
	// loop has finished; it is visited again after the loop's last pass.
	if r.deferred(node.ID) {
		return nil, nil
	}

	// Determine whether this is a merge node (InDegree > 1).
	inDeg := dag.forwardInDegree(node.ID)
	if inDeg > 1 {
		// On first encounter (before any predecessor has completed) we have
		// nothing to wait for yet — skip until all predecessors are done.
		if _, initialised := r.mergeWaiting[node.ID]; !initialised {
			// This shouldn't happen in topological order (all predecessors
			// run before us), but guard defensively.
			logger.Warn().
				Str("node_id", node.ID).
				Str("node_name", node.Name).
				Msg("merge node reached with uninitialised counter; skipping")
			return nil, nil
		}
		if r.mergeWaiting[node.ID] > 0 {
			// Still waiting for more predecessors.
			return nil, nil
		}
	}

	// Determine input items.
	var inputItems []Item
	if r.triggerNodeIDs[node.ID] {
		// Only the trigger node that actually fired receives the trigger
		// payload. When TriggerNodeID is empty (a manual run of a workflow
		// without exactly one trigger.manual) every trigger node fires. Other trigger nodes get no items, so their
		// downstream branch is skipped by the empty-input guard above.
		// trigger.evaluation fires only in the runs of an evaluation, never
		// in a manual run that fires every trigger.
		if node.Type == TriggerTypeEvaluation && r.exec.TriggerType != TriggerTypeEvaluation {
			inputItems = []Item{}
		} else if r.exec.TriggerNodeID == "" || node.ID == r.exec.TriggerNodeID {
			inputItems = r.triggerItems
		} else {
			inputItems = []Item{}
		}
	} else {
		inputItems = r.pendingInputs[node.ID]
	}
	if inputItems == nil {
		inputItems = []Item{}
	}

	// Skip non-trigger nodes that received no items from their predecessors.
	// When all upstream nodes produced 0 items the pipeline is "empty" — running
	// downstream nodes with an empty context produces garbage (unresolved template
	// variables, malformed API calls, etc.).  Persist a SKIPPED record so the
	// workflow editor can display them correctly in past-run views.
	// An active loop node always runs: its body may legitimately feed back
	// nothing, and the node must still emit its next batch or "done".
	if !r.triggerNodeIDs[node.ID] && inDeg > 0 && len(inputItems) == 0 && !r.loopActive(node.ID) {
		logger.Debug().
			Str("node_id", node.ID).
			Str("node_name", node.Name).
			Msg("no input items from predecessors — skipping node")
		r.recordSkipped(node)
		r.complete(node.ID)
		return nil, nil
	}

	// Parse node config.
	nodeCopy := node
	if err := nodeCopy.ParseConfig(); err != nil {
		return nil, fmt.Errorf("node %s (%s): parse config: %w", node.ID, node.Name, err)
	}
	// A copy: credential injection and the per-item field split below edit
	// the config, and a loop node's body runs this node again on the next
	// pass with the workflow's own (unchanged) config.
	config, _ := cloneConfigValue(nodeCopy.Config).(map[string]interface{})
	if config == nil {
		config = make(map[string]interface{})
	}

	r.injectCredential(node, config)

	// Trigger nodes are pass-through — they emit their trigger items
	// without needing a registered executor, and don't use config at
	// all, so this runs before config is resolved (or even looked up
	// in the registry, which trigger types never are).
	if strings.HasPrefix(node.Type, "trigger.") {
		// Route outputs to successors.
		for _, succ := range dag.Successors(node.ID) {
			r.route(node, succ.ID, inputItems)
		}
		// Store outputs for expression access.
		r.nodeOutputs[node.Name] = inputItems

		// Record execution node as SUCCESS. Its items are its output too,
		// so the run's detail shows what the trigger handed on.
		now2 := time.Now().UTC()
		execNode := &WorkflowExecutionNode{
			ExecutionID: r.exec.ID,
			NodeID:      node.ID,
			NodeName:    node.Name,
			Status:      "SUCCESS",
			InputItems:  inputItems,
			OutputItems: inputItems,
			StartedAt:   &now2,
			FinishedAt:  &now2,
		}
		_ = r.store.CreateExecutionNode(ctx, execNode)
		r.complete(node.ID)
		logger.Debug().
			Str("node_id", node.ID).
			Str("node_type", node.Type).
			Msg("trigger node pass-through")
		return nil, nil
	}

	// Get executor from registry. Constructed now (rather than after
	// config resolution) so a PerItemConfigResolver node can be queried
	// for the fields to hold back before expr.ResolveConfig runs.
	// Factories are cheap, side-effect-free constructors (e.g. "return
	// &FooNode{}") for every registered node type, so building the
	// executor here instead of later does not change behavior.
	factory, ok := r.registry.Get(node.Type)
	if !ok {
		err := fmt.Errorf("%w: %s", ErrNodeTypeUnknown, node.Type)
		r.recordNotStarted(node, inputItems, err)
		return nil, err
	}
	executor := factory()

	if executeOnce(config) && len(inputItems) > 1 {
		inputItems = inputItems[:1]
	}

	// Build expression context from the first input item (if any).
	var currentItemJSON map[string]interface{}
	if len(inputItems) > 0 {
		currentItemJSON = inputItems[0].JSON
	}
	exprCtx := buildExpressionContext(currentItemJSON, r.nodeOutputs, r.wf, r.exec)
	exprCtx.RunIndex = r.runIndex(node.ID)
	exprCtx.PrevNode = r.pendingFrom[node.ID]

	// Resolve config templates — but hold back any fields the node
	// declares via PerItemConfigResolver (e.g. a filter condition, or
	// an HTTP URL that reads from $json). Those nodes evaluate their
	// own held-back fields once per item; pre-resolving here would use
	// only the first item's JSON for the whole batch.
	var perItemFields []string
	if resolver, ok := executor.(PerItemConfigResolver); ok {
		perItemFields = resolver.PerItemConfigFields()
	}
	plan := r.planPerItem(node, executor, config, len(inputItems), perItemFields, exprCtx)
	// The config as the workflow holds it, which a @secret reference must
	// be in to resolve (secrets.ResolveConfig).
	stored, _ := cloneConfigValue(config).(map[string]interface{})
	fieldState := extractPerItemFields(config, perItemFields)
	resolvedConfig, err := r.expr.ResolveConfig(config, exprCtx)
	if err != nil {
		return nil, fmt.Errorf("node %s (%s): resolve config: %w", node.ID, node.Name, err)
	}
	restorePerItemFields(resolvedConfig, fieldState)
	if err := checkStoredFields(executor, stored, resolvedConfig); err != nil {
		return nil, fmt.Errorf("node %s (%s): %w", node.ID, node.Name, err)
	}

	// Resolve @img-NNN references to absolute vault file paths.
	if vaultDB := vault.DBFromContext(ctx); vaultDB != nil {
		profile, perr := vault.ProfileIDFromContext(ctx)
		if perr != nil {
			return nil, fmt.Errorf("node %s (%s) ran without a profile: %w", node.ID, node.Name, perr)
		}
		if err := vault.ResolveConfig(ctx, vaultDB, stored, resolvedConfig); err != nil {
			return nil, fmt.Errorf("node %s (%s): %w", node.ID, node.Name, err)
		}
		// Resolve @secret:<name> references to decrypted vault values; a run
		// the user hasn't approved resolves none, and the step doesn't run.
		if err := secrets.ResolveConfig(ctx, vaultDB, profile, stored, resolvedConfig); err != nil {
			return nil, fmt.Errorf("node %s (%s): %w", node.ID, node.Name, err)
		}
	}

	// AI sub-nodes attached through ai_… connections, resolved for the node.
	subNodes, err := r.resolveSubNodes(node.ID, exprCtx, 0)
	if err != nil {
		return nil, fmt.Errorf("node %s (%s): %w", node.ID, node.Name, err)
	}

	if dag.isLoopNode(node.ID) {
		if err := r.countLoopIteration(node, inputItems, resolvedConfig); err != nil {
			return nil, err
		}
	}

	// Create execution-node record in RUNNING state.
	now := time.Now().UTC()
	execNode := &WorkflowExecutionNode{
		ExecutionID: r.exec.ID,
		NodeID:      node.ID,
		NodeName:    node.Name,
		Status:      "RUNNING",
		InputItems:  inputItems,
		StartedAt:   &now,
	}
	dbCtx1, dbCancel1 := dbCtx()
	if err := r.store.CreateExecutionNode(dbCtx1, execNode); err != nil {
		logger.Error().Err(err).
			Str("node_id", node.ID).
			Str("node_name", node.Name).
			Msg("failed to create execution node record")
		// Non-fatal for the execution itself — continue.
	}
	dbCancel1()

	return &nodeTask{
		node:     node,
		executor: executor,
		input: NodeInput{
			Items:       inputItems,
			NodeOutputs: r.nodeOutputs,
			WorkflowID:  r.wf.ID,
			ExecutionID: r.exec.ID,
			NodeID:      node.ID,
			NodeName:    node.Name,
			Location:    exprCtx.Location,
			SubNodes:    subNodes,
			Trigger:     exprCtx.Trigger,
			Vars:        exprCtx.Vars,
			RunIndex:    exprCtx.RunIndex,
			PrevNode:    exprCtx.PrevNode,
		},
		config: resolvedConfig,
		// Extract retry policy and on_error behaviour from config.
		retryPolicy: extractRetryPolicy(resolvedConfig),
		onError:     extractOnError(resolvedConfig),
		execNode:    execNode,
		ctx:         WithNodeState(ctx, r.nodeState[node.ID]),
		perItem:     plan,
	}, nil
}
