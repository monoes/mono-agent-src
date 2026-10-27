package workflow

import (
	"context"
	"testing"

	"github.com/rs/zerolog"
)

// newVersionsEngine wires an engine over a hybrid (file + SQLite) store with
// a fake scheduler, so schedule triggers register without real cron jobs.
func newVersionsEngine(t *testing.T) (*WorkflowEngine, *HybridWorkflowStore, *SQLiteWorkflowStore) {
	t.Helper()
	h, s := newHybridForTest(t)
	return newVersionsEngineOver(t, h, s), h, s
}

// newVersionsEngineOver is the engine over store (h, or a wrapper of it).
func newVersionsEngineOver(t *testing.T, store WorkflowStore, s *SQLiteWorkflowStore) *WorkflowEngine {
	t.Helper()
	eng := NewWorkflowEngineWithStore(store, s.RawDB(), &fakeScheduler{}, NewNodeTypeRegistry(),
		EngineConfig{ProfileID: "default"}, zerolog.Nop())
	t.Cleanup(func() { _ = eng.Stop() })
	return eng
}

// scheduledWF is trigger.schedule → core.set with set's value v.
func scheduledWF(id, v string) *Workflow {
	w := versionWF(id, v)
	w.Nodes[0].Type = "trigger.schedule"
	w.Nodes[0].Config = map[string]interface{}{"cron": "0 0 9 * * *"}
	return w
}

func registeredTriggers(eng *WorkflowEngine, id string) int {
	eng.triggerMgr.mu.Lock()
	defer eng.triggerMgr.mu.Unlock()
	return len(eng.triggerMgr.active[id])
}

func createActiveScheduled(t *testing.T, eng *WorkflowEngine, id string) {
	t.Helper()
	ctx := context.Background()
	if err := eng.CreateWorkflow(ctx, scheduledWF(id, "old")); err != nil {
		t.Fatal(err)
	}
	approveForTest(t, eng, id)
	if err := eng.ActivateWorkflow(ctx, id); err != nil {
		t.Fatal(err)
	}
	if registeredTriggers(eng, id) != 1 {
		t.Fatalf("schedule trigger not registered")
	}
}

// assertActiveOld checks the workflow is still active, armed, and has its
// previous definition in both stores.
func assertActiveOld(t *testing.T, eng *WorkflowEngine, h *HybridWorkflowStore, s *SQLiteWorkflowStore, id string) {
	t.Helper()
	ctx := context.Background()
	fromFile, _ := h.GetWorkflow(ctx, id)
	fromSQL, _ := s.GetWorkflow(ctx, id)
	for name, wf := range map[string]*Workflow{"file": fromFile, "sqlite": fromSQL} {
		if !wf.IsActive {
			t.Errorf("%s: workflow left inactive", name)
		}
		if got := setValue(t, wf); got != "old" || wf.Name != id {
			t.Errorf("%s: name %q value %q; want the old definition", name, wf.Name, got)
		}
	}
	if n := registeredTriggers(eng, id); n != 1 {
		t.Errorf("%d triggers registered, want the old one", n)
	}
}

// A save that fails mid-way (the connections are written after the row and
// nodes) leaves the workflow active, armed and unchanged. Before #21 the
// workflow was deactivated first and stayed inactive after the failure.
func TestEngineSaveWorkflow_FailureMidSaveKeepsActiveOldDefinition(t *testing.T) {
	eng, h, s := newVersionsEngine(t)
	createActiveScheduled(t, eng, "wf-x")

	bad := scheduledWF("wf-x", "new")
	bad.Name = "renamed"
	bad.Connections[0].TargetNodeID = "missing-node"
	if err := eng.SaveWorkflow(context.Background(), bad); err == nil {
		t.Fatal("save with a dangling connection succeeded")
	}
	assertActiveOld(t, eng, h, s, "wf-x")
}

// When the new definition's triggers fail to register, the previous
// definition is saved back and its triggers re-armed.
func TestEngineSaveWorkflow_TriggerFailureRestoresPrevious(t *testing.T) {
	eng, h, s := newVersionsEngine(t)
	createActiveScheduled(t, eng, "wf-t")

	next := scheduledWF("wf-t", "new")
	next.Nodes[0].Config = map[string]interface{}{} // no cron: registration fails
	if err := eng.SaveWorkflow(context.Background(), next); err == nil {
		t.Fatal("save with an unregistrable trigger succeeded")
	}
	assertActiveOld(t, eng, h, s, "wf-t")
}

// A good save of an active workflow stays active and is recorded.
func TestEngineSaveWorkflow_ActiveSaveKeepsActive(t *testing.T) {
	eng, h, _ := newVersionsEngine(t)
	createActiveScheduled(t, eng, "wf-g")
	ctx := context.Background()

	if err := eng.SaveWorkflow(ctx, scheduledWF("wf-g", "new")); err != nil {
		t.Fatal(err)
	}
	wf, _ := h.GetWorkflow(ctx, "wf-g")
	if !wf.IsActive || setValue(t, wf) != "new" || wf.Version != 2 {
		t.Errorf("active %v value %q version %d", wf.IsActive, setValue(t, wf), wf.Version)
	}
	if n := registeredTriggers(eng, "wf-g"); n != 1 {
		t.Errorf("%d triggers registered, want 1", n)
	}
}

// An execution runs the definition it was created against, even after the
// workflow is edited (a WAITING run resuming); new executions get the latest.
func TestLoadExecutionWorkflow_UsesPinnedVersion(t *testing.T) {
	eng, h, _ := newVersionsEngine(t)
	ctx := context.Background()
	if err := eng.CreateWorkflow(ctx, versionWF("wf-r", "old")); err != nil {
		t.Fatal(err)
	}
	waiting := &WorkflowExecution{WorkflowID: "wf-r", TriggerType: "manual", ProfileID: "default"}
	if err := h.CreateExecution(ctx, waiting); err != nil {
		t.Fatal(err)
	}
	if err := eng.SaveWorkflow(ctx, versionWF("wf-r", "new")); err != nil {
		t.Fatal(err)
	}

	got, err := eng.loadExecutionWorkflow(ctx, zerolog.Nop(), ExecutionRequest{WorkflowID: "wf-r", ExecutionID: waiting.ID})
	if err != nil {
		t.Fatal(err)
	}
	if setValue(t, got) != "old" || got.Version != 1 {
		t.Errorf("resumed run sees value %q version %d; want old, 1", setValue(t, got), got.Version)
	}

	fresh := &WorkflowExecution{WorkflowID: "wf-r", TriggerType: "manual", ProfileID: "default"}
	if err := h.CreateExecution(ctx, fresh); err != nil {
		t.Fatal(err)
	}
	got, _ = eng.loadExecutionWorkflow(ctx, zerolog.Nop(), ExecutionRequest{WorkflowID: "wf-r", ExecutionID: fresh.ID})
	if setValue(t, got) != "new" {
		t.Errorf("new run sees value %q; want new", setValue(t, got))
	}

	// An unpinned execution (created before versions existed) runs the
	// current definition.
	got, _ = eng.loadExecutionWorkflow(ctx, zerolog.Nop(), ExecutionRequest{WorkflowID: "wf-r", ExecutionID: "no-such-exec"})
	if setValue(t, got) != "new" {
		t.Errorf("unpinned run sees value %q; want new", setValue(t, got))
	}
}

// A resumed run sees the settings of its pinned version (e.g. which error
// workflow to start), not the edited ones.
func TestLoadExecutionWorkflow_PinnedSettings(t *testing.T) {
	eng, h, _ := newVersionsEngine(t)
	ctx := context.Background()
	w := versionWF("wf-ps", "same")
	w.Settings = map[string]interface{}{ErrorWorkflowSetting: "old-handler"}
	if err := eng.CreateWorkflow(ctx, w); err != nil {
		t.Fatal(err)
	}
	waiting := &WorkflowExecution{WorkflowID: "wf-ps", TriggerType: "manual", ProfileID: "default"}
	if err := h.CreateExecution(ctx, waiting); err != nil {
		t.Fatal(err)
	}
	edited := versionWF("wf-ps", "same")
	edited.Settings = map[string]interface{}{ErrorWorkflowSetting: "new-handler"}
	if err := eng.SaveWorkflow(ctx, edited); err != nil {
		t.Fatal(err)
	}
	got, err := eng.loadExecutionWorkflow(ctx, zerolog.Nop(), ExecutionRequest{WorkflowID: "wf-ps", ExecutionID: waiting.ID})
	if err != nil {
		t.Fatal(err)
	}
	if s := errorWorkflowSettingOf(got); s != "old-handler" {
		t.Errorf("pinned run sees error workflow %q, want old-handler", s)
	}
}
