package workflow

import (
	"context"
	"database/sql"
	"errors"
	"os"
	"path/filepath"
	"sync/atomic"
	"testing"
	"time"

	"github.com/rs/zerolog"
	_ "modernc.org/sqlite"

	"github.com/monoes/mono-agent/internal/vault"
)

// TestCheckWorkflowProfile is a regression test: engine methods that load a
// workflow by bare ID (SaveWorkflow, ActivateWorkflow, DeactivateWorkflow,
// TriggerWorkflow, RetryExecution, GetWorkflow) must reject workflows that
// belong to a different profile than the engine's active one.
func TestCheckWorkflowProfile(t *testing.T) {
	e := &WorkflowEngine{profileID: "profile-a"}

	cases := []struct {
		name    string
		wf      *Workflow
		wantErr bool
	}{
		{"same profile", &Workflow{ID: "wf-1", ProfileID: "profile-a"}, false},
		{"different profile", &Workflow{ID: "wf-2", ProfileID: "profile-b"}, true},
		// A legacy row with no profile id is the default profile's (the
		// store's COALESCE rule), not everyone's: see
		// TestCheckWorkflowProfileUnownedMeansDefault.
		{"unset profile (legacy row) from another profile", &Workflow{ID: "wf-3", ProfileID: ""}, true},
	}

	for _, tc := range cases {
		err := e.checkWorkflowProfile(tc.wf)
		if tc.wantErr && err == nil {
			t.Errorf("%s: expected error, got nil", tc.name)
		}
		if !tc.wantErr && err != nil {
			t.Errorf("%s: unexpected error: %v", tc.name, err)
		}
	}
}

// newFullEngineStore opens a SQLiteWorkflowStore over a temp-file DB with
// every table the engine's execution path touches. A file (not :memory:)
// because queue workers run on other goroutines and each pooled :memory:
// connection would be its own database. It uses synchronous(NORMAL) like
// storage.NewDatabase: SQLite's default (FULL) fsyncs the WAL on every
// commit, and on a busy disk one node-finish UPDATE then took seconds,
// which made the 30 s window of TestEngine_LongRunFinalStatusPersisted flaky.
func newFullEngineStore(t *testing.T) *SQLiteWorkflowStore {
	t.Helper()
	db, err := sql.Open("sqlite", "file:"+filepath.Join(t.TempDir(), "engine.db")+"?_pragma=busy_timeout(5000)&_pragma=journal_mode(WAL)&_pragma=synchronous(NORMAL)")
	if err != nil {
		t.Fatalf("open db: %v", err)
	}
	t.Cleanup(func() { db.Close() })
	for _, ddl := range []string{
		`CREATE TABLE workflows (id TEXT PRIMARY KEY, name TEXT, description TEXT DEFAULT '', is_active INTEGER DEFAULT 0, version INTEGER DEFAULT 1, profile_id TEXT, created_at TIMESTAMP, updated_at TIMESTAMP, settings TEXT NOT NULL DEFAULT '{}')`,
		`CREATE TABLE workflow_nodes (id TEXT PRIMARY KEY, workflow_id TEXT, node_type TEXT, name TEXT, config TEXT DEFAULT '{}', position_x REAL DEFAULT 0, position_y REAL DEFAULT 0, disabled INTEGER DEFAULT 0, created_at TIMESTAMP, updated_at TIMESTAMP)`,
		`CREATE TABLE workflow_connections (id TEXT PRIMARY KEY, workflow_id TEXT, source_node_id TEXT, source_handle TEXT DEFAULT 'main', target_node_id TEXT, target_handle TEXT DEFAULT 'main', position INTEGER DEFAULT 0)`,
		`CREATE TABLE workflow_executions (id TEXT PRIMARY KEY, workflow_id TEXT, status TEXT, trigger_type TEXT, trigger_data TEXT DEFAULT '{}', started_at TIMESTAMP, finished_at TIMESTAMP, error_message TEXT DEFAULT '', created_at TIMESTAMP DEFAULT CURRENT_TIMESTAMP, pid INTEGER, resume_state TEXT NOT NULL DEFAULT '', profile_id TEXT NOT NULL DEFAULT 'default', trigger_node_id TEXT NOT NULL DEFAULT '', error_class TEXT NOT NULL DEFAULT '')`,
		`CREATE TABLE workflow_execution_nodes (id TEXT PRIMARY KEY, execution_id TEXT, node_id TEXT, node_name TEXT, status TEXT DEFAULT 'PENDING', input_items TEXT DEFAULT '[]', output_items TEXT DEFAULT '[]', error_message TEXT, started_at TIMESTAMP, finished_at TIMESTAMP, retry_count INTEGER DEFAULT 0, error_class TEXT NOT NULL DEFAULT '', error_hint TEXT NOT NULL DEFAULT '')`,
		`CREATE TABLE hil_pending (id TEXT PRIMARY KEY, execution_id TEXT, status TEXT DEFAULT 'pending')`,
	} {
		if _, err := db.Exec(ddl); err != nil {
			t.Fatalf("create table: %v", err)
		}
	}
	return NewSQLiteWorkflowStore(db)
}

// newTestEngine wires an engine over store with only test-registered node
// types. The queue is NOT started — tests drive dispatch explicitly so no
// webhook port is bound and no background loops run.
func newTestEngine(t *testing.T, store *SQLiteWorkflowStore, registry *NodeTypeRegistry) *WorkflowEngine {
	t.Helper()
	eng := NewWorkflowEngineWithStore(store, store.RawDB(), nil, registry, EngineConfig{ProfileID: "default"}, zerolog.Nop())
	t.Cleanup(func() { _ = eng.Stop() })
	return eng
}

// createActiveManualWorkflow persists trigger.manual → single node workflow
// (active) and returns its ID.
func createActiveManualWorkflow(t *testing.T, ctx context.Context, eng *WorkflowEngine, nodeType string) string {
	t.Helper()
	wf := &Workflow{
		ID:        "wf-" + nodeType,
		Name:      "wf-" + nodeType,
		ProfileID: "default",
		Nodes: []WorkflowNode{
			{ID: "t", WorkflowID: "wf-" + nodeType, Type: "trigger.manual", Name: "T"},
			{ID: "n", WorkflowID: "wf-" + nodeType, Type: nodeType, Name: "N"},
		},
		Connections: []WorkflowConnection{
			{SourceNodeID: "t", SourceHandle: "main", TargetNodeID: "n", TargetHandle: "main"},
		},
	}
	if err := eng.CreateWorkflow(ctx, wf); err != nil {
		t.Fatalf("create workflow: %v", err)
	}
	if err := eng.store.SetWorkflowActive(ctx, wf.ID, true); err != nil {
		t.Fatalf("activate workflow: %v", err)
	}
	return wf.ID
}

// waitForTerminalStatus polls the store until the execution leaves
// QUEUED/RUNNING/WAITING or the timeout hits; returns the terminal status.
func waitForTerminalStatus(t *testing.T, store *SQLiteWorkflowStore, execID string, timeout time.Duration) string {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for {
		st, err := store.GetExecutionStatus(context.Background(), execID)
		if err != nil {
			t.Fatalf("poll status: %v", err)
		}
		switch st {
		case "QUEUED", "RUNNING", "WAITING":
			if time.Now().After(deadline) {
				t.Fatalf("execution still %s after %s", st, timeout)
			}
			time.Sleep(100 * time.Millisecond)
		default:
			return st
		}
	}
}

// slowNode sleeps for d (honouring cancellation) then passes items through.
type slowNode struct{ d time.Duration }

func (slowNode) Type() string { return "test.slow" }
func (s slowNode) Execute(ctx context.Context, in NodeInput, _ map[string]interface{}) ([]NodeOutput, error) {
	select {
	case <-ctx.Done():
		return nil, ErrExecutionCancelled
	case <-time.After(s.d):
	}
	return []NodeOutput{{Handle: "main", Items: in.Items}}, nil
}

// TestEngine_LongRunFinalStatusPersisted is the regression guard for the
// zombie-RUNNING bug (V3-F1): the persistence context used to be a 10s
// timeout created at dispatch, so an execution longer than 10s had its final
// SetExecutionFinished silently fail — the row stayed RUNNING forever, the
// CLI wait hung, and the next startup marked it FAILED. An 11s execution
// must end with SUCCESS durably persisted.
func TestEngine_LongRunFinalStatusPersisted(t *testing.T) {
	store := newFullEngineStore(t)
	reg := NewNodeTypeRegistry()
	reg.Register("test.slow", func() NodeExecutor { return slowNode{d: 11 * time.Second} })
	eng := newTestEngine(t, store, reg)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	eng.queue.Start(ctx)

	wfID := createActiveManualWorkflow(t, ctx, eng, "test.slow")
	execID, err := eng.TriggerWorkflow(ctx, wfID, nil)
	if err != nil {
		t.Fatalf("TriggerWorkflow: %v", err)
	}

	if st := waitForTerminalStatus(t, store, execID, 30*time.Second); st != "SUCCESS" {
		t.Fatalf("final status = %s, want SUCCESS (final-status persistence failed)", st)
	}
	exec, err := store.GetExecution(context.Background(), execID)
	if err != nil {
		t.Fatalf("GetExecution: %v", err)
	}
	if exec.FinishedAt == nil {
		t.Error("finished_at not recorded for the long run")
	}
}

// atomicCountNode counts its executions atomically — readable from the test
// goroutine while a queue worker executes it.
type atomicCountNode struct{ runs *int32 }

func (atomicCountNode) Type() string { return "test.count.atomic" }
func (c atomicCountNode) Execute(_ context.Context, in NodeInput, _ map[string]interface{}) ([]NodeOutput, error) {
	atomic.AddInt32(c.runs, 1)
	return []NodeOutput{{Handle: "main", Items: in.Items}}, nil
}

// TestEngine_AdoptsUnownedQueuedExecution is the --no-wait adoption test
// (V3-F2): a QUEUED execution with pid 0 persisted by a process that already
// exited must be claimed by a live engine and run to completion.
func TestEngine_AdoptsUnownedQueuedExecution(t *testing.T) {
	store := newFullEngineStore(t)
	var runs int32
	reg := NewNodeTypeRegistry()
	reg.Register("test.count.atomic", func() NodeExecutor { return atomicCountNode{runs: &runs} })
	eng := newTestEngine(t, store, reg)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	eng.queue.Start(ctx)

	wfID := createActiveManualWorkflow(t, ctx, eng, "test.count.atomic")

	// The --no-wait artifact: an unowned QUEUED row (pid 0, never dispatched).
	adopted := &WorkflowExecution{
		ProfileID:   "default",
		WorkflowID:  wfID,
		Status:      "QUEUED",
		TriggerType: "trigger.manual",
		TriggerData: map[string]interface{}{},
	}
	if err := store.CreateExecution(ctx, adopted); err != nil {
		t.Fatalf("create execution: %v", err)
	}

	eng.adoptQueuedExecutions(ctx)

	if st := waitForTerminalStatus(t, store, adopted.ID, 15*time.Second); st != "SUCCESS" {
		t.Fatalf("adopted execution final status = %s, want SUCCESS", st)
	}
	if n := atomic.LoadInt32(&runs); n != 1 {
		t.Errorf("adopted node ran %d times, want exactly 1", n)
	}
	var pid int
	if err := store.RawDB().QueryRow(`SELECT COALESCE(pid,0) FROM workflow_executions WHERE id=?`, adopted.ID).Scan(&pid); err != nil {
		t.Fatalf("read pid: %v", err)
	}
	if pid != os.Getpid() {
		t.Errorf("adopted execution pid = %d, want %d (owner stamped on dispatch)", pid, os.Getpid())
	}
}

// TestEngine_AdoptionRunsExactlyOnce_TwoEngines guards the CAS: with two
// engines sharing the DB, an unowned QUEUED execution must be adopted (and
// run) exactly once.
func TestEngine_AdoptionRunsExactlyOnce_TwoEngines(t *testing.T) {
	store := newFullEngineStore(t)
	var runs int32
	reg := NewNodeTypeRegistry()
	reg.Register("test.count.atomic", func() NodeExecutor { return atomicCountNode{runs: &runs} })
	eng1 := newTestEngine(t, store, reg)
	eng2 := newTestEngine(t, store, reg)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	eng1.queue.Start(ctx)
	eng2.queue.Start(ctx)

	wfID := createActiveManualWorkflow(t, ctx, eng1, "test.count.atomic")

	orphan := &WorkflowExecution{
		ProfileID:   "default",
		WorkflowID:  wfID,
		Status:      "QUEUED",
		TriggerType: "trigger.manual",
		TriggerData: map[string]interface{}{},
	}
	if err := store.CreateExecution(ctx, orphan); err != nil {
		t.Fatalf("create execution: %v", err)
	}

	// Both engines sweep; the pid-0→self CAS (and the pid filter in the list
	// query) must ensure exactly one of them runs it.
	eng1.adoptQueuedExecutions(ctx)
	eng2.adoptQueuedExecutions(ctx)

	if st := waitForTerminalStatus(t, store, orphan.ID, 15*time.Second); st != "SUCCESS" {
		t.Fatalf("orphan execution final status = %s, want SUCCESS", st)
	}
	if n := atomic.LoadInt32(&runs); n != 1 {
		t.Errorf("node ran %d times across two adopting engines, want exactly 1", n)
	}
}

// TestExecutionProfileID is a regression test for a bug where every new
// WorkflowExecution silently fell back to the profile_id column's SQL
// default ('default') because nothing ever set it — breaking
// GetExecutionDetail's profile-scoped lookup for any workflow not owned by
// the 'default' profile, and specifically for a long-running daemon's single
// engine instance (RestoreActiveWorkflows) handling many profiles' scheduled
// triggers at once, where the engine's own profileID is not the right value
// for most of those workflows.
func TestExecutionProfileID(t *testing.T) {
	cases := []struct {
		name string
		wf   *Workflow
		want string
	}{
		{"workflow has its own profile", &Workflow{ID: "wf-1", ProfileID: "halansari"}, "halansari"},
		{"another workflow, another profile", &Workflow{ID: "wf-2", ProfileID: "monoes"}, "monoes"},
		{"workflow with no profile yields none (CreateExecution refuses it)", &Workflow{ID: "wf-3", ProfileID: ""}, ""},
		{"nil workflow yields none", nil, ""},
	}
	for _, tc := range cases {
		if got := executionProfileID(tc.wf); got != tc.want {
			t.Errorf("%s: executionProfileID() = %q, want %q", tc.name, got, tc.want)
		}
	}
}

// TestEngineStart_OneShotDoesNotReregisterOtherWorkflowsTriggers is the
// regression guard for the trigger double-fire bug (issue #12): a one-shot
// engine.Start() call (AllowAllProfiles: false — what every `workflow run`,
// `activate`, `deactivate`, `delete`, and `templates run` CLI invocation
// uses) must NOT re-register schedule triggers for workflows other than the
// one it's about to act on. Before the fix, Start() unconditionally
// re-registered every active workflow's triggers into its own
// process-local scheduler, so a one-shot command sharing a profile with a
// running daemon would independently register the daemon's already-active
// schedule triggers — and if a tick landed while both were registered, both
// processes would create an execution for it.
//
// This simulates "the daemon already has the schedule active" by
// registering it directly on the trigger manager (mirroring what the
// daemon's own Start()/RestoreActiveWorkflows would have done in a real
// two-process scenario) and then starting a second, one-shot-style engine
// against the same store and asserts its own scheduler never sees the spec.
func TestEngineStart_OneShotDoesNotReregisterOtherWorkflowsTriggers(t *testing.T) {
	store := newFullEngineStore(t)

	daemonSched := &fakeScheduler{}
	daemonEng := NewWorkflowEngineWithStore(store, store.RawDB(), daemonSched, NewNodeTypeRegistry(), EngineConfig{
		ProfileID:        "default",
		AllowAllProfiles: true,
		WebhookAddr:      "127.0.0.1:0",
	}, zerolog.Nop())
	t.Cleanup(func() { _ = daemonEng.Stop() })

	ctx := context.Background()
	wf := &Workflow{
		ID:        "wf-schedule",
		Name:      "wf-schedule",
		ProfileID: "default",
		IsActive:  true,
		Nodes: []WorkflowNode{
			{ID: "sched", WorkflowID: "wf-schedule", Type: "trigger.schedule", Name: "Sched", Config: map[string]interface{}{
				"cron": "0 0 9 * * *",
			}},
		},
	}
	if err := daemonEng.CreateWorkflow(ctx, wf); err != nil {
		t.Fatalf("create workflow: %v", err)
	}
	if err := daemonEng.store.SetWorkflowActive(ctx, wf.ID, true); err != nil {
		t.Fatalf("activate workflow: %v", err)
	}
	// Mirrors the daemon's own trigger registration for this workflow —
	// what a real daemon process would already have done before the
	// one-shot engine below starts.
	if err := daemonEng.triggerMgr.ActivateWorkflow(ctx, wf); err != nil {
		t.Fatalf("daemon activate triggers: %v", err)
	}
	if len(daemonSched.specs) != 1 {
		t.Fatalf("daemon scheduler has %d specs, want 1", len(daemonSched.specs))
	}

	// The one-shot engine: same profile, same store, AllowAllProfiles unset
	// (false) — exactly what buildEngine(cfg, false) produces for
	// `workflow run`/`activate`/`deactivate`/`delete`/`templates run`.
	oneShotSched := &fakeScheduler{}
	oneShotEng := NewWorkflowEngineWithStore(store, store.RawDB(), oneShotSched, NewNodeTypeRegistry(), EngineConfig{
		ProfileID:   "default",
		WebhookAddr: "127.0.0.1:0",
	}, zerolog.Nop())
	t.Cleanup(func() { _ = oneShotEng.Stop() })

	if err := oneShotEng.Start(context.Background()); err != nil {
		t.Fatalf("one-shot engine Start: %v", err)
	}

	if len(oneShotSched.specs) != 0 {
		t.Fatalf("one-shot engine registered %d schedule(s) into its own scheduler on Start, want 0 — "+
			"it re-registered another process's already-active trigger, which is how the tick double-fires", len(oneShotSched.specs))
	}
}

// TestTriggerWorkflow_StampsWorkflowsOwnProfile is an integration-level
// regression test proving TriggerWorkflow actually persists the workflow's
// own ProfileID on the created execution (not the engine's), via the real
// CreateExecution call path.
func TestTriggerWorkflow_StampsWorkflowsOwnProfile(t *testing.T) {
	store := &stubStore{
		workflowToReturn: &Workflow{ID: "wf-halansari", ProfileID: "halansari", IsActive: true},
	}
	e := &WorkflowEngine{
		profileID:        "default", // the daemon's own engine profile — must NOT end up on the exec
		allowAllProfiles: true,
		store:            store,
		queue:            NewExecutionQueue(1, 1, func(context.Context, ExecutionRequest) {}, zerolog.Nop()),
		logger:           zerolog.Nop(),
	}
	execID, err := e.TriggerWorkflow(context.Background(), "wf-halansari", nil)
	if err != nil {
		t.Fatalf("TriggerWorkflow: %v", err)
	}
	if len(store.createdExecs) != 1 {
		t.Fatalf("expected 1 created execution, got %d", len(store.createdExecs))
	}
	got := store.createdExecs[0]
	if got.ID != execID {
		t.Errorf("returned execID %q doesn't match created execution %q", execID, got.ID)
	}
	if got.ProfileID != "halansari" {
		t.Errorf("execution ProfileID = %q, want %q (the workflow's own profile, not the engine's %q)", got.ProfileID, "halansari", e.profileID)
	}
}

// capturingProfileNode records the vault profile ID present in the context it
// was executed with, so tests can assert what runExecution actually
// propagates downstream to nodes (and, transitively, to credential/secret
// resolution which reads vault.ProfileIDFromContext(ctx)).
type capturingProfileNode struct {
	gotProfileID *string
}

func (capturingProfileNode) Type() string { return "test.capture_profile" }
func (n capturingProfileNode) Execute(ctx context.Context, _ NodeInput, _ map[string]interface{}) ([]NodeOutput, error) {
	*n.gotProfileID, _ = vault.ProfileIDFromContext(ctx)
	return []NodeOutput{{Handle: "main", Items: []Item{NewItem(map[string]interface{}{})}}}, nil
}

// TestRunExecution_UsesExecutionsOwnProfileID is a regression test for
// runExecution stamping the vault context with e.profileID (the engine's own,
// fixed-at-construction profile) instead of exec.ProfileID (the execution's
// real, possibly different, profile — e.g. on a multi-profile daemon with
// AllowAllProfiles). Downstream credential/secret resolution reads the
// profile ID back out of this context, so a mismatch here silently resolves
// the wrong profile's secrets.
func TestRunExecution_UsesExecutionsOwnProfileID(t *testing.T) {
	reg := NewNodeTypeRegistry()
	var gotProfileID string
	reg.Register("test.capture_profile", func() NodeExecutor { return capturingProfileNode{gotProfileID: &gotProfileID} })

	wf := &Workflow{
		ID:        "wf-multi",
		ProfileID: "workflow-owner-profile",
		Name:      "multi-profile",
		Nodes: []WorkflowNode{
			{ID: "t1", WorkflowID: "wf-multi", Type: "trigger.manual", Name: "Trigger"},
			{ID: "n1", WorkflowID: "wf-multi", Type: "test.capture_profile", Name: "Capture"},
		},
		Connections: []WorkflowConnection{
			{SourceNodeID: "t1", SourceHandle: "main", TargetNodeID: "n1", TargetHandle: "main"},
		},
	}
	dag, err := BuildDAG(wf.Nodes, wf.Connections)
	if err != nil {
		t.Fatalf("BuildDAG: %v", err)
	}
	exec := &WorkflowExecution{
		ID:            "e-multi",
		WorkflowID:    "wf-multi",
		ProfileID:     "execution-owner-profile", // the execution's real, stamped profile
		TriggerNodeID: "t1",
	}

	e := &WorkflowEngine{
		profileID: "engine-fixed-profile", // the engine's own, must NOT leak into the vault context
		store:     &stubStore{},
		expr:      NewExpressionEngine(),
		registry:  reg,
		logger:    zerolog.Nop(),
	}

	if err := e.runExecution(context.Background(), exec, wf, dag); err != nil {
		t.Fatalf("runExecution: %v", err)
	}
	if gotProfileID != "execution-owner-profile" {
		t.Errorf("vault profile ID seen by node = %q, want %q (the execution's own profile, not the engine's %q)",
			gotProfileID, "execution-owner-profile", e.profileID)
	}
}

// erroringTriggerSource fails every Activate. A trigger.org node in event
// mode with a blank org_name is exactly this — and schemas/trigger.org.json
// defaults mode to "event" without requiring org_name, so it is a config a
// user can build in the editor.
type erroringTriggerSource struct{}

func (erroringTriggerSource) Activate(*Workflow, *WorkflowNode, func([]Item)) (func(), error) {
	return nil, errors.New("trigger.org: event mode needs a valid org_name")
}

// TestActivateWorkflow_RollsBackTriggersWhenRegistrationFails is a
// regression test: the failure path reverted the stored is_active flag but
// never called DeactivateWorkflow, so triggers registered before the
// failing one stayed live. A workflow with trigger.schedule (*/5) plus a
// trigger.org the manager cannot activate showed as inactive while its cron
// kept firing for the life of the process — and DeleteWorkflow only
// deactivates `if existing.IsActive`, so the cron survived deletion too.
func TestActivateWorkflow_RollsBackTriggersWhenRegistrationFails(t *testing.T) {
	sched := &fakeScheduler{}
	wf := &Workflow{
		ID:        "wf-partial",
		Name:      "partial",
		ProfileID: "default",
		Nodes: []WorkflowNode{
			{ID: "cron", WorkflowID: "wf-partial", Type: "trigger.schedule", Name: "Every 5m",
				Config: map[string]interface{}{"cron": "0 */5 * * * *"}},
			{ID: "org", WorkflowID: "wf-partial", Type: "trigger.org", Name: "Org",
				Config: map[string]interface{}{"mode": "event", "org_name": ""}},
		},
	}
	tm := NewTriggerManager(nil, nil, sched, func(string, string, []Item) {}, zerolog.Nop())
	tm.RegisterProvider("trigger.org", erroringTriggerSource{})
	e := &WorkflowEngine{
		profileID:  "default",
		store:      &stubStore{workflowToReturn: wf},
		triggerMgr: tm,
		logger:     zerolog.Nop(),
	}

	if err := e.ActivateWorkflow(context.Background(), wf.ID); err == nil {
		t.Fatal("ActivateWorkflow: want the failing trigger.org node to fail activation")
	}
	if len(sched.specs) != 1 {
		t.Fatalf("scheduler registered %d cron jobs, want 1", len(sched.specs))
	}
	if len(sched.removed) != 1 {
		t.Errorf("failed activation left the schedule registered: %d cron jobs added, %d removed — "+
			"the workflow reads inactive but keeps firing every 5 minutes", len(sched.specs), len(sched.removed))
	}
	if n := len(tm.active[wf.ID]); n != 0 {
		t.Errorf("trigger manager still holds %d trigger(s) for a workflow whose activation failed", n)
	}
}
