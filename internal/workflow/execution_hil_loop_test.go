package workflow_test

import (
	"context"
	"database/sql"
	"errors"
	"testing"

	"github.com/rs/zerolog"
	_ "modernc.org/sqlite"

	"github.com/monoes/mono-agent/internal/nodes/control"
	"github.com/monoes/mono-agent/internal/vault"
	"github.com/monoes/mono-agent/internal/workflow"
)

func newHILLoopDB(t *testing.T) *sql.DB {
	t.Helper()
	db, err := sql.Open("sqlite", ":memory:")
	if err != nil {
		t.Fatal(err)
	}
	db.SetMaxOpenConns(1)
	t.Cleanup(func() { db.Close() })
	if _, err := db.Exec(`CREATE TABLE hil_pending (
		id TEXT PRIMARY KEY, execution_id TEXT NOT NULL, workflow_id TEXT NOT NULL,
		node_id TEXT NOT NULL, node_name TEXT NOT NULL, status TEXT NOT NULL DEFAULT 'pending',
		readonly_data TEXT NOT NULL DEFAULT '{}', editable_data TEXT NOT NULL DEFAULT '{}',
		edited_data TEXT NOT NULL DEFAULT '{}', node_config TEXT NOT NULL DEFAULT '{}',
		profile_id TEXT NOT NULL DEFAULT 'default',
		created_at TIMESTAMP DEFAULT CURRENT_TIMESTAMP, updated_at TIMESTAMP DEFAULT CURRENT_TIMESTAMP)`); err != nil {
		t.Fatal(err)
	}
	return db
}

func pendingHIL(t *testing.T, db *sql.DB) int {
	t.Helper()
	var n int
	if err := db.QueryRow(`SELECT COUNT(*) FROM hil_pending WHERE status = 'pending'`).Scan(&n); err != nil {
		t.Fatal(err)
	}
	return n
}

// Human-in-Loop inside a loop body asks again on every iteration: the
// second batch must not reuse the first batch's approvals.
func TestLoop_HumanInLoopAsksEachIteration(t *testing.T) {
	db := newHILLoopDB(t)
	done := newRec()
	reg := workflow.NewNodeTypeRegistry()
	reg.Register(workflow.LoopNodeType, func() workflow.NodeExecutor { return &control.SplitInBatchesNode{} })
	reg.Register("test.emit", func() workflow.NodeExecutor { return emitNode{n: 4} })
	reg.Register("core.human_in_loop", func() workflow.NodeExecutor { return &control.HumanInLoopNode{} })
	reg.Register("test.done", func() workflow.NodeExecutor { return doneRec{done} })
	wf := &workflow.Workflow{ID: "wf", Nodes: []workflow.WorkflowNode{
		{ID: "t", Type: "trigger.manual", Name: "T"},
		{ID: "f", Type: "test.emit", Name: "F"},
		{ID: "s", Type: workflow.LoopNodeType, Name: "S", Config: map[string]interface{}{"batch_size": 2}},
		{ID: "h", Type: "core.human_in_loop", Name: "Review"},
		{ID: "d", Type: "test.done", Name: "D"},
	}, Connections: []workflow.WorkflowConnection{
		conn("t", "main", "f"), conn("f", "main", "s"),
		conn("s", "loop", "h"), conn("h", "main", "s"),
		conn("s", "done", "d"),
	}}
	dag, err := workflow.BuildDAG(wf.Nodes, wf.Connections)
	if err != nil {
		t.Fatal(err)
	}
	store := &recStore{}
	run := func(resume string) error {
		exec := &workflow.WorkflowExecution{ID: "e", WorkflowID: wf.ID, TriggerData: map[string]interface{}{}, ResumeState: resume}
		ctx := vault.ContextWithDB(context.Background(), db)
		return workflow.RunExecution(ctx, exec, wf, dag, reg, store, nil, workflow.NewExpressionEngine(), zerolog.Nop())
	}

	// Iteration 1 asks about its 2 items.
	if err := run(""); !errors.Is(err, workflow.ErrExecutionPaused) {
		t.Fatalf("first run: %v, want paused", err)
	}
	if n := pendingHIL(t, db); n != 2 {
		t.Fatalf("pending after iteration 1 = %d, want 2", n)
	}
	if _, err := db.Exec(`UPDATE hil_pending SET status = 'approved', edited_data = '{"note":"first"}'`); err != nil {
		t.Fatal(err)
	}

	// Iteration 2 asks again about its own 2 items.
	if err := run(store.waiting); !errors.Is(err, workflow.ErrExecutionPaused) {
		t.Fatalf("resume after iteration 1: %v, want a new pause for iteration 2", err)
	}
	if n := pendingHIL(t, db); n != 2 {
		t.Fatalf("pending after iteration 2 = %d, want 2 new rows", n)
	}
	if _, err := db.Exec(`UPDATE hil_pending SET status = 'approved' WHERE status = 'pending'`); err != nil {
		t.Fatal(err)
	}

	if err := run(store.waiting); err != nil {
		t.Fatalf("final resume: %v", err)
	}
	if got := done.snapshot(); !eqInts(got, []int{4}) {
		t.Fatalf("done calls = %v, want [4]", got)
	}
	// Only iteration 1's items carry iteration 1's edits.
	for i, it := range (*done.items)[:4] {
		if want := i < 2; (it.JSON["note"] == "first") != want {
			t.Errorf("done item %d = %v: iteration 1 edit applied = %v, want %v", i, it.JSON, !want, want)
		}
	}
}
