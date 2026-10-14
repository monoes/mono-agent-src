package workflow

import (
	"errors"
	"fmt"
)

// ErrLoopRunaway is returned when a loop node keeps asking for another
// iteration far beyond what its input could need — an engine or node bug,
// never a user-facing limit.
var ErrLoopRunaway = errors.New("workflow: loop did not finish")

// loopState is the engine's bookkeeping for one active loop node. It is
// part of the resume state, so a loop survives a pause. The loop node's
// own progress (remaining and processed items) is its node state.
type loopState struct {
	// Iterations counts the loop node's runs after the one that started
	// the loop.
	Iterations int `json:"iterations"`
	// Budget is the iteration guard: the items the loop started with + 1,
	// growing with the items fed back when the node resets on each run.
	Budget int `json:"budget"`
	// LastBatch is the last batch emitted, fed back as the next input
	// when no back-edge closes the loop (legacy "batch" wiring).
	LastBatch []Item `json:"last_batch,omitempty"`
}

// loopActive reports whether loop node id has started and not finished.
func (r *runner) loopActive(id string) bool {
	_, ok := r.loops[id]
	return ok
}

// deferred reports whether nodeID waits for an active loop's "done".
func (r *runner) deferred(nodeID string) bool {
	for loopID := range r.loops {
		if r.dag.loopDeferred(loopID)[nodeID] {
			return true
		}
	}
	return false
}

// countLoopIteration counts a continuing run of an active loop node and
// enforces the iteration guard.
func (r *runner) countLoopIteration(node WorkflowNode, input []Item, config map[string]interface{}) error {
	ls, ok := r.loops[node.ID]
	if !ok {
		return nil
	}
	ls.Iterations++
	if loopResets(config) {
		ls.Budget += len(input)
	}
	if ls.Iterations > ls.Budget {
		return fmt.Errorf("node %s (%s): %w after %d iterations", node.ID, node.Name, ErrLoopRunaway, ls.Iterations)
	}
	return nil
}

// loopResets reports whether a loop node's config restarts it on every run
// (n8n's options.reset).
func loopResets(config map[string]interface{}) bool {
	if v, ok := config["reset"].(bool); ok {
		return v
	}
	if opts, ok := config["options"].(map[string]interface{}); ok {
		v, _ := opts["reset"].(bool)
		return v
	}
	return false
}

// afterLoopRun updates the loop's state after its node ran successfully: a
// batch on the loop output keeps (or starts) the loop, anything else ends it.
func (r *runner) afterLoopRun(t *nodeTask) {
	id := t.node.ID
	var batch []Item
	for _, out := range t.outputs {
		if isLoopHandle(out.Handle) && len(out.Items) > 0 {
			batch = out.Items
			break
		}
	}
	if len(batch) == 0 {
		r.endLoop(id)
		return
	}
	ls, ok := r.loops[id]
	if !ok {
		ls = &loopState{Budget: len(t.input.Items) + 1}
		r.loops[id] = ls
	}
	if !r.dag.loopHasBackEdge(id) {
		ls.LastBatch = batch
	}
}

// endLoop finishes an active loop. Its body nodes, cleared for a pass that
// never came, are marked complete without records so the walk moves on to
// the nodes waiting for "done".
func (r *runner) endLoop(id string) {
	if !r.loopActive(id) {
		return
	}
	delete(r.loops, id)
	for n := range r.dag.loopBody(id) {
		r.completedNodes[n] = true
	}
}

// rewind starts the next iteration of the innermost active loop (the one
// latest in the order) and returns its position, or false when no loop is
// active and the run is over.
func (r *runner) rewind() (int, bool) {
	best, loopID := -1, ""
	for id := range r.loops {
		if idx := r.index[id]; idx > best {
			best, loopID = idx, id
		}
	}
	if best < 0 {
		return 0, false
	}
	body := r.dag.loopBody(loopID)
	delete(r.completedNodes, loopID)
	for n := range body {
		delete(r.completedNodes, n)
		delete(r.pendingInputs, n)
		delete(r.pendingFrom, n)
		r.resetMergeCounter(n, loopID, body)
	}
	if !r.dag.loopHasBackEdge(loopID) {
		r.pendingInputs[loopID] = r.loops[loopID].LastBatch
	}
	return best, true
}

// resetMergeCounter re-arms a merge node in a loop body for the next
// iteration: it waits only for its predecessors inside the loop, since
// those outside have already run and will not run again.
func (r *runner) resetMergeCounter(nodeID, loopID string, body map[string]bool) {
	if r.dag.forwardInDegree(nodeID) <= 1 {
		delete(r.mergeWaiting, nodeID)
		return
	}
	inside := 0
	for _, c := range r.dag.inEdges[nodeID] {
		if !r.dag.isBackEdge(c) && (body[c.SourceNodeID] || c.SourceNodeID == loopID) {
			inside++
		}
	}
	r.mergeWaiting[nodeID] = inside
}

// runIndex is the 0-based iteration of the innermost active loop whose body
// contains nodeID (0 outside loops), for $runIndex in expressions.
func (r *runner) runIndex(nodeID string) int {
	best, idx := -1, 0
	for id, ls := range r.loops {
		if r.dag.loopBody(id)[nodeID] && r.index[id] > best {
			best, idx = r.index[id], ls.Iterations
		}
	}
	return idx
}
