package daemon

import (
	"context"
	"testing"
	"time"

	"github.com/alicebob/miniredis/v2"
	"github.com/rs/zerolog"

	"github.com/monoes/mono-agent/internal/connections"
	"github.com/monoes/mono-agent/internal/storage"
	"github.com/monoes/mono-agent/internal/workflow"
)

// The daemon's engine starts a trigger.redis consumer when the workflow
// activates, resolving its credential_id from the workflow's profile the way
// a run does, and every published message starts a run.
func TestDaemonQueueTriggerFiresARun(t *testing.T) {
	mr := miniredis.RunT(t)
	db, err := storage.NewDatabase(t.TempDir() + "/q.db")
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	if err := db.ApplyMigrations(); err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	conn := &connections.Connection{Platform: "redis", Method: "api_key", Label: "local", ProfileID: "default",
		Data: map[string]interface{}{"connection_string": "redis://" + mr.Addr()}}
	if err := connections.NewStore(db.DB).Save(ctx, conn); err != nil {
		t.Fatal(err)
	}

	store := workflow.NewSQLiteWorkflowStore(db.DB)
	engine := workflow.NewWorkflowEngineWithStore(store, db.DB, nil, workflow.NewNodeTypeRegistry(),
		workflow.EngineConfig{WebhookAddr: "127.0.0.1:0", ProfileID: "default"}, zerolog.Nop())
	registerQueueTriggers(engine, t.Logf)
	if err := engine.Start(ctx); err != nil {
		t.Fatal(err)
	}
	defer engine.Stop() //nolint:errcheck

	wf := &workflow.Workflow{Name: "redis in", ProfileID: "default", Nodes: []workflow.WorkflowNode{{
		ID: "t", Type: "trigger.redis", Name: "Redis",
		Config: map[string]interface{}{"credential_id": conn.ID, "channels": "orders"},
	}}}
	for i := range wf.Nodes {
		if err := wf.Nodes[i].MarshalConfig(); err != nil {
			t.Fatal(err)
		}
	}
	if err := engine.CreateWorkflow(ctx, wf); err != nil {
		t.Fatal(err)
	}
	if err := engine.ActivateWorkflow(ctx, wf.ID); err != nil {
		t.Fatal(err)
	}

	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		mr.Publish("orders", `{"id":1}`) // until the consumer has subscribed
		execs, err := store.ListExecutions(ctx, wf.ID, 10)
		if err != nil {
			t.Fatal(err)
		}
		if len(execs) > 0 {
			return
		}
		time.Sleep(50 * time.Millisecond)
	}
	t.Fatal("no run started from a published message")
}
