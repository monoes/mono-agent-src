// Package aitasknodes is the ai.task workflow node: it QUEUES an AI task
// (internal/aitask) and returns at once with {taskId, conversationId,
// status: "queued"}. It never waits for the task and never starts it: a person
// starts it from the bubble queue, which is also where its result lands (or
// `aitask result <id>` reads it later).
package aitasknodes

import (
	"context"
	"database/sql"
	"fmt"
	"slices"
	"strings"

	"github.com/monoes/mono-agent/internal/aitask"
	"github.com/monoes/mono-agent/internal/nodes/nodeprofile"
	"github.com/monoes/mono-agent/internal/operator"
	"github.com/monoes/mono-agent/internal/workflow"
)

// NodeType is the node's type name.
const NodeType = "ai.task"

// QueueFunc creates a queued task for profileID in db, and starts nothing.
// The CLI sets it (cmd/monoagentcli), since creating the task's coder
// conversation needs the CLI's launcher; it is nil in a process without one.
type QueueFunc func(ctx context.Context, db *sql.DB, profileID string, in aitask.SubmitInput) (aitask.Task, error)

// Queue is the function the node queues through.
var Queue QueueFunc

var globalDB *sql.DB

// RegisterAll registers ai.task into the registry.
func RegisterAll(r *workflow.NodeTypeRegistry, db *sql.DB) {
	globalDB = db
	r.Register(NodeType, func() workflow.NodeExecutor { return &TaskNode{} })
}

// TaskNode queues an AI task. Type: "ai.task"
type TaskNode struct{}

func (n *TaskNode) Type() string { return NodeType }

func (n *TaskNode) Execute(ctx context.Context, input workflow.NodeInput, config map[string]interface{}) ([]workflow.NodeOutput, error) {
	// A task is a coder conversation with a shell: a run that isn't the
	// operator's (a workflow nobody approved, one an AI wrote or started,
	// an agent's shell) can't open one, and so can't borrow the operator's
	// authority through it (plan 4b.3).
	if err := workflow.RefuseUnapprovedProgram(ctx, NodeType+" (it opens a coder task)", input.WorkflowID); err != nil {
		return nil, err
	}
	if operator.AIRun(ctx) {
		return nil, fmt.Errorf("%s opens a coder task, and this run isn't the user's own (an agent, the chat or another workflow started it): "+
			"only a workflow the user runs queues one", NodeType)
	}
	goal, _ := config["goal"].(string)
	if goal == "" {
		return nil, fmt.Errorf("%s: 'goal' is required", NodeType)
	}
	profileID, err := nodeprofile.Require(ctx, NodeType, config)
	if err != nil {
		return nil, err
	}
	in := aitask.SubmitInput{Goal: goal, Kind: "generic"}
	if v, _ := config["kind"].(string); v != "" {
		in.Kind = v
	}
	in.ApplicationID, _ = config["application_id"].(string)
	in.Access, _ = config["access"].(string)
	in.Runtime, _ = config["runtime"].(string)
	if raw, ok := config["context"].([]interface{}); ok {
		for _, r := range raw {
			s, _ := r.(string)
			cs, err := aitask.ParseContextSource(s)
			if err != nil {
				return nil, fmt.Errorf("%s: %w", NodeType, err)
			}
			if !slices.Contains(aitask.ContextSources, cs.Class) {
				return nil, fmt.Errorf("%s: context from %q is refused: a task is given only %s (messages and people threads never reach a task)",
					NodeType, cs.Class, strings.Join(aitask.ContextSources, ", "))
			}
			in.Context = append(in.Context, cs)
		}
	}
	// Whatever the config said: queue, never start, and no unattended runs.
	in.QueueOnly, in.Unattended = true, false
	if Queue == nil || globalDB == nil {
		return nil, fmt.Errorf("%s: AI tasks are not available in this process", NodeType)
	}
	t, err := Queue(ctx, globalDB, profileID, in)
	if err != nil {
		return nil, fmt.Errorf("%s: %w", NodeType, err)
	}
	out := map[string]interface{}{"taskId": t.ID, "conversationId": t.ConversationID, "status": t.Status}
	return []workflow.NodeOutput{{Handle: "main", Items: []workflow.Item{workflow.NewItem(out)}}}, nil
}
