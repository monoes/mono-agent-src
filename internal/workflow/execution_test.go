package workflow

import (
	"bytes"
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/rs/zerolog"

	"github.com/monoes/mono-agent/internal/vault"
)

// stubStore is a no-op WorkflowStore that only records the execution-node rows
// written during a run, so tests can assert which nodes ran and with what input.
type stubStore struct {
	mu           sync.Mutex
	nodes        []*WorkflowExecutionNode
	waitingState string // captured by SetExecutionWaiting

	// workflowToReturn, if set, is what GetWorkflow returns for any ID.
	workflowToReturn *Workflow
	// createdExecs captures every exec passed to CreateExecution, for
	// asserting what the engine actually persists (e.g. ProfileID).
	createdExecs []*WorkflowExecution
}

func (s *stubStore) CreateExecutionNode(ctx context.Context, en *WorkflowExecutionNode) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	cp := *en
	s.nodes = append(s.nodes, &cp)
	return nil
}

func (s *stubStore) SetExecutionNodeFinished(ctx context.Context, id string, status string, outputItems []Item, errMsg string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, n := range s.nodes {
		if n.ID == id {
			n.Status = status
			n.ErrorMessage = errMsg
			n.OutputItems = outputItems
		}
	}
	return nil
}

// nodeRecord returns the recorded execution-node row for nodeID, or nil.
func (s *stubStore) nodeRecord(nodeID string) *WorkflowExecutionNode {
	s.mu.Lock()
	defer s.mu.Unlock()
	var rec *WorkflowExecutionNode
	for _, n := range s.nodes {
		if n.NodeID == nodeID {
			rec = n
		}
	}
	return rec
}

func (s *stubStore) CreateWorkflow(context.Context, *Workflow) error { return nil }
func (s *stubStore) GetWorkflow(context.Context, string) (*Workflow, error) {
	return s.workflowToReturn, nil
}
func (s *stubStore) ListWorkflows(context.Context, string) ([]Workflow, error) { return nil, nil }
func (s *stubStore) UpdateWorkflow(context.Context, *Workflow) error           { return nil }
func (s *stubStore) DeleteWorkflow(context.Context, string) error              { return nil }
func (s *stubStore) SetWorkflowActive(context.Context, string, bool) error     { return nil }
func (s *stubStore) SaveWorkflowNodes(context.Context, string, []WorkflowNode) error {
	return nil
}
func (s *stubStore) SaveWorkflowConnections(context.Context, string, []WorkflowConnection) error {
	return nil
}
func (s *stubStore) CreateExecution(ctx context.Context, e *WorkflowExecution) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if e.ID == "" {
		e.ID = fmt.Sprintf("exec-%d", len(s.createdExecs)+1)
	}
	cp := *e
	s.createdExecs = append(s.createdExecs, &cp)
	return nil
}
func (s *stubStore) GetExecution(context.Context, string) (*WorkflowExecution, error) {
	return nil, nil
}
func (s *stubStore) GetExecutionStatus(context.Context, string) (string, error) { return "", nil }
func (s *stubStore) ListExecutions(context.Context, string, int) ([]WorkflowExecution, error) {
	return nil, nil
}
func (s *stubStore) UpdateExecutionStatus(context.Context, string, string, string) error { return nil }
func (s *stubStore) SetExecutionStarted(context.Context, string) error                   { return nil }
func (s *stubStore) SetExecutionFinished(context.Context, string, string, string) error  { return nil }
func (s *stubStore) UpdateExecutionNode(context.Context, *WorkflowExecutionNode) error   { return nil }
func (s *stubStore) RecoverStaleExecutions(context.Context) error                        { return nil }
func (s *stubStore) ReapStaleRunningExecutions(context.Context, time.Time) error         { return nil }
func (s *stubStore) CancelQueuedExecution(context.Context, string) (bool, error)         { return false, nil }
func (s *stubStore) SetExecutionWaiting(_ context.Context, _ string, state string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.waitingState = state
	return nil
}
func (s *stubStore) ListResumableExecutions(context.Context) ([]string, error)    { return nil, nil }
func (s *stubStore) ResumeWaitingExecution(context.Context, string) (bool, error) { return true, nil }
func (s *stubStore) ListAdoptableExecutions(context.Context) ([]string, error)    { return nil, nil }
func (s *stubStore) ClaimQueuedExecution(context.Context, string) (bool, error)   { return false, nil }
func (s *stubStore) PruneExecutions(context.Context, string, int) error           { return nil }
func (s *stubStore) RawDB() *sql.DB                                               { return nil }

// fakeFailNode always fails, to exercise on_error handling.
type fakeFailNode struct{}

func (fakeFailNode) Type() string { return "test.fail" }
func (fakeFailNode) Execute(context.Context, NodeInput, map[string]interface{}) ([]NodeOutput, error) {
	return nil, errTestNodeFail
}

var errTestNodeFail = fmt.Errorf("test node failure")

// TestRunExecution_PartialFailure verifies a node failing under
// on_error=continue makes RunExecution return a *PartialFailureError (which the
// engine maps to SUCCESS_WITH_ERRORS) rather than nil (a misleading success).
func TestRunExecution_PartialFailure(t *testing.T) {
	reg := NewNodeTypeRegistry()
	reg.Register("test.fail", func() NodeExecutor { return fakeFailNode{} })

	wf := &Workflow{
		ID:   "wf-pf",
		Name: "partial",
		Nodes: []WorkflowNode{
			{ID: "t1", WorkflowID: "wf-pf", Type: "trigger.manual", Name: "Trigger"},
			{ID: "n1", WorkflowID: "wf-pf", Type: "test.fail", Name: "Flaky", Config: map[string]interface{}{"on_error": "continue"}},
		},
		Connections: []WorkflowConnection{
			{SourceNodeID: "t1", SourceHandle: "main", TargetNodeID: "n1", TargetHandle: "main"},
		},
	}
	dag, err := BuildDAG(wf.Nodes, wf.Connections)
	if err != nil {
		t.Fatalf("BuildDAG: %v", err)
	}
	exec := &WorkflowExecution{ID: "e-pf", WorkflowID: "wf-pf", TriggerNodeID: "t1"}
	err = RunExecution(context.Background(), exec, wf, dag, reg, &stubStore{}, nil, NewExpressionEngine(), zerolog.Nop())

	var pf *PartialFailureError
	if !errors.As(err, &pf) {
		t.Fatalf("RunExecution error = %v, want *PartialFailureError", err)
	}
	if len(pf.Nodes) != 1 || pf.Nodes[0] != "Flaky" {
		t.Errorf("PartialFailureError.Nodes = %v, want [Flaky]", pf.Nodes)
	}
}

// countNode counts its executions and passes items through — used to assert a
// node does NOT re-run on resume.
type countNode struct{ count *int }

func (c countNode) Type() string { return "test.count" }
func (c countNode) Execute(_ context.Context, in NodeInput, _ map[string]interface{}) ([]NodeOutput, error) {
	*c.count++
	return []NodeOutput{{Handle: "main", Items: in.Items}}, nil
}

// errorEmitNode succeeds but routes one item to the "error" handle — the
// failure convention of http.request (connection refused, cap breach) and
// system.execute_command (non-zero exit).
type errorEmitNode struct{}

func (errorEmitNode) Type() string { return "test.erroremit" }
func (errorEmitNode) Execute(_ context.Context, _ NodeInput, _ map[string]interface{}) ([]NodeOutput, error) {
	return []NodeOutput{{
		Handle: "error",
		Items:  []Item{NewItem(map[string]interface{}{"error": "dial tcp: connection refused"})},
	}}, nil
}

// captureItemsNode records the items it receives and passes them through.
type captureItemsNode struct{ got *[]Item }

func (captureItemsNode) Type() string { return "test.capture.items" }
func (c captureItemsNode) Execute(_ context.Context, in NodeInput, _ map[string]interface{}) ([]NodeOutput, error) {
	*c.got = append(*c.got, in.Items...)
	return []NodeOutput{{Handle: "main", Items: in.Items}}, nil
}

// TestRunExecution_ErrorHandleUnwiredFailsNode is the regression guard for
// V3-F3/F7: a node routing failures to its "error" output with NO edge wired
// from that handle used to have its error items silently dropped while
// reporting SUCCESS with 0 items. It must now FAIL with the routed error,
// and the execution must reflect it.
func TestRunExecution_ErrorHandleUnwiredFailsNode(t *testing.T) {
	reg := NewNodeTypeRegistry()
	reg.Register("test.erroremit", func() NodeExecutor { return errorEmitNode{} })

	wf := &Workflow{
		ID:   "wf-err-unwired",
		Name: "err-unwired",
		Nodes: []WorkflowNode{
			{ID: "t", WorkflowID: "wf-err-unwired", Type: "trigger.manual", Name: "T"},
			{ID: "emit", WorkflowID: "wf-err-unwired", Type: "test.erroremit", Name: "Emit"},
		},
		Connections: []WorkflowConnection{
			{SourceNodeID: "t", SourceHandle: "main", TargetNodeID: "emit", TargetHandle: "main"},
		},
	}
	dag, err := BuildDAG(wf.Nodes, wf.Connections)
	if err != nil {
		t.Fatalf("BuildDAG: %v", err)
	}
	store := &stubStore{}
	exec := &WorkflowExecution{ID: "e-err-unwired", WorkflowID: wf.ID, TriggerNodeID: "t"}
	err = RunExecution(context.Background(), exec, wf, dag, reg, store, nil, NewExpressionEngine(), zerolog.Nop())

	if err == nil {
		t.Fatal("RunExecution error = nil, want failure from the dropped error output")
	}
	if !strings.Contains(err.Error(), "dial tcp: connection refused") {
		t.Errorf("error should surface the routed failure, got: %v", err)
	}
	rec := store.nodeRecord("emit")
	if rec == nil {
		t.Fatal("no execution-node record for the emitting node")
	}
	if rec.Status != "FAILED" {
		t.Errorf("emitting node status = %s, want FAILED (was silently SUCCESS)", rec.Status)
	}
	if rec.ErrorMessage == "" {
		t.Error("emitting node record carries no error message")
	}
}

// TestRunExecution_ErrorHandleWiredRoutesNotFailed preserves the wired
// behaviour: with an edge from the "error" handle, the error item routes to
// the successor and the node does NOT fail.
func TestRunExecution_ErrorHandleWiredRoutesNotFailed(t *testing.T) {
	reg := NewNodeTypeRegistry()
	reg.Register("test.erroremit", func() NodeExecutor { return errorEmitNode{} })
	var captured []Item
	reg.Register("test.capture.items", func() NodeExecutor { return captureItemsNode{got: &captured} })

	wf := &Workflow{
		ID:   "wf-err-wired",
		Name: "err-wired",
		Nodes: []WorkflowNode{
			{ID: "t", WorkflowID: "wf-err-wired", Type: "trigger.manual", Name: "T"},
			{ID: "emit", WorkflowID: "wf-err-wired", Type: "test.erroremit", Name: "Emit"},
			{ID: "cap", WorkflowID: "wf-err-wired", Type: "test.capture.items", Name: "Cap"},
		},
		Connections: []WorkflowConnection{
			{SourceNodeID: "t", SourceHandle: "main", TargetNodeID: "emit", TargetHandle: "main"},
			{SourceNodeID: "emit", SourceHandle: "error", TargetNodeID: "cap", TargetHandle: "main"},
		},
	}
	dag, err := BuildDAG(wf.Nodes, wf.Connections)
	if err != nil {
		t.Fatalf("BuildDAG: %v", err)
	}
	store := &stubStore{}
	exec := &WorkflowExecution{ID: "e-err-wired", WorkflowID: wf.ID, TriggerNodeID: "t"}
	if err := RunExecution(context.Background(), exec, wf, dag, reg, store, nil, NewExpressionEngine(), zerolog.Nop()); err != nil {
		t.Fatalf("RunExecution: %v (wired error edge must not fail the run)", err)
	}
	if len(captured) != 1 {
		t.Fatalf("error-edge successor captured %d items, want 1", len(captured))
	}
	if msg, _ := captured[0].JSON["error"].(string); msg == "" {
		t.Errorf("routed error item has no error field: %+v", captured[0].JSON)
	}
	if rec := store.nodeRecord("emit"); rec != nil && rec.Status == "FAILED" {
		t.Errorf("emitting node status = FAILED with a wired error edge; want not-failed (got %s)", rec.Status)
	}
}

// TestRunExecution_ErrorHandleUnwiredContinuePolicyKept preserves the
// explicit opt-out: with on_error=continue and an unwired error handle the
// dropped error output stays non-fatal.
func TestRunExecution_ErrorHandleUnwiredContinuePolicyKept(t *testing.T) {
	reg := NewNodeTypeRegistry()
	reg.Register("test.erroremit", func() NodeExecutor { return errorEmitNode{} })

	wf := &Workflow{
		ID:   "wf-err-continue",
		Name: "err-continue",
		Nodes: []WorkflowNode{
			{ID: "t", WorkflowID: "wf-err-continue", Type: "trigger.manual", Name: "T"},
			{ID: "emit", WorkflowID: "wf-err-continue", Type: "test.erroremit", Name: "Emit", Config: map[string]interface{}{"on_error": "continue"}},
		},
		Connections: []WorkflowConnection{
			{SourceNodeID: "t", SourceHandle: "main", TargetNodeID: "emit", TargetHandle: "main"},
		},
	}
	dag, err := BuildDAG(wf.Nodes, wf.Connections)
	if err != nil {
		t.Fatalf("BuildDAG: %v", err)
	}
	store := &stubStore{}
	exec := &WorkflowExecution{ID: "e-err-continue", WorkflowID: wf.ID, TriggerNodeID: "t"}
	if err := RunExecution(context.Background(), exec, wf, dag, reg, store, nil, NewExpressionEngine(), zerolog.Nop()); err != nil {
		t.Fatalf("RunExecution: %v (explicit on_error=continue must tolerate the unwired error output)", err)
	}
	if rec := store.nodeRecord("emit"); rec != nil && rec.Status == "FAILED" {
		t.Errorf("emitting node FAILED under on_error=continue; want preserved non-fatal behaviour")
	}
}

// TestRunExecution_ExecutorErrorBranchUnwiredFails covers the other half of
// V3-F3: a node whose Execute returns an error under on_error=error_branch
// with no edge wired from the error handle used to have the failure
// discarded as non-fatal; it must now fail the run.
func TestRunExecution_ExecutorErrorBranchUnwiredFails(t *testing.T) {
	reg := NewNodeTypeRegistry()
	reg.Register("test.fail", func() NodeExecutor { return fakeFailNode{} })

	wf := &Workflow{
		ID:   "wf-eb-unwired",
		Name: "eb-unwired",
		Nodes: []WorkflowNode{
			{ID: "t", WorkflowID: "wf-eb-unwired", Type: "trigger.manual", Name: "T"},
			{ID: "n1", WorkflowID: "wf-eb-unwired", Type: "test.fail", Name: "Flaky", Config: map[string]interface{}{"on_error": "error_branch"}},
		},
		Connections: []WorkflowConnection{
			{SourceNodeID: "t", SourceHandle: "main", TargetNodeID: "n1", TargetHandle: "main"},
		},
	}
	dag, err := BuildDAG(wf.Nodes, wf.Connections)
	if err != nil {
		t.Fatalf("BuildDAG: %v", err)
	}
	exec := &WorkflowExecution{ID: "e-eb-unwired", WorkflowID: wf.ID, TriggerNodeID: "t"}
	err = RunExecution(context.Background(), exec, wf, dag, reg, &stubStore{}, nil, NewExpressionEngine(), zerolog.Nop())
	if err == nil {
		t.Fatal("RunExecution error = nil, want failure (error_branch with no wired error edge must not discard)")
	}
	if !errors.Is(err, errTestNodeFail) {
		t.Errorf("error should wrap the node failure, got: %v", err)
	}
}

// TestRunExecution_CredentialMissLogsProfile guards the credential-miss log:
// resolution is profile-scoped (connections GetOrResolve), so a full miss
// must log the execution's profile for diagnosability.
func TestRunExecution_CredentialMissLogsProfile(t *testing.T) {
	var buf bytes.Buffer
	logger := zerolog.New(&buf)

	reg := NewNodeTypeRegistry()
	reg.Register("test.count", func() NodeExecutor { return countNode{new(int)} })

	wf := &Workflow{
		ID:   "wf-credmiss",
		Name: "credmiss",
		Nodes: []WorkflowNode{
			{ID: "t", WorkflowID: "wf-credmiss", Type: "trigger.manual", Name: "T"},
			{ID: "n", WorkflowID: "wf-credmiss", Type: "test.count", Name: "N", Config: map[string]interface{}{"credential_id": "wc_missing"}},
		},
		Connections: []WorkflowConnection{
			{SourceNodeID: "t", SourceHandle: "main", TargetNodeID: "n", TargetHandle: "main"},
		},
	}
	dag, err := BuildDAG(wf.Nodes, wf.Connections)
	if err != nil {
		t.Fatalf("BuildDAG: %v", err)
	}
	ctx := vault.ContextWithProfileID(context.Background(), "profile-x")
	exec := &WorkflowExecution{ID: "e-credmiss", WorkflowID: wf.ID, TriggerNodeID: "t"}
	if err := RunExecution(ctx, exec, wf, dag, reg, &stubStore{}, nil, NewExpressionEngine(), logger); err != nil {
		t.Fatalf("RunExecution: %v", err)
	}
	if !strings.Contains(buf.String(), "profile-x") {
		t.Errorf("credential miss log should include the execution's profile, got: %s", buf.String())
	}
	if !strings.Contains(buf.String(), "wc_missing") {
		t.Errorf("credential miss log should include the credential id, got: %s", buf.String())
	}
}

// gateNode pauses (ErrNodePaused) until *open is true, then passes items through
// — a stand-in for a Human-in-Loop node awaiting approval.
type gateNode struct{ open *bool }

func (g gateNode) Type() string { return "test.gate" }
func (g gateNode) Execute(_ context.Context, in NodeInput, _ map[string]interface{}) ([]NodeOutput, error) {
	if !*g.open {
		return nil, ErrNodePaused
	}
	return []NodeOutput{{Handle: "main", Items: in.Items}}, nil
}

// TestRunExecution_PauseAndResume verifies the HIL restart-resume core: a node
// pausing suspends the run (ErrExecutionPaused) with captured state, and on
// resume the pre-pause node does NOT re-execute while the post-pause node runs.
func TestRunExecution_PauseAndResume(t *testing.T) {
	preCount, postCount := 0, 0
	gateOpen := false

	reg := NewNodeTypeRegistry()
	reg.Register("test.count.pre", func() NodeExecutor { return countNode{&preCount} })
	reg.Register("test.gate", func() NodeExecutor { return gateNode{&gateOpen} })
	reg.Register("test.count.post", func() NodeExecutor { return countNode{&postCount} })

	wf := &Workflow{
		ID:   "wf-pr",
		Name: "pause-resume",
		Nodes: []WorkflowNode{
			{ID: "t", WorkflowID: "wf-pr", Type: "trigger.manual", Name: "T"},
			{ID: "pre", WorkflowID: "wf-pr", Type: "test.count.pre", Name: "Pre"},
			{ID: "gate", WorkflowID: "wf-pr", Type: "test.gate", Name: "Gate"},
			{ID: "post", WorkflowID: "wf-pr", Type: "test.count.post", Name: "Post"},
		},
		Connections: []WorkflowConnection{
			{SourceNodeID: "t", SourceHandle: "main", TargetNodeID: "pre", TargetHandle: "main"},
			{SourceNodeID: "pre", SourceHandle: "main", TargetNodeID: "gate", TargetHandle: "main"},
			{SourceNodeID: "gate", SourceHandle: "main", TargetNodeID: "post", TargetHandle: "main"},
		},
	}
	dag, err := BuildDAG(wf.Nodes, wf.Connections)
	if err != nil {
		t.Fatalf("BuildDAG: %v", err)
	}

	// First run: gate closed → pause.
	store := &stubStore{}
	exec := &WorkflowExecution{ID: "e", WorkflowID: "wf-pr", TriggerNodeID: "t", TriggerData: map[string]interface{}{"x": 1}}
	err = RunExecution(context.Background(), exec, wf, dag, reg, store, nil, NewExpressionEngine(), zerolog.Nop())
	if !errors.Is(err, ErrExecutionPaused) {
		t.Fatalf("first run error = %v, want ErrExecutionPaused", err)
	}
	if preCount != 1 {
		t.Fatalf("pre node ran %d times before pause, want 1", preCount)
	}
	if postCount != 0 {
		t.Fatalf("post node ran %d times before resume, want 0", postCount)
	}
	if store.waitingState == "" {
		t.Fatal("no resume state was captured on pause")
	}

	// Resume: gate open, seeded with the captured state.
	gateOpen = true
	exec2 := &WorkflowExecution{ID: "e", WorkflowID: "wf-pr", TriggerData: map[string]interface{}{"x": 1}, ResumeState: store.waitingState}
	if err := RunExecution(context.Background(), exec2, wf, dag, reg, store, nil, NewExpressionEngine(), zerolog.Nop()); err != nil {
		t.Fatalf("resume run error: %v", err)
	}
	if preCount != 1 {
		t.Fatalf("pre node re-ran on resume (count=%d) — side effects would double", preCount)
	}
	if postCount != 1 {
		t.Fatalf("post node ran %d times after resume, want 1", postCount)
	}
}

// TestRunExecution_OnlyFiredTriggerReceivesPayload is a regression test for the
// multi-trigger fan-out bug: a single trigger firing must only feed its own
// trigger node, not every trigger node in the workflow.
func TestRunExecution_OnlyFiredTriggerReceivesPayload(t *testing.T) {
	wf := &Workflow{
		ID:   "wf-1",
		Name: "multi-trigger",
		Nodes: []WorkflowNode{
			{ID: "t1", WorkflowID: "wf-1", Type: "trigger.webhook", Name: "Webhook"},
			{ID: "t2", WorkflowID: "wf-1", Type: "trigger.schedule", Name: "Schedule"},
		},
	}
	dag, err := BuildDAG(wf.Nodes, wf.Connections)
	if err != nil {
		t.Fatalf("BuildDAG: %v", err)
	}

	inputLen := func(store *stubStore) map[string]int {
		out := map[string]int{}
		for _, n := range store.nodes {
			out[n.NodeID] = len(n.InputItems)
		}
		return out
	}

	// Only t1 fired: t1 gets the payload item, t2 gets none.
	fired := &stubStore{}
	exec := &WorkflowExecution{
		ID:            "ex-1",
		WorkflowID:    "wf-1",
		TriggerNodeID: "t1",
		TriggerData:   map[string]interface{}{"src": "webhook"},
	}
	if err := RunExecution(context.Background(), exec, wf, dag, NewNodeTypeRegistry(), fired, nil, NewExpressionEngine(), zerolog.Nop()); err != nil {
		t.Fatalf("RunExecution: %v", err)
	}
	got := inputLen(fired)
	if got["t1"] != 1 {
		t.Errorf("fired trigger t1: got %d input items, want 1", got["t1"])
	}
	if got["t2"] != 0 {
		t.Errorf("non-fired trigger t2: got %d input items, want 0", got["t2"])
	}

	// Empty TriggerNodeID (manual/retry): legacy fan-out — every trigger fires.
	all := &stubStore{}
	execAll := &WorkflowExecution{
		ID:          "ex-2",
		WorkflowID:  "wf-1",
		TriggerData: map[string]interface{}{"src": "manual"},
	}
	if err := RunExecution(context.Background(), execAll, wf, dag, NewNodeTypeRegistry(), all, nil, NewExpressionEngine(), zerolog.Nop()); err != nil {
		t.Fatalf("RunExecution (manual): %v", err)
	}
	got = inputLen(all)
	if got["t1"] != 1 || got["t2"] != 1 {
		t.Errorf("manual run: both triggers should fire, got t1=%d t2=%d", got["t1"], got["t2"])
	}
}

func TestParsePerItemFieldSpec(t *testing.T) {
	cases := []struct {
		spec         string
		wantArrayKey string
		wantKey      string
	}{
		{"condition", "", "condition"},
		{"assignments[].value", "assignments", "value"},
	}
	for _, c := range cases {
		arrayKey, key := parsePerItemFieldSpec(c.spec)
		if arrayKey != c.wantArrayKey || key != c.wantKey {
			t.Errorf("parsePerItemFieldSpec(%q) = (%q, %q), want (%q, %q)", c.spec, arrayKey, key, c.wantArrayKey, c.wantKey)
		}
	}
}

// TestExtractRestorePerItemFields_TopLevel verifies a top-level field spec
// (e.g. "condition") survives a real ResolveConfig pass unresolved, while an
// unrelated field in the same config is resolved normally.
func TestExtractRestorePerItemFields_TopLevel(t *testing.T) {
	config := map[string]interface{}{
		"condition": "{{$json.status}}",
		"other":     "{{$json.name}}",
	}
	state := extractPerItemFields(config, []string{"condition"})

	if _, ok := config["condition"]; ok {
		t.Fatal("condition should have been removed from config before resolution")
	}
	if _, ok := config["other"]; !ok {
		t.Fatal("other should remain in config for normal resolution")
	}

	engine := NewExpressionEngine()
	resolvedConfig, err := engine.ResolveConfig(config, ExpressionContext{JSON: map[string]interface{}{"status": "open", "name": "alice"}})
	if err != nil {
		t.Fatalf("ResolveConfig: %v", err)
	}
	restorePerItemFields(resolvedConfig, state)

	if resolvedConfig["condition"] != "{{$json.status}}" {
		t.Errorf("condition = %v, want raw template preserved", resolvedConfig["condition"])
	}
	if resolvedConfig["other"] != "alice" {
		t.Errorf("other = %v, want resolved to \"alice\"", resolvedConfig["other"])
	}
}

// TestExtractRestorePerItemFields_NestedArray verifies an "arrayKey[].subKey"
// spec (core.set's "assignments[].value") protects only that sub-key of
// every element, while sibling keys in each element still resolve normally
// and element order/count survive the round trip.
func TestExtractRestorePerItemFields_NestedArray(t *testing.T) {
	config := map[string]interface{}{
		"assignments": []interface{}{
			map[string]interface{}{"field": "a", "value": "{{$json.x}}"},
			map[string]interface{}{"field": "b", "value": "{{$json.y}}"},
		},
	}
	state := extractPerItemFields(config, []string{"assignments[].value"})

	for i, raw := range config["assignments"].([]interface{}) {
		elem := raw.(map[string]interface{})
		if _, ok := elem["value"]; ok {
			t.Fatalf("assignments[%d].value should have been removed before resolution", i)
		}
		if _, ok := elem["field"]; !ok {
			t.Fatalf("assignments[%d].field should remain for normal resolution", i)
		}
	}

	engine := NewExpressionEngine()
	resolvedConfig, err := engine.ResolveConfig(config, ExpressionContext{JSON: map[string]interface{}{"x": 1, "y": 2}})
	if err != nil {
		t.Fatalf("ResolveConfig: %v", err)
	}
	restorePerItemFields(resolvedConfig, state)

	resolvedAssignments, ok := resolvedConfig["assignments"].([]interface{})
	if !ok || len(resolvedAssignments) != 2 {
		t.Fatalf("resolved assignments = %#v, want a 2-element slice", resolvedConfig["assignments"])
	}
	elem0 := resolvedAssignments[0].(map[string]interface{})
	if elem0["field"] != "a" || elem0["value"] != "{{$json.x}}" {
		t.Errorf("assignments[0] = %+v, want field=a value={{$json.x}} (raw)", elem0)
	}
	elem1 := resolvedAssignments[1].(map[string]interface{})
	if elem1["field"] != "b" || elem1["value"] != "{{$json.y}}" {
		t.Errorf("assignments[1] = %+v, want field=b value={{$json.y}} (raw)", elem1)
	}
}

// TestExtractPerItemFields_AbsentFieldsAreNoop verifies a spec naming a field
// that isn't present in config (e.g. a node registered but not using one of
// its optional per-item fields) is silently skipped rather than erroring or
// fabricating an entry.
func TestExtractPerItemFields_AbsentFieldsAreNoop(t *testing.T) {
	config := map[string]interface{}{"other": "x"}
	state := extractPerItemFields(config, []string{"condition", "assignments[].value"})
	if len(state.topLevel) != 0 || len(state.nested) != 0 {
		t.Fatalf("extractPerItemFields state = %+v, want empty (no matching fields present)", state)
	}
	if len(config) != 1 {
		t.Fatalf("config = %v, want untouched", config)
	}
}

func TestErrorHandleMessage_HTTPStatus(t *testing.T) {
	long := strings.Repeat("x ", 200)
	cases := []struct {
		name string
		item map[string]interface{}
		want string
	}{
		{"error field wins", map[string]interface{}{"error": "boom", "status": 500}, "boom"},
		{"status and object body", map[string]interface{}{"status": 404, "body": map[string]interface{}{}}, "HTTP 404 Not Found: {}"},
		{"statusCode float, text body on one line", map[string]interface{}{"statusCode": float64(502), "body": "bad\n  gateway"}, "HTTP 502 Bad Gateway: bad gateway"},
		{"status_code, no body", map[string]interface{}{"status_code": 418}, "HTTP 418 I'm a teapot"},
		{"no status", map[string]interface{}{"exit_code": 1}, ""},
	}
	for _, c := range cases {
		if got := errorHandleMessage([]Item{{JSON: c.item}}); got != c.want {
			t.Errorf("%s: got %q, want %q", c.name, got, c.want)
		}
	}
	got := errorHandleMessage([]Item{{JSON: map[string]interface{}{"status": 500, "body": long}}})
	snippet := strings.TrimPrefix(got, "HTTP 500 Internal Server Error: ")
	if n := len([]rune(snippet)); n != 120 || !strings.HasSuffix(snippet, "…") {
		t.Errorf("long body snippet = %q (%d runes), want 120 runes ending in …", snippet, n)
	}
}
