package workflow_test

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"github.com/rs/zerolog"

	httpnodes "github.com/monoes/mono-agent/internal/nodes/http"
	"github.com/monoes/mono-agent/internal/workflow"
)

// finishStore records every SetExecutionNodeFinished call.
type finishStore struct {
	fakeStore
	mu       sync.Mutex
	finished []finishedRow
}

type finishedRow struct {
	status string
	items  []workflow.Item
	errMsg string
}

func (s *finishStore) SetExecutionNodeFinished(_ context.Context, _ string, status string, items []workflow.Item, errMsg string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.finished = append(s.finished, finishedRow{status, items, errMsg})
	return nil
}

// QA Q11: an HTTP 404 routed to an unwired "error" output used to fail with
// only "routed 1 item(s) to the 'error' output" and store no items, so the
// status and body were lost. The message now names the status and the
// failed row keeps the error item.
func TestRunExecution_HTTP404_NoErrorEdgeKeepsDetail(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusNotFound)
		_, _ = w.Write([]byte("{}"))
	}))
	defer srv.Close()

	reg := workflow.NewNodeTypeRegistry()
	reg.Register("http.request", func() workflow.NodeExecutor { return &httpnodes.RequestNode{} })
	wf := &workflow.Workflow{ID: "wf-404", Nodes: []workflow.WorkflowNode{
		{ID: "t", Type: "trigger.manual", Name: "T"},
		{ID: "h", Type: "http.request", Name: "Fetch", Config: map[string]interface{}{"url": srv.URL + "/missing", "method": "GET"}},
	}, Connections: []workflow.WorkflowConnection{
		{SourceNodeID: "t", SourceHandle: "main", TargetNodeID: "h", TargetHandle: "main"},
	}}
	dag, err := workflow.BuildDAG(wf.Nodes, wf.Connections)
	if err != nil {
		t.Fatalf("BuildDAG: %v", err)
	}
	store := &finishStore{}
	exec := &workflow.WorkflowExecution{ID: "e-404", WorkflowID: wf.ID, TriggerNodeID: "t"}
	err = workflow.RunExecution(context.Background(), exec, wf, dag, reg, store, nil, workflow.NewExpressionEngine(), zerolog.Nop())
	if err == nil {
		t.Fatal("RunExecution error = nil, want the 404 failure")
	}
	if !strings.Contains(err.Error(), "HTTP 404 Not Found: {}") {
		t.Errorf("error = %v, want it to say HTTP 404 Not Found: {}", err)
	}
	var failed *finishedRow
	for i := range store.finished {
		if store.finished[i].status == "FAILED" {
			failed = &store.finished[i]
		}
	}
	if failed == nil {
		t.Fatalf("no FAILED row recorded: %+v", store.finished)
	}
	if len(failed.items) != 1 {
		t.Fatalf("failed row items = %+v, want the one error item", failed.items)
	}
	if got := failed.items[0].JSON["status"]; got != 404 {
		t.Errorf("stored item status = %v, want 404", got)
	}
}
