package control

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"

	"github.com/monoes/mono-agent/internal/workflow"
)

// SplitInBatchesNode is "Loop Over Items": each run emits the next batch of
// its input on the "loop" output (also emitted as "batch", the handle name
// older workflows are wired to). Wire the end of the loop body back into
// this node; every item fed back is collected, and once no batch is left
// the node emits all collected items on "done". The engine re-runs the body
// once per batch (see workflow.LoopNodeType).
//
// Config fields:
//
//	"batch_size" (number, required; n8n's "batchSize" also accepted): items per batch, 1–1000
//	"reset" (bool, optional; or "options": {"reset": true}): treat this
//	    run's input as a new set instead of continuing the current one
type SplitInBatchesNode struct{}

func (n *SplitInBatchesNode) Type() string { return "core.split_in_batches" }

// PublishesToThirdParty: core.split_in_batches sends nothing off this machine (issue #1418).
func (SplitInBatchesNode) PublishesToThirdParty() bool { return false }

// batchState is the node's progress through one set, kept in the
// execution's node state between runs.
type batchState struct {
	Remaining []workflow.Item `json:"remaining"`
	Processed []workflow.Item `json:"processed"`
}

func (n *SplitInBatchesNode) Execute(ctx context.Context, input workflow.NodeInput, config map[string]interface{}) ([]workflow.NodeOutput, error) {
	raw, ok := config["batch_size"]
	if !ok {
		raw = config["batchSize"]
	}
	var batchSize int
	switch v := raw.(type) {
	case int:
		batchSize = v
	case int64:
		batchSize = int(v)
	case float64:
		batchSize = int(v)
	case float32:
		batchSize = int(v)
	default:
		return nil, fmt.Errorf("split_in_batches: batch_size is required and must be a number")
	}

	if batchSize < 1 || batchSize > 1000 {
		return nil, fmt.Errorf("split_in_batches: batch_size must be between 1 and 1000, got %d", batchSize)
	}

	var st *batchState
	if prev := workflow.NodeStateFromContext(ctx); prev != nil && !resetRequested(config) {
		st = &batchState{}
		if err := json.Unmarshal(prev, st); err != nil {
			return nil, fmt.Errorf("split_in_batches: decode loop state: %w", err)
		}
		// A continuing run: what the body fed back has been processed.
		st.Processed = append(st.Processed, input.Items...)
	} else {
		// The first run of a set: everything is still to do.
		st = &batchState{Remaining: append([]workflow.Item(nil), input.Items...), Processed: []workflow.Item{}}
	}

	if len(st.Remaining) == 0 {
		if err := workflow.SetNodeState(ctx, nil); err != nil && !errors.Is(err, workflow.ErrNodeStateUnavailable) {
			return nil, err
		}
		return []workflow.NodeOutput{{Handle: "done", Items: st.Processed}}, nil
	}

	end := batchSize
	if end > len(st.Remaining) {
		end = len(st.Remaining)
	}
	batch := append([]workflow.Item(nil), st.Remaining[:end]...)
	st.Remaining = st.Remaining[end:]
	// Outside an execution (no state slot) the node is a one-shot that
	// emits the first batch.
	if err := workflow.SetNodeState(ctx, st); err != nil && !errors.Is(err, workflow.ErrNodeStateUnavailable) {
		return nil, err
	}
	return []workflow.NodeOutput{
		{Handle: "loop", Items: batch},
		{Handle: "batch", Items: batch},
	}, nil
}

// resetRequested reports n8n's "reset" option, at the top level or under
// "options".
func resetRequested(config map[string]interface{}) bool {
	if v, ok := config["reset"].(bool); ok {
		return v
	}
	if opts, ok := config["options"].(map[string]interface{}); ok {
		v, _ := opts["reset"].(bool)
		return v
	}
	return false
}
