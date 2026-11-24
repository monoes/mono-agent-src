package workflow

import (
	"errors"
	"time"
	"context"
	"sort"
	"sync"
	"testing"

	"github.com/robfig/cron/v3"
	"github.com/rs/zerolog"
)

// recScheduler records live cron jobs (spec + callback) so a test can see
// what is registered and fire a job by hand.
type recScheduler struct {
	mu   sync.Mutex
	next cron.EntryID
	jobs map[cron.EntryID]recJob
}

type recJob struct {
	spec string
	fn   func()
}

func newRecScheduler() *recScheduler { return &recScheduler{jobs: map[cron.EntryID]recJob{}} }

func (s *recScheduler) AddWorkflowJob(spec string, fn func()) (cron.EntryID, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, err := scheduleParser.Parse(spec); err != nil {
		return 0, err
	}
	s.next++
	s.jobs[s.next] = recJob{spec: spec, fn: fn}
	return s.next, nil
}

func (s *recScheduler) RemoveJob(id cron.EntryID) {
	s.mu.Lock()
	defer s.mu.Unlock()
	delete(s.jobs, id)
}

func (s *recScheduler) specs() []string {
	s.mu.Lock()
	defer s.mu.Unlock()
	var out []string
	for _, j := range s.jobs {
		out = append(out, j.spec)
	}
	sort.Strings(out)
	return out
}

func (s *recScheduler) added() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return int(s.next)
}

// fireAll runs every registered job once.
func (s *recScheduler) fireAll() {
	s.mu.Lock()
	var fns []func()
	for _, j := range s.jobs {
		fns = append(fns, j.fn)
	}
	s.mu.Unlock()
	for _, fn := range fns {
		fn()
	}
}

// newDaemonEngine is a daemon-style (AllowAllProfiles) engine over store with
// a recording scheduler. It is not started: no port is bound.
func newDaemonEngine(t *testing.T, store *SQLiteWorkflowStore) (*WorkflowEngine, *recScheduler) {
	t.Helper()
	sched := newRecScheduler()
	eng := NewWorkflowEngineWithStore(store, store.RawDB(), sched, NewNodeTypeRegistry(),
		EngineConfig{ProfileID: "default", AllowAllProfiles: true, WebhookAddr: "127.0.0.1:0"}, zerolog.Nop())
	t.Cleanup(func() { _ = eng.Stop() })
	return eng, sched
}

// saveTriggerWorkflow writes a workflow with the given trigger nodes the way
// another process would: straight to the store.
func saveTriggerWorkflow(t *testing.T, ctx context.Context, store *SQLiteWorkflowStore, id string, nodes ...WorkflowNode) {
	t.Helper()
	for i := range nodes {
		nodes[i].WorkflowID = id
	}
	if existing, _ := store.GetWorkflow(ctx, id); existing == nil {
		if err := store.CreateWorkflow(ctx, &Workflow{ID: id, Name: id, ProfileID: "default"}); err != nil {
			t.Fatal(err)
		}
	}
	if err := store.SaveWorkflowNodes(ctx, id, nodes); err != nil {
		t.Fatal(err)
	}
}

func scheduleNode(id, cronSpec, tz string) WorkflowNode {
	cfg := map[string]interface{}{"cron": cronSpec}
	if tz != "" {
		cfg["timezone"] = tz
	}
	return WorkflowNode{ID: id, Type: TriggerNodeTypeSchedule, Name: id, Config: cfg}
}

func webhookNode(id, path, method string) WorkflowNode {
	return WorkflowNode{ID: id, Type: TriggerNodeTypeWebhook, Name: id, Config: map[string]interface{}{"path": path, "method": method}}
}

func webhookRoute(eng *WorkflowEngine, path string) *WebhookRegistration {
	eng.webhookServer.mu.RLock()
	defer eng.webhookServer.mu.RUnlock()
	return eng.webhookServer.routes[path]
}

func equalStrings(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

// TestReconcile_PicksUpChangesMadeElsewhere is #6: the daemon must follow
// activations, edits, deactivations and deletions other processes make.
func TestReconcile_PicksUpChangesMadeElsewhere(t *testing.T) {
	store := newFullEngineStore(t)
	ctx := context.Background()
	eng, sched := newDaemonEngine(t, store)
	if err := eng.RestoreActiveWorkflows(ctx); err != nil {
		t.Fatal(err)
	}

	saveTriggerWorkflow(t, ctx, store, "wf", scheduleNode("s", "0 0 9 * * *", ""), webhookNode("h", "/a", "POST"))
	eng.reconcileTriggers(ctx)
	if got := sched.specs(); len(got) != 0 {
		t.Fatalf("inactive workflow registered %v", got)
	}

	// Activated elsewhere.
	if err := store.SetWorkflowActive(ctx, "wf", true); err != nil {
		t.Fatal(err)
	}
	eng.reconcileTriggers(ctx)
	if got, want := sched.specs(), []string{"CRON_TZ=UTC 0 0 9 * * *"}; !equalStrings(got, want) {
		t.Fatalf("after activate specs = %v, want %v", got, want)
	}
	if webhookRoute(eng, "/a") == nil {
		t.Fatal("webhook /a not registered after activate")
	}

	// Unchanged: no re-registration.
	before := sched.added()
	eng.reconcileTriggers(ctx)
	if sched.added() != before {
		t.Fatal("unchanged workflow was re-registered")
	}

	// Cron, timezone and webhook path/method edited elsewhere (node save only:
	// the workflow row's updated_at does not move).
	saveTriggerWorkflow(t, ctx, store, "wf", scheduleNode("s", "0 30 18 * * *", "Europe/Berlin"), webhookNode("h", "/b", "PUT"))
	eng.reconcileTriggers(ctx)
	if got, want := sched.specs(), []string{"CRON_TZ=Europe/Berlin 0 30 18 * * *"}; !equalStrings(got, want) {
		t.Fatalf("after edit specs = %v, want %v", got, want)
	}
	if webhookRoute(eng, "/a") != nil {
		t.Error("old webhook path /a still registered")
	}
	if r := webhookRoute(eng, "/b"); r == nil || r.Method != "PUT" {
		t.Errorf("webhook /b = %+v, want PUT registration", r)
	}

	// Deactivated elsewhere.
	if err := store.SetWorkflowActive(ctx, "wf", false); err != nil {
		t.Fatal(err)
	}
	eng.reconcileTriggers(ctx)
	if got := sched.specs(); len(got) != 0 {
		t.Fatalf("after deactivate specs = %v", got)
	}
	if webhookRoute(eng, "/b") != nil {
		t.Error("webhook still registered after deactivate")
	}

	// Re-activated, then deleted elsewhere.
	if err := store.SetWorkflowActive(ctx, "wf", true); err != nil {
		t.Fatal(err)
	}
	eng.reconcileTriggers(ctx)
	if len(sched.specs()) != 1 {
		t.Fatalf("re-activate specs = %v", sched.specs())
	}
	if err := store.DeleteWorkflow(ctx, "wf"); err != nil {
		t.Fatal(err)
	}
	eng.reconcileTriggers(ctx)
	if got := sched.specs(); len(got) != 0 {
		t.Fatalf("after delete specs = %v", got)
	}
	if _, ok := eng.triggerMgr.Fingerprint("wf"); ok {
		t.Error("deleted workflow still registered")
	}
}

// TestReconcile_RestoredWorkflowNotReregistered: what RestoreActiveWorkflows
// registered matches the store, so the first reconcile leaves it alone.
func TestReconcile_RestoredWorkflowNotReregistered(t *testing.T) {
	store := newFullEngineStore(t)
	ctx := context.Background()
	saveTriggerWorkflow(t, ctx, store, "wf", scheduleNode("s", "0 0 9 * * *", ""))
	if err := store.SetWorkflowActive(ctx, "wf", true); err != nil {
		t.Fatal(err)
	}
	eng, sched := newDaemonEngine(t, store)
	if err := eng.RestoreActiveWorkflows(ctx); err != nil {
		t.Fatal(err)
	}
	if sched.added() != 1 {
		t.Fatalf("restore added %d jobs, want 1", sched.added())
	}
	eng.reconcileTriggers(ctx)
	if sched.added() != 1 {
		t.Fatalf("reconcile re-registered a restored workflow (%d jobs added)", sched.added())
	}
}

// TestReconcile_OnlyDaemonEngines: a single-profile (one-shot) engine never
// starts the loop.
func TestReconcile_OnlyDaemonEngines(t *testing.T) {
	store := newFullEngineStore(t)
	eng := newTestEngine(t, store, NewNodeTypeRegistry())
	eng.startTriggerReconcile(context.Background())
	started := false
	eng.reconciler.once.Do(func() { started = true })
	if !started {
		t.Fatal("reconcile loop started on a non-daemon engine")
	}
}

// failingScheduler refuses every job until ok is set.
type failingScheduler struct {
	*recScheduler
	mu sync.Mutex
	ok bool
}

func (s *failingScheduler) AddWorkflowJob(spec string, fn func()) (cron.EntryID, error) {
	s.mu.Lock()
	ok := s.ok
	s.mu.Unlock()
	if !ok {
		return 0, errors.New("scheduler briefly unavailable")
	}
	return s.recScheduler.AddWorkflowJob(spec, fn)
}

// TestReconcile_RetriesTransientActivationFailure is r01-r01-5: a failed
// registration must be retried with backoff, flagged while it fails, and
// cleared once it succeeds.
func TestReconcile_RetriesTransientActivationFailure(t *testing.T) {
	store := newFullEngineStore(t)
	ctx := context.Background()
	rec := newRecScheduler()
	fs := &failingScheduler{recScheduler: rec}
	eng := NewWorkflowEngineWithStore(store, store.RawDB(), fs, NewNodeTypeRegistry(),
		EngineConfig{ProfileID: "default", AllowAllProfiles: true, WebhookAddr: "127.0.0.1:0"}, zerolog.Nop())
	t.Cleanup(func() { _ = eng.Stop() })

	saveTriggerWorkflow(t, ctx, store, "wf-retry", scheduleNode("s", "0 */5 * * * *", ""))
	if err := store.SetWorkflowActive(ctx, "wf-retry", true); err != nil {
		t.Fatal(err)
	}
	now := time.Now()
	eng.reconciler.now = func() time.Time { return now }

	eng.reconcileTriggers(ctx)
	if len(rec.specs()) != 0 {
		t.Fatal("registered although the scheduler refused")
	}
	if eng.TriggerFailures()["wf-retry"] == "" {
		t.Fatal("a failed registration is not flagged")
	}
	fs.mu.Lock()
	fs.ok = true
	fs.mu.Unlock()

	eng.reconcileTriggers(ctx) // inside the backoff: no retry yet
	if len(rec.specs()) != 0 {
		t.Fatal("retried before the backoff elapsed")
	}
	now = now.Add(2 * time.Minute)
	eng.reconcileTriggers(ctx)
	if len(rec.specs()) != 1 {
		t.Fatalf("not retried after the backoff: %v %v", rec.specs(), eng.TriggerFailures())
	}
	if len(eng.TriggerFailures()) != 0 {
		t.Fatal("flag not cleared after success")
	}
}
