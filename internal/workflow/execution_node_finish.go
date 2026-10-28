package workflow

import (
	"errors"
	"fmt"
)

// finish records a task's outcome and routes its outputs. It reports
// paused=true when the node asked to pause; the caller persists the pause.
func (r *runner) finish(t *nodeTask) (paused bool, err error) {
	node, execNode, onError, execErr := t.node, t.execNode, t.onError, t.execErr
	dag, logger, ctx := r.dag, r.logger, r.ctx
	r.recordRetries(t)

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

	// A node cut short by a cancel (`workflow cancel`, a timeout, the daemon
	// stopping) did not fail: its error, or the items it routed to its error
	// output (system.execute_command's killed process), are the cancel's
	// doing. The run ends CANCELLED rather than following on_error or failing
	// on an unwired error output.
	if ctx.Err() != nil && (execErr != nil || routedToError(t.outputs)) {
		cerr := fmt.Errorf("node %s (%s): %w", node.ID, node.Name, cancelError(ctx))
		dbCtx, dbCancel := dbCtx()
		if storeErr := recordNodeFailure(dbCtx, r.store, execNode, cerr, nil); storeErr != nil {
			logger.Error().Err(storeErr).Str("node_id", node.ID).Msg("failed to persist node failure")
		}
		dbCancel()
		return false, cerr
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
		if storeErr := recordNodeFailure(dbCtx2, r.store, execNode, execErr, nil); storeErr != nil {
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

	outputs := alwaysOutputData(t.config, t.outputs)

	// Auto-register any image, audio or video files produced by the node into the media vault.
	autoRegisterItemImages(ctx, outputs, r.wf.ID, r.exec.ID)

	// Collect items per handle: "main" feeds nodeOutputs and the success
	// record; "error" is the failure-output convention shared by nodes
	// that report per-item failures instead of returning an error
	// (http.request connection failures and cap breaches,
	// system.execute_command non-zero exits, …). A node with no "main"
	// output (core.if true/false, switch cases, a loop's loop/done) records
	// the items it routed on its other handles instead, so the run's detail
	// is not empty; $node["IF"] reads its first output (firstOutputItems).
	var mainItems, branchItems []Item
	var errorHandleItems []Item
	hasMain, hasLoop := false, false
	for _, out := range outputs {
		if out.Handle == "loop" {
			hasLoop = true
		}
	}
	for _, out := range outputs {
		switch out.Handle {
		case "error":
			errorHandleItems = append(errorHandleItems, out.Items...)
		case "main", "":
			hasMain = true
			mainItems = append(mainItems, out.Items...)
		case "batch":
			// The loop node's "batch" repeats the items of its "loop" output.
			if !hasLoop {
				branchItems = append(branchItems, out.Items...)
			}
		default:
			branchItems = append(branchItems, out.Items...)
		}
	}
	exprItems := mainItems
	if !hasMain {
		mainItems = branchItems
		exprItems = firstOutputItems(node.Type, outputs)
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
		if storeErr := recordNodeFailure(fctx, r.store, execNode, nodeErr, errorHandleItems); storeErr != nil {
			logger.Error().Err(storeErr).
				Str("node_id", node.ID).
				Msg("failed to persist node failure")
		}
		fcancel()
		return false, nodeErr
	}

	r.nodeOutputs[node.Name] = exprItems
	if t.itemFailures > 0 {
		r.nonFatalFailures = append(r.nonFatalFailures, node.Name)
	}

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

// firstOutputItems is what $node["Name"] / $('Name') read of a node without
// a "main" output: as in n8n, its first output (output index 0) only. That
// is the first handle the node emits (core.if "true", core.switch its first
// case), except for the loop node, whose n8n output 0 is "done": empty
// until the loop has finished.
func firstOutputItems(nodeType string, outputs []NodeOutput) []Item {
	if nodeType == LoopNodeType {
		for _, out := range outputs {
			if out.Handle == "done" {
				return out.Items
			}
		}
		return nil
	}
	for _, out := range outputs {
		if out.Handle != "error" {
			return out.Items
		}
	}
	return nil
}
