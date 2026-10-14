package daemon

import (
	"context"
	"database/sql"
	"strings"
	"testing"
	"time"

	"github.com/monoes/mono-agent/internal/cli/jevcli"
	"github.com/monoes/mono-agent/internal/monomind"
	"github.com/monoes/mono-agent/internal/testdb"
	"github.com/monoes/mono-agent/internal/workflow"
)

type memSyncCall struct {
	profile string
	nodes   []monomind.KGNode
	edges   []monomind.KGEdge
}

// TestRunMemorySweepOverStore runs the sweeper's source against a migrated
// database: finished runs are read since the watermark, the failed node's
// error is classified and redacted, and SyncToKnowledgeGraph (stubbed) gets
// the workflow's entities.
func TestRunMemorySweepOverStore(t *testing.T) {
	db := testdb.Open(t)
	ctx := context.Background()
	store := workflow.NewSQLiteWorkflowStore(db.DB)
	wf := &workflow.Workflow{Name: "Scrape feed", Description: "Reads the feed.", ProfileID: "p7"}
	if err := store.CreateWorkflow(ctx, wf); err != nil {
		t.Fatal(err)
	}
	if err := store.SaveWorkflowNodes(ctx, wf.ID, []workflow.WorkflowNode{
		{ID: "n1", Type: "trigger.manual", Name: "Start"},
		{ID: "n2", Type: "http.request", Name: "Fetch"},
	}); err != nil {
		t.Fatal(err)
	}
	run := func(status, nodeErr string) {
		t.Helper()
		e := &workflow.WorkflowExecution{WorkflowID: wf.ID, ProfileID: "p7", TriggerType: "manual"}
		if err := store.CreateExecution(ctx, e); err != nil {
			t.Fatal(err)
		}
		if nodeErr != "" {
			en := &workflow.WorkflowExecutionNode{ExecutionID: e.ID, NodeID: "n2", NodeName: "Fetch", Status: "RUNNING"}
			if err := store.CreateExecutionNode(ctx, en); err != nil {
				t.Fatal(err)
			}
			if err := store.SetExecutionNodeFinished(ctx, en.ID, "FAILED", nil, nodeErr); err != nil {
				t.Fatal(err)
			}
		}
		if err := store.SetExecutionFinished(ctx, e.ID, status, nodeErr); err != nil {
			t.Fatal(err)
		}
		time.Sleep(5 * time.Millisecond) // distinct created_at ordering
	}
	run("SUCCESS", "")
	secret := "Bearer " + "abcDEF123456789xyz"
	run("FAILED", "HTTP 503 Service Unavailable from https://api.example.com/x?sig=zz9 ("+secret+")")

	var calls []memSyncCall
	m := &monomind.RunMemory{
		DB:     db.DB,
		Source: newRunMemorySource(db.DB, store),
		Redact: jevcli.RedactRetryError,
		Sync: func(_ context.Context, _ *sql.DB, pid string, n []monomind.KGNode, e []monomind.KGEdge, _ string) error {
			calls = append(calls, memSyncCall{pid, n, e})
			return nil
		},
	}
	if err := m.Sweep(ctx); err != nil {
		t.Fatal(err)
	}
	if len(calls) != 1 || calls[0].profile != "p7" {
		t.Fatalf("sync calls %+v", calls)
	}
	var outcome, wfNode *monomind.KGNode
	for i, n := range calls[0].nodes {
		switch n.Name {
		case "run outcome:Scrape feed":
			outcome = &calls[0].nodes[i]
		case "workflow:Scrape feed":
			wfNode = &calls[0].nodes[i]
		}
	}
	if wfNode == nil || !strings.Contains(wfNode.Description, "http.request, trigger.manual") {
		t.Fatalf("workflow node %+v", wfNode)
	}
	if outcome == nil {
		t.Fatalf("no outcome node in %+v", calls[0].nodes)
	}
	for _, want := range []string{"last 2 runs 1 success, 1 failed: upstream_5xx ×1", "at node Fetch", "latest run failed"} {
		if !strings.Contains(outcome.Description, want) {
			t.Errorf("outcome missing %q: %s", want, outcome.Description)
		}
	}
	if strings.Contains(outcome.Description, "abcDEF123456789xyz") || strings.Contains(outcome.Description, "zz9") {
		t.Errorf("secret leaked: %s", outcome.Description)
	}
	found := false
	for _, e := range calls[0].edges {
		if e.Source == "workflow:Scrape feed" && e.Relation == "fails_with" && e.Target == "failure class:upstream_5xx" {
			found = true
		}
	}
	if !found {
		t.Errorf("fails_with edge missing: %+v", calls[0].edges)
	}

	// Nothing new since the watermark and no outcome change: no second sync.
	if err := m.Sweep(ctx); err != nil {
		t.Fatal(err)
	}
	if len(calls) != 1 {
		t.Fatalf("re-ingested without change: %d", len(calls))
	}

	// The engine-hook path: a new failure class makes the finished
	// execution's workflow re-ingest (the throttle interval is lifted).
	m.Now = func() time.Time { return time.Now().Add(time.Hour) }
	runMemories.Store(db.DB, m)
	t.Cleanup(func() { runMemories.Delete(db.DB) })
	run("FAILED", "waiting for selector .title")
	var last string
	if err := db.DB.QueryRow(`SELECT id FROM workflow_executions ORDER BY created_at DESC LIMIT 1`).Scan(&last); err != nil {
		t.Fatal(err)
	}
	runMemoryExecutionFinished(ctx, db.DB, last, "FAILED")
	if len(calls) != 2 {
		t.Fatalf("hook did not ingest the new class: %d calls", len(calls))
	}
}

func TestRunMemoryDaemonInvocation(t *testing.T) {
	cases := []struct {
		args []string
		ok   bool
		db   string
	}{
		{[]string{"daemon"}, true, "~/.monoagent/monoagent.db"},
		{[]string{"--profile", "x", "daemon", "--api=false"}, true, "~/.monoagent/monoagent.db"},
		{[]string{"--db-path", "/tmp/a.db", "daemon"}, true, "/tmp/a.db"},
		{[]string{"daemon", "--db-path=/tmp/b.db", "--api-addr", "127.0.0.1:1"}, true, "/tmp/b.db"},
		{[]string{"daemon", "install"}, false, ""},
		{[]string{"workflow", "run", "x"}, false, ""},
		{[]string{"-test.run=Foo"}, false, ""},
	}
	for _, c := range cases {
		db, ok := daemonInvocation(c.args)
		if ok != c.ok || (ok && db != c.db) {
			t.Errorf("daemonInvocation(%v) = %q, %v", c.args, db, ok)
		}
	}
}
