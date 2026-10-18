package mcp

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/monoes/mono-agent/internal/orggrant"
	"github.com/monoes/mono-agent/internal/storage"
	"github.com/monoes/mono-agent/internal/workflow"
)

// slowDaemon stands in for `monoagentcli daemon`: on its own connection it
// claims the first QUEUED execution, marks it RUNNING, and finishes it with
// one output item after d (never, when d < 0). It looks for the execution
// until the test ends, however long a loaded machine takes to queue it.
func slowDaemon(t *testing.T, db *storage.Database, d time.Duration) {
	testCtx := t.Context()
	go func() {
		ctx := context.Background()
		ws := workflow.NewSQLiteWorkflowStore(db.DB)
		for ; testCtx.Err() == nil; time.Sleep(10 * time.Millisecond) {
			var id string
			if db.DB.QueryRow(`SELECT id FROM workflow_executions WHERE status = 'QUEUED' LIMIT 1`).Scan(&id) != nil {
				continue
			}
			_ = ws.SetExecutionStarted(ctx, id)
			_ = ws.UpdateExecutionStatus(ctx, id, "RUNNING", "")
			if d < 0 {
				return
			}
			time.Sleep(d)
			now := time.Now().UTC()
			_ = ws.CreateExecutionNode(ctx, &workflow.WorkflowExecutionNode{
				ExecutionID: id, NodeID: "n1", NodeName: "search", Status: "SUCCESS",
				OutputItems: []workflow.Item{{JSON: map[string]interface{}{"top": "relevant post"}}}, StartedAt: &now, FinishedAt: &now})
			_ = ws.SetExecutionFinished(ctx, id, "SUCCESS", "")
			return
		}
	}()
}

// #244: a run another connection finishes while the call waits is
// returned as soon as it is final, with its output.
func TestGrantWaitSeesARunAnotherConnectionFinishes(t *testing.T) {
	f := newGrantFixture(t, orggrant.Tool{Wait: true, Timeout: 600})
	liveHeartbeat(t)
	slowDaemon(t, f.db, 400*time.Millisecond)
	start := time.Now()
	out, err := f.server.callGrantTool(context.Background(), "automation_publish", json.RawMessage(`{"keywords":"mcp"}`))
	if err != nil {
		t.Fatal(err)
	}
	// Returned by the run's end, not by the tool's 600s limit; the bound is
	// that far from both, so a loaded machine cannot fail it.
	if took := time.Since(start); took > time.Minute {
		t.Fatalf("call took %s", took)
	}
	if !strings.Contains(out, `"status": "success"`) || !strings.Contains(out, "relevant post") {
		t.Fatalf("result = %s", out)
	}
}

// #244: a piped client closes stdin, and Serve cuts in-flight calls off
// postEOFGrace later. The result said "still running after 600s" after
// 3 seconds; it must say what happened.
func TestGrantWaitCutOffByClosedStdinSaysSo(t *testing.T) {
	f := newGrantFixture(t, orggrant.Tool{Wait: true, Timeout: 600})
	liveHeartbeat(t)
	slowDaemon(t, f.db, -1)
	start := time.Now()
	resps := serveLines(t, f.server, callToolReq(1, "automation_publish", map[string]interface{}{"keywords": "mcp"}))
	if took := time.Since(start); took > postEOFGrace+time.Minute { // cut off at the grace, not at 600s
		t.Fatalf("call took %s", took)
	}
	text := string(resps[0]["result"])
	if !strings.Contains(text, "client closed the connection") || strings.Contains(text, "600s") || !strings.Contains(text, `\"status\": \"running\"`) {
		t.Fatalf("result = %s", text)
	}
}

// A real timeout reports the time waited and the tool's limit.
func TestGrantWaitTimeoutNote(t *testing.T) {
	f := newGrantFixture(t, orggrant.Tool{Wait: true, Timeout: 1})
	liveHeartbeat(t)
	slowDaemon(t, f.db, -1)
	out, err := f.server.callGrantTool(context.Background(), "automation_publish", json.RawMessage(`{}`))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out, "still running after 1s (the tool waits up to 1s)") {
		t.Fatalf("result = %s", out)
	}
}

// The last read after the wait ends still returns a run that finished.
func TestGrantWaitFinalReadReturnsAFinishedRun(t *testing.T) {
	f := newGrantFixture(t, orggrant.Tool{Wait: true, Timeout: 600})
	liveHeartbeat(t)
	fakeDaemon(t, f.db, map[string]interface{}{"top": "done"})
	oldPoll := grantPollInterval
	grantPollInterval = time.Hour // only the first poll and the final read run
	t.Cleanup(func() { grantPollInterval = oldPoll })
	ctx, cancel := context.WithCancel(context.Background())
	go func() {
		for i := 0; i < 500; i++ {
			var st string
			if f.db.DB.QueryRow(`SELECT status FROM workflow_executions LIMIT 1`).Scan(&st) == nil && st == "SUCCESS" {
				break
			}
			time.Sleep(10 * time.Millisecond)
		}
		cancel()
	}()
	out, err := f.server.callGrantTool(ctx, "automation_publish", json.RawMessage(`{}`))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out, `"status": "success"`) || !strings.Contains(out, "done") {
		t.Fatalf("result = %s", out)
	}
}
