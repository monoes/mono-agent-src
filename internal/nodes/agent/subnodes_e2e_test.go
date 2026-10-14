package agent_test

import (
	"context"
	"strings"
	"testing"

	"github.com/rs/zerolog"

	"github.com/monoes/mono-agent/internal/aimemory"
	"github.com/monoes/mono-agent/internal/monomind"
	"github.com/monoes/mono-agent/internal/noderegistry"
	"github.com/monoes/mono-agent/internal/nodes/agent"
	"github.com/monoes/mono-agent/internal/testdb"
	"github.com/monoes/mono-agent/internal/vault"
	"github.com/monoes/mono-agent/internal/workflow"
)

// A workflow trigger → agent.ask with an ai.model and an ai.memory attached
// through ai_… connections runs on the real registry: the turn uses the
// model sub-node's runtime and model, the memory keeps the conversation
// per item session, and the sub-nodes never run as steps.
func TestAgentAskSubNodesEndToEnd(t *testing.T) {
	db := testdb.Open(t)
	ctx := vault.ContextWithProfileID(vault.ContextWithDB(context.Background(), db.DB), "p1")
	var turns []monomind.ExecOptions
	agent.FakeTurnsForTest(t, func(o monomind.ExecOptions) (*monomind.TurnResult, error) {
		turns = append(turns, o)
		if strings.Contains(o.Prompt, "my name is Ada") && strings.Contains(o.Prompt, "What is my name") {
			return &monomind.TurnResult{ResultText: "Ada"}, nil
		}
		return &monomind.TurnResult{ResultText: "Hello"}, nil
	})
	store := workflow.NewSQLiteWorkflowStore(db.DB)
	reg := noderegistry.Build(db.DB)
	wf := &workflow.Workflow{Name: "chat", ProfileID: "p1", Nodes: []workflow.WorkflowNode{
		{ID: "t", Type: "trigger.manual", Name: "Start"},
		{ID: "a", Type: "agent.ask", Name: "Ask", Config: map[string]interface{}{"prompt": "{{ $json.chatInput }}"}},
		{ID: "m", Type: "ai.model", Name: "Chat Model", Config: map[string]interface{}{"runtime": "opencode", "model": "free-model"}},
		{ID: "mem", Type: "ai.memory", Name: "Simple Memory", Config: map[string]interface{}{"session_key": "{{ $json.sessionId }}", "window": float64(10)}},
	}, Connections: []workflow.WorkflowConnection{
		{SourceNodeID: "t", SourceHandle: "main", TargetNodeID: "a", TargetHandle: "main"},
		{SourceNodeID: "m", SourceHandle: "ai_languageModel", TargetNodeID: "a", TargetHandle: "ai_languageModel"},
		{SourceNodeID: "mem", SourceHandle: "ai_memory", TargetNodeID: "a", TargetHandle: "ai_memory"},
	}}
	if rep := workflow.ValidateWorkflow(wf, workflow.ValidateOptions{}); !rep.Valid {
		t.Fatalf("validate: %+v", rep.Issues)
	}
	if err := store.CreateWorkflow(ctx, wf); err != nil {
		t.Fatal(err)
	}
	dag, err := workflow.BuildDAG(wf.Nodes, wf.Connections)
	if err != nil {
		t.Fatal(err)
	}
	run := func(text string) []workflow.WorkflowExecutionNode {
		t.Helper()
		e := &workflow.WorkflowExecution{WorkflowID: wf.ID, ProfileID: "p1", TriggerType: "manual",
			TriggerData: map[string]interface{}{"sessionId": "e2e", "chatInput": text}}
		if err := store.CreateExecution(ctx, e); err != nil {
			t.Fatal(err)
		}
		if err := workflow.RunExecution(ctx, e, wf, dag, reg, store, nil, workflow.NewExpressionEngine(), zerolog.Nop()); err != nil {
			t.Fatalf("run: %v", err)
		}
		nodes, err := store.GetExecutionNodeOutputs(ctx, e.ID)
		if err != nil {
			t.Fatal(err)
		}
		return nodes
	}
	run("Hi, my name is Ada.")
	nodes := run("What is my name?")
	for _, n := range nodes {
		if n.NodeID == "m" || n.NodeID == "mem" {
			t.Fatalf("sub-node %s ran as a step", n.NodeID)
		}
	}
	if len(turns) != 2 || turns[0].Runtime != "opencode" || turns[0].Model != "free-model" {
		t.Fatalf("turns %+v", turns)
	}
	if !strings.Contains(turns[1].Prompt, "User: Hi, my name is Ada.") {
		t.Fatalf("second turn has no history: %q", turns[1].Prompt)
	}
	msgs, err := aimemory.Recent(ctx, db.DB, "p1", "e2e", 0)
	if err != nil || len(msgs) != 4 || msgs[3].Content != "Ada" {
		t.Fatalf("session: %+v %v", msgs, err)
	}
}
