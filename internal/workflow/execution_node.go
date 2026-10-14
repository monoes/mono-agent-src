package workflow

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/monoes/mono-agent/internal/publication"
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
		// payload. When TriggerNodeID is empty (manual/retry runs) every
		// trigger node fires. Other trigger nodes get no items, so their
		// downstream branch is skipped by the empty-input guard above.
		if r.exec.TriggerNodeID == "" || node.ID == r.exec.TriggerNodeID {
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
	config := nodeCopy.Config
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

		// Record execution node as SUCCESS.
		now2 := time.Now().UTC()
		execNode := &WorkflowExecutionNode{
			ExecutionID: r.exec.ID,
			NodeID:      node.ID,
			NodeName:    node.Name,
			Status:      "SUCCESS",
			InputItems:  inputItems,
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
		return nil, fmt.Errorf("%w: %s", ErrNodeTypeUnknown, node.Type)
	}
	executor := factory()

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
	fieldState := extractPerItemFields(config, perItemFields)
	resolvedConfig, err := r.expr.ResolveConfig(config, exprCtx)
	if err != nil {
		return nil, fmt.Errorf("node %s (%s): resolve config: %w", node.ID, node.Name, err)
	}
	restorePerItemFields(resolvedConfig, fieldState)

	// Resolve @img-NNN references to absolute vault file paths.
	if vaultDB := vault.DBFromContext(ctx); vaultDB != nil {
		_ = vault.ResolveConfig(ctx, vaultDB, resolvedConfig)
		// Resolve @secret:<name> references to decrypted vault values.
		_ = secrets.ResolveConfig(ctx, vaultDB, vault.ProfileIDFromContext(ctx), resolvedConfig)
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
		},
		config: resolvedConfig,
		// Extract retry policy and on_error behaviour from config.
		retryPolicy: extractRetryPolicy(resolvedConfig),
		onError:     extractOnError(resolvedConfig),
		execNode:    execNode,
		ctx:         WithNodeState(ctx, r.nodeState[node.ID]),
	}, nil
}

// runTask executes a prepared node with retry. Safe to call concurrently
// for independent tasks: it touches only the task and the store.
func (r *runner) runTask(t *nodeTask) {
	ctx := t.ctx
	logger := r.logger
	t.outputs, t.execErr = executeWithRetry(ctx, t.executor, t.input, t.config, t.retryPolicy, func(outputs []NodeOutput) {
		// Capture returned successful items even when another item failed. Tracking
		// errors never change the publisher's outcome or retry decisions.
		if db := vault.DBFromContext(ctx); db != nil {
			pubOutputs := make([]publication.Output, 0, len(outputs))
			for _, out := range outputs {
				items := make([]map[string]interface{}, 0, len(out.Items))
				for _, item := range out.Items {
					items = append(items, item.JSON)
				}
				pubOutputs = append(pubOutputs, publication.Output{Handle: out.Handle, Items: items})
			}
			entries := publication.Normalize(publication.CaptureInput{NodeType: t.node.Type, Config: t.config, Outputs: pubOutputs,
				Source: withNodeRun(PublicationSource(ctx, t.input), t.execNode.ID)})
			publication.Record(ctx, publication.NewStore(db, vault.ProfileIDFromContext(ctx)), entries, func(msg string) { logger.Warn().Msg(msg) })
		}

	})
}

// finish records a task's outcome and routes its outputs. It reports
// paused=true when the node asked to pause; the caller persists the pause.
func (r *runner) finish(t *nodeTask) (paused bool, err error) {
	node, execNode, onError, execErr := t.node, t.execNode, t.onError, t.execErr
	dag, logger, ctx := r.dag, r.logger, r.ctx

	if st := NodeStateFromContext(t.ctx); st != nil {
		r.nodeState[node.ID] = st
	} else {
		delete(r.nodeState, node.ID)
	}

	// A node can pause the run (Human-in-Loop awaiting approval). Checked
	// before the failure path so the node is not recorded FAILED.
	if execErr != nil && errors.Is(execErr, ErrNodePaused) {
		return true, nil
	}

	if execErr != nil {
		// A partial failure still delivers the items that succeeded, under
		// every on_error mode.
		var pie *PartialItemsError
		if errors.As(execErr, &pie) && len(pie.Succeeded) > 0 {
			for _, out := range pie.Succeeded {
				h := out.Handle
				if h == "" {
					h = "main"
				}
				for _, succ := range dag.SuccessorsOnHandle(node.ID, h) {
					r.route(node, succ.ID, out.Items)
				}
			}
		}
		logger.Error().Err(execErr).
			Str("node_id", node.ID).
			Str("node_name", node.Name).
			Str("on_error", onError).
			Msg("node execution failed")

		// Persist failure.
		dbCtx2, dbCancel2 := dbCtx()
		if storeErr := recordNodeFailure(dbCtx2, r.store, execNode, execErr); storeErr != nil {
			logger.Error().Err(storeErr).
				Str("node_id", node.ID).
				Msg("failed to persist node failure")
		}
		dbCancel2()

		// A failed loop node ends its loop, whatever on_error says.
		if dag.isLoopNode(node.ID) {
			r.endLoop(node.ID)
		}

		switch onError {
		case "continue":
			// Pass through the input items so downstream nodes still receive data.
			// This preserves pipeline data even when a node fails (e.g. rate-limited AI).
			r.nonFatalFailures = append(r.nonFatalFailures, node.Name)
			successors := dag.SuccessorsOnHandle(node.ID, "main")
			for _, succ := range successors {
				r.route(node, succ.ID, t.input.Items)
			}
			r.nodeOutputs[node.Name] = t.input.Items
			r.complete(node.ID)
			return false, nil

		case "skip":
			// Mark the node as failed but do not propagate items or error downstream.
			// Downstream nodes will receive no input from this node and may not run.
			r.nonFatalFailures = append(r.nonFatalFailures, node.Name)
			r.complete(node.ID)
			return false, nil

		case "error_branch":
			// Route an error item to successors on the "error" handle.
			errorSuccessors := dag.SuccessorsOnHandle(node.ID, "error")
			if len(errorSuccessors) == 0 {
				// No edge wired from the error handle: the failure output
				// has nowhere to go and would be silently discarded while
				// the node reports SUCCESS. Fail the node instead so the
				// run reflects the error (wire an error edge to opt back
				// into routing).
				return false, fmt.Errorf("node %s (%s): on_error=error_branch but no edge is wired from the 'error' handle: %w", node.ID, node.Name, execErr)
			}
			r.nonFatalFailures = append(r.nonFatalFailures, node.Name)
			errorItems := []Item{
				NewItem(map[string]interface{}{
					"error":   execErr.Error(),
					"node_id": node.ID,
					"node":    node.Name,
				}),
			}
			for _, succ := range errorSuccessors {
				r.route(node, succ.ID, errorItems)
			}
			r.complete(node.ID)
			return false, nil

		default: // "stop" or anything else
			return false, fmt.Errorf("node %s (%s): %w", node.ID, node.Name, execErr)
		}
	}

	outputs := t.outputs

	// Auto-register any image files produced by the node into the image vault.
	autoRegisterItemImages(ctx, outputs, r.wf.ID, r.exec.ID)

	// Collect items per handle: "main" feeds nodeOutputs and the success
	// record; "error" is the failure-output convention shared by nodes
	// that report per-item failures instead of returning an error
	// (http.request connection failures and cap breaches,
	// system.execute_command non-zero exits, …).
	var mainItems []Item
	var errorHandleItems []Item
	for _, out := range outputs {
		switch out.Handle {
		case "error":
			errorHandleItems = append(errorHandleItems, out.Items...)
		case "main", "":
			mainItems = append(mainItems, out.Items...)
		}
	}

	// A node that routed failures to its "error" output must not report
	// SUCCESS when that output has nowhere to go: with no edge wired from
	// the error handle the items used to be silently dropped and the node
	// went green with 0 items. Preserved cases: a wired error edge (the
	// items route normally below) and an explicit on_error=continue/skip
	// policy. Anything else fails the node with the routed error, which
	// then follows the normal default (stop) handling for the run.
	if len(errorHandleItems) > 0 &&
		len(dag.SuccessorsOnHandle(node.ID, "error")) == 0 &&
		onError != "continue" && onError != "skip" {
		msg := errorHandleMessage(errorHandleItems)
		if msg == "" {
			msg = fmt.Sprintf("routed %d item(s) to the 'error' output", len(errorHandleItems))
		}
		nodeErr := fmt.Errorf("node %s (%s): %s", node.ID, node.Name, msg)
		logger.Error().Err(nodeErr).
			Str("node_id", node.ID).
			Str("node_name", node.Name).
			Msg("node emitted error output with no edge wired from the 'error' handle — failing node (wire an error edge or set on_error=continue/skip to tolerate)")
		fctx, fcancel := dbCtx()
		if storeErr := recordNodeFailure(fctx, r.store, execNode, nodeErr); storeErr != nil {
			logger.Error().Err(storeErr).
				Str("node_id", node.ID).
				Msg("failed to persist node failure")
		}
		fcancel()
		return false, nodeErr
	}

	r.nodeOutputs[node.Name] = mainItems

	// Persist success.
	dbCtx3, dbCancel3 := dbCtx()
	if storeErr := r.store.SetExecutionNodeFinished(dbCtx3, execNode.ID, "SUCCESS", mainItems, ""); storeErr != nil {
		logger.Error().Err(storeErr).
			Str("node_id", node.ID).
			Msg("failed to persist node success")
	}
	dbCancel3()

	if dag.isLoopNode(node.ID) {
		// Clear the consumed input first: the body's back-edge refills it
		// for the next iteration.
		delete(r.pendingInputs, node.ID)
		delete(r.pendingFrom, node.ID)
	}

	// Route outputs to downstream nodes by handle.
	for _, out := range outputs {
		handle := out.Handle
		if handle == "" {
			handle = "main"
		}
		successors := dag.SuccessorsOnHandle(node.ID, handle)
		for _, succ := range successors {
			r.route(node, succ.ID, out.Items)
		}
	}

	if dag.isLoopNode(node.ID) {
		r.afterLoopRun(t)
	}

	// Mark complete and update merge counters for downstream merge nodes.
	r.complete(node.ID)

	logger.Debug().
		Str("node_id", node.ID).
		Str("node_name", node.Name).
		Int("output_items", len(mainItems)).
		Msg("node completed successfully")
	return false, nil
}
