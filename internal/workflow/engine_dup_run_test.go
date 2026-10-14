package workflow

import (
	"context"
	"os"
	"sync/atomic"
	"testing"
	"time"
)

// blockingCountNode counts its runs and then holds its concurrency slot for d,
// so later executions of the same engine wait QUEUED for a slot.
type blockingCountNode struct {
	runs *int32
	d    time.Duration
}

func (blockingCountNode) Type() string { return "test.block" }
func (b blockingCountNode) Execute(ctx context.Context, in NodeInput, _ map[string]interface{}) ([]NodeOutput, error) {
	atomic.AddInt32(b.runs, 1)
	select {
	case <-time.After(b.d):
	case <-ctx.Done():
	}
	return []NodeOutput{{Handle: "main", Items: in.Items}}, nil
}

// fillGateAndAdopt triggers cap+1 runs on owner (one waits QUEUED for a slot),
// then lets every engine in adopters sweep for adoptable rows. Returns the
// execution IDs.
func fillGateAndAdopt(t *testing.T, ctx context.Context, owner *WorkflowEngine, wfID string, adopters ...*WorkflowEngine) []string {
	t.Helper()
	var ids []string
	for i := 0; i < cap(owner.queue.gate.tokens)+1; i++ {
		id, err := owner.TriggerWorkflow(ctx, wfID, nil)
		if err != nil {
			t.Fatalf("trigger: %v", err)
		}
		ids = append(ids, id)
	}
	time.Sleep(200 * time.Millisecond)
	for _, a := range adopters {
		a.adoptQueuedExecutions(ctx)
	}
	return ids
}

// TestEngine_QueuedWaitingForSlotIsNotAdopted is the #5 regression: a run
// waiting in its own engine for a concurrency slot is QUEUED, and used to
// carry pid 0 — so the resume tick adopted it and the body ran twice.
func TestEngine_QueuedWaitingForSlotIsNotAdopted(t *testing.T) {
	store := newFullEngineStore(t)
	var runs int32
	reg := NewNodeTypeRegistry()
	reg.Register("test.block", func() NodeExecutor { return blockingCountNode{runs: &runs, d: 800 * time.Millisecond} })
	eng := newTestEngine(t, store, reg)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	eng.queue.Start(ctx)
	wfID := createActiveManualWorkflow(t, ctx, eng, "test.block")

	ids := fillGateAndAdopt(t, ctx, eng, wfID, eng)
	for _, id := range ids {
		waitForTerminalStatus(t, store, id, 20*time.Second)
	}
	time.Sleep(time.Second) // a duplicate dispatch would still be counting
	if n := atomic.LoadInt32(&runs); int(n) != len(ids) {
		t.Fatalf("node ran %d times for %d executions — duplicate run", n, len(ids))
	}
}

// TestEngine_QueuedWaitingForSlotIsNotAdopted_TwoEngines: another engine
// sharing the DB must not adopt the rows a live engine has queued itself.
func TestEngine_QueuedWaitingForSlotIsNotAdopted_TwoEngines(t *testing.T) {
	store := newFullEngineStore(t)
	var runs int32
	reg := NewNodeTypeRegistry()
	reg.Register("test.block", func() NodeExecutor { return blockingCountNode{runs: &runs, d: 800 * time.Millisecond} })
	owner := newTestEngine(t, store, reg)
	other := newTestEngine(t, store, reg)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	owner.queue.Start(ctx)
	other.queue.Start(ctx)
	wfID := createActiveManualWorkflow(t, ctx, owner, "test.block")

	ids := fillGateAndAdopt(t, ctx, owner, wfID, other, owner)
	for _, id := range ids {
		if st := waitForTerminalStatus(t, store, id, 20*time.Second); st != "SUCCESS" {
			t.Errorf("execution %s = %s, want SUCCESS", id, st)
		}
	}
	time.Sleep(time.Second)
	if n := atomic.LoadInt32(&runs); int(n) != len(ids) {
		t.Fatalf("node ran %d times for %d executions across two engines", n, len(ids))
	}
}

// TestEngine_DuplicateDispatchRunsOnce: whatever puts the same execution in
// a queue twice, the QUEUED→RUNNING CAS lets exactly one dispatch run it.
func TestEngine_DuplicateDispatchRunsOnce(t *testing.T) {
	store := newFullEngineStore(t)
	var runs int32
	reg := NewNodeTypeRegistry()
	reg.Register("test.block", func() NodeExecutor { return blockingCountNode{runs: &runs, d: 300 * time.Millisecond} })
	eng := newTestEngine(t, store, reg)
	other := newTestEngine(t, store, reg)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	wfID := createActiveManualWorkflow(t, ctx, eng, "test.block")

	exec := &WorkflowExecution{WorkflowID: wfID, Status: "QUEUED", TriggerType: "trigger.manual", PID: os.Getpid()}
	if err := store.CreateExecution(ctx, exec); err != nil {
		t.Fatal(err)
	}
	req := ExecutionRequest{WorkflowID: wfID, ExecutionID: exec.ID, TriggerType: "trigger.manual"}
	eng.queue.Start(ctx)
	other.queue.Start(ctx)
	if err := eng.queue.Enqueue(req); err != nil {
		t.Fatal(err)
	}
	if err := eng.queue.Enqueue(req); err != nil {
		t.Fatal(err)
	}
	if err := other.queue.Enqueue(req); err != nil {
		t.Fatal(err)
	}
	if st := waitForTerminalStatus(t, store, exec.ID, 10*time.Second); st != "SUCCESS" {
		t.Fatalf("status = %s, want SUCCESS", st)
	}
	time.Sleep(500 * time.Millisecond)
	if n := atomic.LoadInt32(&runs); n != 1 {
		t.Fatalf("node ran %d times for one execution dispatched three times", n)
	}
}

// TestEngine_LocalRunsStampOwnerPID: rows a live engine enqueues itself
// carry its pid from creation; only TriggerWorkflowPersistOnly leaves pid 0.
func TestEngine_LocalRunsStampOwnerPID(t *testing.T) {
	store := newFullEngineStore(t)
	reg := NewNodeTypeRegistry()
	var runs int32
	reg.Register("test.count.atomic", func() NodeExecutor { return atomicCountNode{runs: &runs} })
	eng := newTestEngine(t, store, reg)
	ctx := context.Background()
	wfID := createActiveManualWorkflow(t, ctx, eng, "test.count.atomic")

	pidOf := func(id string) int {
		t.Helper()
		var pid int
		if err := store.RawDB().QueryRow(`SELECT COALESCE(pid,0) FROM workflow_executions WHERE id=?`, id).Scan(&pid); err != nil {
			t.Fatal(err)
		}
		return pid
	}
	// The queue is not started: rows stay QUEUED as created.
	local, err := eng.TriggerWorkflow(ctx, wfID, nil)
	if err != nil {
		t.Fatal(err)
	}
	if got := pidOf(local); got != os.Getpid() {
		t.Errorf("TriggerWorkflow row pid = %d, want %d", got, os.Getpid())
	}
	eng.ctx = ctx // handleTrigger runs under the engine context Start sets
	eng.handleTrigger(wfID, "t", nil)
	var trig string
	_ = store.RawDB().QueryRow(`SELECT id FROM workflow_executions WHERE id != ? ORDER BY created_at DESC LIMIT 1`, local).Scan(&trig)
	if got := pidOf(trig); got != os.Getpid() {
		t.Errorf("handleTrigger row pid = %d, want %d", got, os.Getpid())
	}
	retried, err := eng.RetryExecution(ctx, local)
	if err != nil {
		t.Fatal(err)
	}
	if got := pidOf(retried); got != os.Getpid() {
		t.Errorf("RetryExecution row pid = %d, want %d", got, os.Getpid())
	}
	persisted, err := eng.TriggerWorkflowPersistOnly(ctx, wfID, nil)
	if err != nil {
		t.Fatal(err)
	}
	if got := pidOf(persisted); got != 0 {
		t.Errorf("TriggerWorkflowPersistOnly row pid = %d, want 0", got)
	}
}

// TestRecoverStale_DeadOwnerQueuedRows: a dead owner's QUEUED row that never
// started becomes adoptable again (pid 0); one with resume state goes back
// to WAITING; a dead owner's RUNNING row fails.
func TestRecoverStale_DeadOwnerQueuedRows(t *testing.T) {
	s := newExecStore(t)
	ctx := context.Background()
	dead := deadPID(t)
	for _, r := range []struct{ id, status, resume string }{
		{"never-started", "QUEUED", ""},
		{"resumed", "QUEUED", "{}"},
		{"running", "RUNNING", ""},
	} {
		if _, err := s.db.Exec(`INSERT INTO workflow_executions (id, workflow_id, status, resume_state, pid) VALUES (?, 'wf', ?, ?, ?)`,
			r.id, r.status, r.resume, dead); err != nil {
			t.Fatal(err)
		}
	}
	if err := s.RecoverStaleExecutions(ctx); err != nil {
		t.Fatal(err)
	}
	want := map[string]struct {
		status string
		pid    int
	}{
		"never-started": {"QUEUED", 0},
		"resumed":       {"WAITING", dead},
		"running":       {"FAILED", dead},
	}
	for id, w := range want {
		var st string
		var pid int
		if err := s.db.QueryRow(`SELECT status, COALESCE(pid,0) FROM workflow_executions WHERE id=?`, id).Scan(&st, &pid); err != nil {
			t.Fatal(err)
		}
		if st != w.status || pid != w.pid {
			t.Errorf("%s: status/pid = %s/%d, want %s/%d", id, st, pid, w.status, w.pid)
		}
	}
	ids, err := s.ListAdoptableExecutions(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(ids) != 1 || ids[0] != "never-started" {
		t.Errorf("adoptable after recovery = %v, want [never-started]", ids)
	}
}

// TestAdoptable_DeadOwnerQueuedRowWithoutRestart: a live engine adopts a dead
// process's never-started QUEUED row without waiting for its own restart.
func TestAdoptable_DeadOwnerQueuedRowWithoutRestart(t *testing.T) {
	s := newExecStore(t)
	ctx := context.Background()
	dead := deadPID(t)
	if _, err := s.db.Exec(`INSERT INTO workflow_executions (id, workflow_id, status, pid) VALUES ('orphan', 'wf', 'QUEUED', ?)`, dead); err != nil {
		t.Fatal(err)
	}
	if _, err := s.db.Exec(`INSERT INTO workflow_executions (id, workflow_id, status, pid) VALUES ('live', 'wf', 'QUEUED', ?)`, os.Getpid()); err != nil {
		t.Fatal(err)
	}
	ids, err := s.ListAdoptableExecutions(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(ids) != 1 || ids[0] != "orphan" {
		t.Fatalf("adoptable = %v, want [orphan]", ids)
	}
	if ok, err := s.ClaimQueuedExecution(ctx, "orphan"); err != nil || !ok {
		t.Fatalf("claim orphan = %v, %v", ok, err)
	}
}

// TestStartQueuedExecution_CAS: only a QUEUED row owned by this process (or
// unowned) can be started, and only once.
func TestStartQueuedExecution_CAS(t *testing.T) {
	s := newExecStore(t)
	ctx := context.Background()
	insertExec(t, s, "q", "QUEUED", "")
	insertExec(t, s, "c", "CANCELLED", "")
	if _, err := s.db.Exec(`INSERT INTO workflow_executions (id, workflow_id, status, pid) VALUES ('foreign', 'wf', 'QUEUED', ?)`, os.Getpid()+1); err != nil {
		t.Fatal(err)
	}
	if ok, err := s.StartQueuedExecution(ctx, "q"); err != nil || !ok {
		t.Fatalf("first start = %v, %v; want true", ok, err)
	}
	if ok, _ := s.StartQueuedExecution(ctx, "q"); ok {
		t.Error("second start won; want false")
	}
	if ok, _ := s.StartQueuedExecution(ctx, "c"); ok {
		t.Error("cancelled row started")
	}
	if ok, _ := s.StartQueuedExecution(ctx, "foreign"); ok {
		t.Error("row owned by another process started")
	}
}

// TestEngine_EnqueueClaimsUnownedRow: a path that creates its row with pid 0
// and enqueues it locally (sub-workflow children do) still owns it once it
// is in the queue, so no engine's adoption sweep can take it.
func TestEngine_EnqueueClaimsUnownedRow(t *testing.T) {
	store := newFullEngineStore(t)
	eng := newTestEngine(t, store, NewNodeTypeRegistry())
	other := newTestEngine(t, store, NewNodeTypeRegistry())
	ctx := context.Background()
	wfID := createActiveManualWorkflow(t, ctx, eng, "test.count.atomic")
	row := &WorkflowExecution{WorkflowID: wfID, Status: "QUEUED", TriggerType: "trigger.manual"}
	if err := store.CreateExecution(ctx, row); err != nil {
		t.Fatal(err)
	}
	// The queue is not started: the request stays buffered.
	if err := eng.queue.Enqueue(ExecutionRequest{WorkflowID: wfID, ExecutionID: row.ID}); err != nil {
		t.Fatal(err)
	}
	var pid int
	if err := store.RawDB().QueryRow(`SELECT COALESCE(pid,0) FROM workflow_executions WHERE id=?`, row.ID).Scan(&pid); err != nil {
		t.Fatal(err)
	}
	if pid != os.Getpid() {
		t.Fatalf("enqueued row pid = %d, want %d", pid, os.Getpid())
	}
	ids, err := store.ListAdoptableExecutions(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(ids) != 0 {
		t.Fatalf("locally enqueued row is adoptable: %v", ids)
	}
	other.adoptQueuedExecutions(ctx)
	if len(other.queue.ch) != 0 {
		t.Fatal("another engine adopted a row this engine has queued")
	}
}
