package workflow

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"os"
	"runtime/debug"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/monoes/mono-agent/internal/connections"
	"github.com/monoes/mono-agent/internal/fsconfine"
	"github.com/monoes/mono-agent/internal/imagescan"
	"github.com/monoes/mono-agent/internal/publication"
	"github.com/monoes/mono-agent/internal/secrets"
	"github.com/monoes/mono-agent/internal/vault"
	"github.com/rs/zerolog"
)

// executionResumeState is the serialized snapshot persisted when an execution
// pauses (WAITING) and restored to continue it. pendingInputs already captures
// per-handle routing (items keyed by target node ID), so no per-handle output
// schema is needed to resume correctly.
type executionResumeState struct {
	PendingInputs  map[string][]Item `json:"pending_inputs"`
	NodeOutputs    map[string][]Item `json:"node_outputs"`
	CompletedNodes map[string]bool   `json:"completed_nodes"`
	MergeWaiting   map[string]int    `json:"merge_waiting"`
}

// RunExecution executes a workflow against its DAG. Called by WorkflowEngine.
// This is the core BFS execution loop.
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

	// Build initial trigger items — wrap TriggerData as a single Item.
	triggerItems := buildTriggerItems(exec.TriggerData)

	// nodeOutputs accumulates the "main" handle output for each node by name.
	// Used for $node["Name"] expression access.
	nodeOutputs := make(map[string][]Item)

	// pendingInputs accumulates items routed to each node by its ID.
	pendingInputs := make(map[string][]Item)

	// triggerNodeIDs is a set of trigger node IDs for quick lookup.
	triggerNodeIDs := make(map[string]bool, len(triggerNodes))
	for _, tn := range triggerNodes {
		triggerNodeIDs[tn.ID] = true
	}

	// mergeWaiting tracks how many predecessors still need to complete
	// before a merge node runs.  A node is treated as a merge node when it
	// has more than one incoming edge.  The counter is initialised to
	// InDegree(nodeID) - 1 on the first predecessor completion, then
	// decremented by each subsequent predecessor until it reaches zero.
	mergeWaiting := make(map[string]int)

	// Enrich context with workflow and execution IDs for vault registration.
	ctx = vault.ContextWithExecIDs(ctx, wf.ID, exec.ID)
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

	// Phase 2: BFS execution loop — process nodes in topological order.
	order, err := dag.TopologicalSort()
	if err != nil {
		return err
	}

	// completedNodes tracks which nodes have finished (by ID) so we can
	// manage the merge counters.
	completedNodes := make(map[string]bool, len(order))

	// If this is a resume of a paused (WAITING) execution, seed the working
	// state from the persisted snapshot so already-completed nodes are skipped
	// (not re-run) and the pause point continues with its captured inputs.
	if exec.ResumeState != "" {
		var rs executionResumeState
		if err := json.Unmarshal([]byte(exec.ResumeState), &rs); err != nil {
			return fmt.Errorf("resume: decode state for execution %s: %w", exec.ID, err)
		}
		if rs.PendingInputs != nil {
			pendingInputs = rs.PendingInputs
		}
		if rs.NodeOutputs != nil {
			nodeOutputs = rs.NodeOutputs
		}
		if rs.CompletedNodes != nil {
			completedNodes = rs.CompletedNodes
		}
		if rs.MergeWaiting != nil {
			mergeWaiting = rs.MergeWaiting
		}
	}

	// nonFatalFailures collects nodes that failed but let the run continue
	// (on_error=continue/skip/error_branch), so the final status can reflect
	// that the run had failures instead of reporting a misleading SUCCESS.
	var nonFatalFailures []string

	for _, node := range order {
		// On resume, skip nodes that already completed in the earlier run so
		// their side effects don't fire twice; their outputs were captured in
		// pendingInputs/nodeOutputs above.
		if completedNodes[node.ID] {
			continue
		}

		// Check context cancellation.
		select {
		case <-ctx.Done():
			return ErrExecutionCancelled
		default:
		}

		// Skip disabled nodes; still mark their successors so mergeWaiting
		// is decremented correctly.
		if node.Disabled {
			logger.Debug().
				Str("node_id", node.ID).
				Str("node_name", node.Name).
				Msg("skipping disabled node")
			skipNow := time.Now().UTC()
			skipCtx, skipCancel := dbCtx()
			_ = store.CreateExecutionNode(skipCtx, &WorkflowExecutionNode{
				ExecutionID: exec.ID,
				NodeID:      node.ID,
				NodeName:    node.Name,
				Status:      "SKIPPED",
				InputItems:  []Item{},
				OutputItems: []Item{},
				StartedAt:   &skipNow,
				FinishedAt:  &skipNow,
			})
			skipCancel()
			completedNodes[node.ID] = true
			decrementMergeWaiting(node.ID, dag, mergeWaiting, completedNodes)
			continue
		}

		// Determine whether this is a merge node (InDegree > 1).
		inDeg := dag.InDegree(node.ID)
		if inDeg > 1 {
			// On first encounter (before any predecessor has completed) we have
			// nothing to wait for yet — skip until all predecessors are done.
			if _, initialised := mergeWaiting[node.ID]; !initialised {
				// This shouldn't happen in topological order (all predecessors
				// run before us), but guard defensively.
				logger.Warn().
					Str("node_id", node.ID).
					Str("node_name", node.Name).
					Msg("merge node reached with uninitialised counter; skipping")
				continue
			}
			if mergeWaiting[node.ID] > 0 {
				// Still waiting for more predecessors.
				continue
			}
		}

		// Determine input items.
		var inputItems []Item
		if triggerNodeIDs[node.ID] {
			// Only the trigger node that actually fired receives the trigger
			// payload. When TriggerNodeID is empty (manual/retry runs) every
			// trigger node fires. Other trigger nodes get no items, so their
			// downstream branch is skipped by the empty-input guard above.
			if exec.TriggerNodeID == "" || node.ID == exec.TriggerNodeID {
				inputItems = triggerItems
			} else {
				inputItems = []Item{}
			}
		} else {
			inputItems = pendingInputs[node.ID]
		}
		if inputItems == nil {
			inputItems = []Item{}
		}

		// Skip non-trigger nodes that received no items from their predecessors.
		// When all upstream nodes produced 0 items the pipeline is "empty" — running
		// downstream nodes with an empty context produces garbage (unresolved template
		// variables, malformed API calls, etc.).  Persist a SKIPPED record so the
		// workflow editor can display them correctly in past-run views.
		if !triggerNodeIDs[node.ID] && dag.InDegree(node.ID) > 0 && len(inputItems) == 0 {
			logger.Debug().
				Str("node_id", node.ID).
				Str("node_name", node.Name).
				Msg("no input items from predecessors — skipping node")
			skipNow := time.Now().UTC()
			skipCtx, skipCancel := dbCtx()
			_ = store.CreateExecutionNode(skipCtx, &WorkflowExecutionNode{
				ExecutionID: exec.ID,
				NodeID:      node.ID,
				NodeName:    node.Name,
				Status:      "SKIPPED",
				InputItems:  []Item{},
				OutputItems: []Item{},
				StartedAt:   &skipNow,
				FinishedAt:  &skipNow,
			})
			skipCancel()
			completedNodes[node.ID] = true
			decrementMergeWaiting(node.ID, dag, mergeWaiting, completedNodes)
			continue
		}

		// Parse node config.
		nodeCopy := node
		if err := nodeCopy.ParseConfig(); err != nil {
			return fmt.Errorf("node %s (%s): parse config: %w", node.ID, node.Name, err)
		}
		config := nodeCopy.Config
		if config == nil {
			config = make(map[string]interface{})
		}

		// Inject credential data if credential_id is present.
		// GetOrResolve looks up the connections table by id or platform
		// name and auto-refreshes expired OAuth tokens. It is scoped to
		// the execution's profile so a workflow under profile B never
		// resolves profile A's credentials.
		if credIDRaw, ok := config["credential_id"]; ok {
			if credID, ok := credIDRaw.(string); ok && credID != "" {
				injected := false

				if connStore != nil {
					conn, err := connStore.GetOrResolve(ctx, credID, vault.ProfileIDFromContext(ctx))
					if err != nil {
						logger.Warn().Err(err).
							Str("credential_id", credID).
							Msg("connections lookup failed")
						// conn is nil; injected stays false, miss handling below runs
					} else if conn != nil {
						// Merge all connection Data fields directly into config.
						for k, v := range conn.Data {
							config[k] = v
						}
						config["credential"] = conn.Data
						injected = true
					}
				}

				// For browser platform nodes (instagram, gemini, linkedin, etc.),
				// the credential_id is just the session username. Inject it as
				// "username" so BrowserNode can resolve the session.
				if !injected {
					browserPlatforms := map[string]bool{
						"instagram": true, "gemini": true, "linkedin": true,
						"x": true, "tiktok": true, "telegram": true,
					}
					parts := strings.SplitN(node.Type, ".", 2)
					if len(parts) == 2 && browserPlatforms[parts[0]] {
						config["username"] = credID
						injected = true
					}
				}

				if !injected {
					// Miss on every path. Include the execution's profile in
					// the log so cross-profile resolution failures are
					// diagnosable.
					logger.Warn().
						Str("credential_id", credID).
						Str("profile", vault.ProfileIDFromContext(ctx)).
						Msg("credential not resolved for profile")
				}
			}
		}

		// Trigger nodes are pass-through — they emit their trigger items
		// without needing a registered executor, and don't use config at
		// all, so this runs before config is resolved (or even looked up
		// in the registry, which trigger types never are).
		if strings.HasPrefix(node.Type, "trigger.") {
			outputs := []NodeOutput{{Handle: "main", Items: inputItems}}
			// Route outputs to successors.
			for _, succ := range dag.Successors(node.ID) {
				pendingInputs[succ.ID] = append(pendingInputs[succ.ID], inputItems...)
			}
			// Store outputs for expression access.
			nodeOutputs[node.Name] = inputItems

			// Record execution node as SUCCESS.
			now2 := time.Now().UTC()
			execNode := &WorkflowExecutionNode{
				ExecutionID: exec.ID,
				NodeID:      node.ID,
				NodeName:    node.Name,
				Status:      "SUCCESS",
				InputItems:  inputItems,
				StartedAt:   &now2,
				FinishedAt:  &now2,
			}
			_ = store.CreateExecutionNode(ctx, execNode)
			_ = outputs // suppress unused warning
			completedNodes[node.ID] = true
			decrementMergeWaiting(node.ID, dag, mergeWaiting, completedNodes)
			logger.Debug().
				Str("node_id", node.ID).
				Str("node_type", node.Type).
				Msg("trigger node pass-through")
			continue
		}

		// Get executor from registry. Constructed now (rather than after
		// config resolution) so a PerItemConfigResolver node can be queried
		// for the fields to hold back before expr.ResolveConfig runs.
		// Factories are cheap, side-effect-free constructors (e.g. "return
		// &FooNode{}") for every registered node type, so building the
		// executor here instead of later does not change behavior.
		factory, ok := registry.Get(node.Type)
		if !ok {
			return fmt.Errorf("%w: %s", ErrNodeTypeUnknown, node.Type)
		}
		executor := factory()

		// Build expression context from the first input item (if any).
		var currentItemJSON map[string]interface{}
		if len(inputItems) > 0 {
			currentItemJSON = inputItems[0].JSON
		}
		exprCtx := buildExpressionContext(currentItemJSON, nodeOutputs, wf.ID, exec.ID)

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
		resolvedConfig, err := expr.ResolveConfig(config, exprCtx)
		if err != nil {
			return fmt.Errorf("node %s (%s): resolve config: %w", node.ID, node.Name, err)
		}
		restorePerItemFields(resolvedConfig, fieldState)

		// Resolve @img-NNN references to absolute vault file paths.
		if vaultDB := vault.DBFromContext(ctx); vaultDB != nil {
			_ = vault.ResolveConfig(ctx, vaultDB, resolvedConfig)
			// Resolve @secret:<name> references to decrypted vault values.
			_ = secrets.ResolveConfig(ctx, vaultDB, vault.ProfileIDFromContext(ctx), resolvedConfig)
		}

		// Extract retry policy and on_error behaviour from config.
		retryPolicy := extractRetryPolicy(resolvedConfig)
		onError := extractOnError(resolvedConfig)

		// Create execution-node record in RUNNING state.
		now := time.Now().UTC()
		execNode := &WorkflowExecutionNode{
			ExecutionID: exec.ID,
			NodeID:      node.ID,
			NodeName:    node.Name,
			Status:      "RUNNING",
			InputItems:  inputItems,
			StartedAt:   &now,
		}
		dbCtx1, dbCancel1 := dbCtx()
		if err := store.CreateExecutionNode(dbCtx1, execNode); err != nil {
			logger.Error().Err(err).
				Str("node_id", node.ID).
				Str("node_name", node.Name).
				Msg("failed to create execution node record")
			// Non-fatal for the execution itself — continue.
		}
		dbCancel1()

		// Build NodeInput.
		nodeInput := NodeInput{
			Items:       inputItems,
			NodeOutputs: nodeOutputs,
			WorkflowID:  wf.ID,
			ExecutionID: exec.ID,
			NodeID:      node.ID,
			NodeName:    node.Name,
		}

		// Execute with retry.
		outputs, execErr := executeWithRetry(ctx, executor, nodeInput, resolvedConfig, retryPolicy, func(outputs []NodeOutput) {
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
				entries := publication.Normalize(publication.CaptureInput{NodeType: node.Type, Config: resolvedConfig, Outputs: pubOutputs, AttemptID: uuid.NewString(),
					Source: PublicationSource(ctx, nodeInput)})
				publication.Record(ctx, publication.NewStore(db, vault.ProfileIDFromContext(ctx)), entries, func(msg string) { logger.Warn().Msg(msg) })
			}

		})

		// A node can pause the run (Human-in-Loop awaiting approval). Persist the
		// working state and suspend as WAITING rather than failing — resume picks
		// up here once the pause is resolved. Checked before the failure path so
		// the node is not recorded FAILED.
		if execErr != nil && errors.Is(execErr, ErrNodePaused) {
			state, mErr := json.Marshal(executionResumeState{
				PendingInputs:  pendingInputs,
				NodeOutputs:    nodeOutputs,
				CompletedNodes: completedNodes,
				MergeWaiting:   mergeWaiting,
			})
			if mErr != nil {
				return fmt.Errorf("pause: encode resume state for execution %s: %w", exec.ID, mErr)
			}
			pauseCtx, pauseCancel := dbCtx()
			werr := store.SetExecutionWaiting(pauseCtx, exec.ID, string(state))
			if werr == nil {
				recordPauseWindow(pauseCtx, store, exec.ID, execErr)
			}
			// Mark this node's record PAUSED rather than leaving it stuck RUNNING
			// in past-run views (resume adds a fresh record when it continues).
			_ = store.SetExecutionNodeFinished(pauseCtx, execNode.ID, "PAUSED", nil, "")
			pauseCancel()
			if werr != nil {
				return fmt.Errorf("pause: persist waiting state for execution %s: %w", exec.ID, werr)
			}
			logger.Info().Str("node_id", node.ID).Str("node_name", node.Name).Msg("execution paused, awaiting resume")
			return ErrExecutionPaused
		}

		if execErr != nil {
			logger.Error().Err(execErr).
				Str("node_id", node.ID).
				Str("node_name", node.Name).
				Str("on_error", onError).
				Msg("node execution failed")

			// Persist failure.
			dbCtx2, dbCancel2 := dbCtx()
			if storeErr := store.SetExecutionNodeFinished(dbCtx2, execNode.ID, "FAILED", nil, execErr.Error()); storeErr != nil {
				logger.Error().Err(storeErr).
					Str("node_id", node.ID).
					Msg("failed to persist node failure")
			}
			dbCancel2()

			switch onError {
			case "continue":
				// Pass through the input items so downstream nodes still receive data.
				// This preserves pipeline data even when a node fails (e.g. rate-limited AI).
				nonFatalFailures = append(nonFatalFailures, node.Name)
				successors := dag.SuccessorsOnHandle(node.ID, "main")
				for _, succ := range successors {
					pendingInputs[succ.ID] = append(pendingInputs[succ.ID], inputItems...)
				}
				nodeOutputs[node.Name] = inputItems
				completedNodes[node.ID] = true
				decrementMergeWaiting(node.ID, dag, mergeWaiting, completedNodes)
				continue

			case "skip":
				// Mark the node as failed but do not propagate items or error downstream.
				// Downstream nodes will receive no input from this node and may not run.
				nonFatalFailures = append(nonFatalFailures, node.Name)
				completedNodes[node.ID] = true
				decrementMergeWaiting(node.ID, dag, mergeWaiting, completedNodes)
				continue

			case "error_branch":
				// Route an error item to successors on the "error" handle.
				errorSuccessors := dag.SuccessorsOnHandle(node.ID, "error")
				if len(errorSuccessors) == 0 {
					// No edge wired from the error handle: the failure output
					// has nowhere to go and would be silently discarded while
					// the node reports SUCCESS. Fail the node instead so the
					// run reflects the error (wire an error edge to opt back
					// into routing).
					return fmt.Errorf("node %s (%s): on_error=error_branch but no edge is wired from the 'error' handle: %w", node.ID, node.Name, execErr)
				}
				nonFatalFailures = append(nonFatalFailures, node.Name)
				errorItems := []Item{
					NewItem(map[string]interface{}{
						"error":   execErr.Error(),
						"node_id": node.ID,
						"node":    node.Name,
					}),
				}
				for _, succ := range errorSuccessors {
					pendingInputs[succ.ID] = append(pendingInputs[succ.ID], errorItems...)
				}
				completedNodes[node.ID] = true
				decrementMergeWaiting(node.ID, dag, mergeWaiting, completedNodes)
				continue

			default: // "stop" or anything else
				return fmt.Errorf("node %s (%s): %w", node.ID, node.Name, execErr)
			}
		}

		// Auto-register any image files produced by the node into the image vault.
		autoRegisterItemImages(ctx, outputs, wf.ID, exec.ID)

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
			if storeErr := store.SetExecutionNodeFinished(fctx, execNode.ID, "FAILED", nil, nodeErr.Error()); storeErr != nil {
				logger.Error().Err(storeErr).
					Str("node_id", node.ID).
					Msg("failed to persist node failure")
			}
			fcancel()
			return nodeErr
		}

		nodeOutputs[node.Name] = mainItems

		// Persist success.
		dbCtx3, dbCancel3 := dbCtx()
		if storeErr := store.SetExecutionNodeFinished(dbCtx3, execNode.ID, "SUCCESS", mainItems, ""); storeErr != nil {
			logger.Error().Err(storeErr).
				Str("node_id", node.ID).
				Msg("failed to persist node success")
		}
		dbCancel3()

		// Route outputs to downstream nodes by handle.
		for _, out := range outputs {
			handle := out.Handle
			if handle == "" {
				handle = "main"
			}
			successors := dag.SuccessorsOnHandle(node.ID, handle)
			for _, succ := range successors {
				pendingInputs[succ.ID] = append(pendingInputs[succ.ID], out.Items...)
			}
		}

		// Mark complete and update merge counters for downstream merge nodes.
		completedNodes[node.ID] = true
		decrementMergeWaiting(node.ID, dag, mergeWaiting, completedNodes)

		logger.Debug().
			Str("node_id", node.ID).
			Str("node_name", node.Name).
			Int("output_items", len(mainItems)).
			Msg("node completed successfully")
	}

	if len(nonFatalFailures) > 0 {
		return &PartialFailureError{Nodes: nonFatalFailures}
	}
	return nil
}

// decrementMergeWaiting decrements the mergeWaiting counter for each direct
// successor of completedNodeID that is a merge node (InDegree > 1).
// On the first predecessor completion of a merge node the counter is
// initialised to InDegree - 1 (because the just-completed predecessor
// already "arrived").  On subsequent completions it is decremented by 1.
func decrementMergeWaiting(completedNodeID string, dag *DAG, mergeWaiting map[string]int, completedNodes map[string]bool) {
	for _, succ := range dag.Successors(completedNodeID) {
		if dag.InDegree(succ.ID) <= 1 {
			continue
		}
		if _, initialised := mergeWaiting[succ.ID]; !initialised {
			// First predecessor completing: initialise to InDegree - 1.
			mergeWaiting[succ.ID] = dag.InDegree(succ.ID) - 1
		} else {
			mergeWaiting[succ.ID]--
		}
	}
}

// dbCtx returns a short-lived context for DB persistence operations.
// It is intentionally independent of the execution context so that
// persistence succeeds even when the execution has been cancelled.
func dbCtx() (context.Context, context.CancelFunc) {
	return context.WithTimeout(context.Background(), 10*time.Second)
}

// executeWithRetry executes a node, retrying on failure according to the
// supplied RetryPolicy.  Panics from the executor are caught and returned
// as errors so that DB persistence in the caller is not bypassed.
//
// Errors a retry cannot fix (isNonRetryable) are returned after one attempt.
// Other errors are retried per policy, consulting RetryClassifier (when
// installed) before each retry: it may stop retrying or override the delay.
func executeWithRetry(
	ctx context.Context,
	executor NodeExecutor,
	input NodeInput,
	config map[string]interface{},
	policy RetryPolicy,
	observers ...func([]NodeOutput),
) (outputs []NodeOutput, err error) {
	maxRetries := policy.MaxRetries
	if maxRetries < 0 {
		maxRetries = 0
	}
	if maxRetries > 10 {
		maxRetries = 10
	}

	var lastErr error
	var override time.Duration // classifier-chosen delay for the next retry
	for attempt := 0; attempt <= maxRetries; attempt++ {
		if attempt > 0 {
			delay := computeDelay(attempt, policy)
			if override > 0 {
				delay = override
			}
			if retrySleep(ctx, delay) != nil || ctx.Err() != nil {
				return nil, ErrExecutionCancelled
			}
		}

		outputs, err = func() (out []NodeOutput, execErr error) {
			defer func() {
				if r := recover(); r != nil {
					execErr = fmt.Errorf("node executor panicked: %v\n%s", r, debug.Stack())
				}
			}()
			return executor.Execute(ctx, input, config)
		}()
		for _, observer := range observers {
			observer(outputs)
		}
		if err == nil {
			return outputs, nil
		}
		lastErr = err

		// Do not retry if the context is done.
		select {
		case <-ctx.Done():
			return nil, ErrExecutionCancelled
		default:
		}

		// A pause, invalid config, cancellation or Permanent error cannot be
		// fixed by running the node again: return it as-is (the caller relies
		// on errors.Is(err, ErrNodePaused) to suspend the run).
		if isNonRetryable(err) {
			return nil, err
		}
		if attempt == maxRetries {
			break // no retry left: never consult the classifier for nothing
		}
		retry, delay := classifyRetry(ctx, executor.Type(), err, attempt+1, config)
		if !retry {
			return nil, err
		}
		override = delay
	}
	return nil, lastErr
}

// computeDelay returns the sleep duration before the given retry attempt
// (attempt is 1-based: first retry is attempt=1).
func computeDelay(attempt int, p RetryPolicy) time.Duration {
	const maxDelaySeconds = 3600.0

	var seconds float64
	switch strings.ToLower(p.BackoffType) {
	case "exponential":
		// p.InitialDelay * 2^(attempt-1)
		seconds = p.InitialDelay * math.Pow(2, float64(attempt-1))
	default: // "fixed" or anything else
		seconds = p.InitialDelay
	}

	if seconds > maxDelaySeconds {
		seconds = maxDelaySeconds
	}
	if seconds < 0 {
		seconds = 0
	}

	return time.Duration(seconds * float64(time.Second))
}

// buildExpressionContext constructs an ExpressionContext from the current
// item's JSON map and the accumulated nodeOutputs map.
func buildExpressionContext(
	currentJSON map[string]interface{},
	nodeOutputs map[string][]Item,
	workflowID string,
	executionID string,
) ExpressionContext {
	if currentJSON == nil {
		currentJSON = make(map[string]interface{})
	}
	// Shallow copy nodeOutputs so the context is not mutated by later nodes.
	nodeCopy := make(map[string][]Item, len(nodeOutputs))
	for k, v := range nodeOutputs {
		nodeCopy[k] = v
	}
	return ExpressionContext{
		JSON:        currentJSON,
		Node:        nodeCopy,
		WorkflowID:  workflowID,
		ExecutionID: executionID,
		Env:         nil, // populated from ctx.Env; OS env only merged when MONOAGENT_ALLOW_ENV_TEMPLATES=1
	}
}

// buildTriggerItems wraps trigger data as a single Item.
// If triggerData is nil or empty, a single empty Item is returned so
// trigger nodes always receive at least one item to process.
func buildTriggerItems(triggerData map[string]interface{}) []Item {
	if triggerData == nil {
		triggerData = make(map[string]interface{})
	}
	return []Item{NewItem(triggerData)}
}

// extractRetryPolicy reads RetryPolicy fields from the resolved config map.
// Defaults: MaxRetries=0, BackoffType="fixed", InitialDelay=1.
func extractRetryPolicy(config map[string]interface{}) RetryPolicy {
	p := RetryPolicy{
		MaxRetries:   0,
		BackoffType:  "fixed",
		InitialDelay: 1,
	}

	rpRaw, ok := config["retry_policy"]
	if !ok {
		return p
	}

	// Try to round-trip through JSON to get a clean RetryPolicy struct.
	b, err := json.Marshal(rpRaw)
	if err != nil {
		return p
	}
	var parsed RetryPolicy
	if err := json.Unmarshal(b, &parsed); err != nil {
		return p
	}
	if parsed.MaxRetries > 0 {
		p.MaxRetries = parsed.MaxRetries
	}
	if parsed.BackoffType != "" {
		p.BackoffType = parsed.BackoffType
	}
	if parsed.InitialDelay > 0 {
		p.InitialDelay = parsed.InitialDelay
	}
	return p
}

// extractOnError reads the on_error string from the resolved config map.
// Defaults to "stop".
func extractOnError(config map[string]interface{}) string {
	if v, ok := config["on_error"]; ok {
		if s, ok := v.(string); ok && s != "" {
			return s
		}
	}
	return "stop"
}

// errorHandleMessage extracts a human-readable failure message from the
// items a node routed to its "error" output. The convention is an "error"
// JSON field (http.request, system.execute_command emit it); items without
// one yield "" so the caller can fall back to a generic message.
func errorHandleMessage(items []Item) string {
	for _, it := range items {
		if it.JSON == nil {
			continue
		}
		if msg, ok := it.JSON["error"].(string); ok && msg != "" {
			return msg
		}
	}
	return ""
}

// perItemFieldState is the token extractPerItemFields returns, holding the
// raw values it removed from a node's config so restorePerItemFields can put
// them back unresolved after expr.ResolveConfig runs.
type perItemFieldState struct {
	topLevel map[string]interface{} // top-level key -> saved raw value
	nested   []nestedPerItemField
}

// nestedPerItemField holds the raw values saved from one sub-key (e.g.
// "value") across every element of one top-level array field (e.g.
// "assignments") that extractPerItemFields protected.
type nestedPerItemField struct {
	arrayKey string
	subKey   string
	saved    map[int]interface{} // element index -> saved raw value
}

// parsePerItemFieldSpec splits a PerItemConfigResolver field spec into an
// array key and sub-key, e.g. "assignments[].value" -> ("assignments",
// "value"). A spec with no "[]." is a plain top-level field; arrayKey is
// returned empty and key holds the spec itself.
func parsePerItemFieldSpec(spec string) (arrayKey, key string) {
	if idx := strings.Index(spec, "[]."); idx >= 0 {
		return spec[:idx], spec[idx+3:]
	}
	return "", spec
}

// extractPerItemFields removes the config fields named by specs — see
// PerItemConfigResolver — from config in place, so the caller's subsequent
// expr.ResolveConfig pass (which only sees the first input item) leaves them
// untouched. restorePerItemFields puts the raw values back afterward so the
// node itself can resolve them once per item.
func extractPerItemFields(config map[string]interface{}, specs []string) *perItemFieldState {
	state := &perItemFieldState{topLevel: make(map[string]interface{})}
	for _, spec := range specs {
		arrayKey, key := parsePerItemFieldSpec(spec)
		if arrayKey == "" {
			if v, ok := config[key]; ok {
				state.topLevel[key] = v
				delete(config, key)
			}
			continue
		}
		rawArr, ok := config[arrayKey].([]interface{})
		if !ok {
			continue
		}
		nested := nestedPerItemField{arrayKey: arrayKey, subKey: key, saved: make(map[int]interface{})}
		for i, rawElem := range rawArr {
			elem, ok := rawElem.(map[string]interface{})
			if !ok {
				continue
			}
			if v, ok := elem[key]; ok {
				nested.saved[i] = v
				delete(elem, key)
			}
		}
		state.nested = append(state.nested, nested)
	}
	return state
}

// restorePerItemFields writes the raw values extractPerItemFields removed
// back into resolvedConfig, unresolved. resolvedConfig is expr.ResolveConfig's
// output: a freshly built map/slice tree of the same shape (and, for arrays,
// the same length and order) as the config extractPerItemFields was called
// on, so the saved element indices still line up.
func restorePerItemFields(resolvedConfig map[string]interface{}, state *perItemFieldState) {
	for k, v := range state.topLevel {
		resolvedConfig[k] = v
	}
	for _, nested := range state.nested {
		rawArr, ok := resolvedConfig[nested.arrayKey].([]interface{})
		if !ok {
			continue
		}
		for i, v := range nested.saved {
			if i < 0 || i >= len(rawArr) {
				continue
			}
			elem, ok := rawArr[i].(map[string]interface{})
			if !ok {
				continue
			}
			elem[nested.subKey] = v
		}
	}
}

// autoRegisterItemImages checks if an output item contains an unregistered image file
// path in its JSON (e.g. file_path or image_path) and registers it in the image vault.
func autoRegisterItemImages(ctx context.Context, outputs []NodeOutput, wfID, execID string) {
	vaultDB := vault.DBFromContext(ctx)
	if vaultDB == nil {
		return
	}
	for _, out := range outputs {
		for _, item := range out.Items {
			if item.JSON == nil {
				continue
			}
			if vid, ok := item.JSON["vault_id"].(string); ok && vid != "" {
				continue
			}
			for _, key := range []string{"file_path", "image_path"} {
				p, ok := item.JSON[key].(string)
				if !ok || p == "" {
					continue
				}
				if !imagescan.IsImageFile(p) {
					continue
				}
				fi, err := os.Stat(p)
				if err != nil || fi.IsDir() {
					continue
				}
				if vaultID, err := vault.Register(ctx, vaultDB, p, "workflow", wfID, execID); err == nil {
					item.JSON["vault_id"] = vaultID
					break
				}
			}
		}
	}
}
