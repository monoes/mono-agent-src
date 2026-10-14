package control

import (
	"context"
	"fmt"
	"time"

	"github.com/monoes/mono-agent/internal/workflow"
)

// subWorkflowPollWindow is how long a waiting parent stays paused before the
// resume poll re-checks its children. The last child's end wakes it sooner;
// the window covers a wake that was missed (a child that ended in a process
// with no engine, or a process that exited first).
const subWorkflowPollWindow = 30 * time.Second

// ExecuteWorkflowNode runs another workflow with this node's items.
// Config fields:
//
//	"workflowId"         (string, required): the child workflow's ID or name
//	"mode"               (string): "once" runs one child with all items (the
//	                     default); "each" runs one child per item
//	"waitForSubWorkflow" (bool, default true): wait and emit the children's
//	                     output; false emits the input items at once, each
//	                     with the child's "subworkflow_execution_id"
//
// The parent does not hold a concurrency slot while it waits: the node starts
// the children and pauses; the engine resumes it when the last child ends.
type ExecuteWorkflowNode struct{}

func (n *ExecuteWorkflowNode) Type() string { return "core.execute_workflow" }

func (n *ExecuteWorkflowNode) Execute(ctx context.Context, input workflow.NodeInput, config map[string]interface{}) ([]workflow.NodeOutput, error) {
	runner, ok := workflow.SubWorkflowRunnerFrom(ctx)
	if !ok {
		return nil, fmt.Errorf("execute_workflow: no workflow engine in this run")
	}
	ref, _ := config["workflowId"].(string)
	if ref == "" {
		return nil, fmt.Errorf("%w: execute_workflow requires \"workflowId\"", workflow.ErrInvalidConfig)
	}
	mode, _ := config["mode"].(string)
	if mode == "" {
		mode = "once"
	}
	if mode != "once" && mode != "each" {
		return nil, fmt.Errorf("%w: execute_workflow \"mode\" must be once or each, got %q", workflow.ErrInvalidConfig, mode)
	}
	wait := true
	if v, ok := config["waitForSubWorkflow"].(bool); ok {
		wait = v
	}

	children, err := runner.SubWorkflowChildren(ctx, input.ExecutionID, input.NodeID)
	if err != nil {
		return nil, err
	}
	if len(children) == 0 {
		if children, err = startChildren(ctx, runner, input, ref, mode, wait); err != nil {
			return nil, err
		}
	}

	if !wait {
		return []workflow.NodeOutput{{Handle: "main", Items: tagWithChildIDs(input.Items, children, mode)}}, nil
	}
	for _, c := range children {
		if !c.Done() {
			return nil, workflow.PauseFor(subWorkflowPollWindow, "waiting for sub-workflow")
		}
	}
	var out []workflow.Item
	for _, c := range children {
		if c.Failed() {
			msg := c.Error
			if msg == "" {
				msg = "no error message"
			}
			return nil, fmt.Errorf("execute_workflow: sub-workflow execution %s %s: %s", c.ExecutionID, c.Status, msg)
		}
		out = append(out, c.Items...)
	}
	if out == nil {
		out = []workflow.Item{}
	}
	return []workflow.NodeOutput{{Handle: "main", Items: out}}, nil
}

// startChildren starts one child for all items, or one per item.
func startChildren(ctx context.Context, runner workflow.SubWorkflowRunner, input workflow.NodeInput, ref, mode string, wait bool) ([]workflow.SubWorkflowChild, error) {
	req := workflow.SubWorkflowStart{
		ParentExecutionID: input.ExecutionID,
		ParentNodeID:      input.NodeID,
		Workflow:          ref,
		Wait:              wait,
	}
	var children []workflow.SubWorkflowChild
	start := func(items []workflow.Item, index int) error {
		req.Items, req.ItemIndex = items, index
		id, err := runner.StartSubWorkflow(ctx, req)
		if err != nil {
			return fmt.Errorf("execute_workflow: %w", err)
		}
		children = append(children, workflow.SubWorkflowChild{ExecutionID: id, ItemIndex: index, Status: "QUEUED"})
		return nil
	}
	if mode == "once" {
		if err := start(input.Items, -1); err != nil {
			return nil, err
		}
		return children, nil
	}
	for i, it := range input.Items {
		if err := start([]workflow.Item{it}, i); err != nil {
			return nil, err
		}
	}
	return children, nil
}

// tagWithChildIDs copies items, adding the ID of the child each one went to.
func tagWithChildIDs(items []workflow.Item, children []workflow.SubWorkflowChild, mode string) []workflow.Item {
	out := make([]workflow.Item, 0, len(items))
	for i, it := range items {
		m := copyMap(it.JSON)
		for _, c := range children {
			if mode == "once" || c.ItemIndex == i {
				m["subworkflow_execution_id"] = c.ExecutionID
				break
			}
		}
		out = append(out, workflow.Item{JSON: m, Binary: it.Binary})
	}
	return out
}
