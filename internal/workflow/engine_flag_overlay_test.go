package workflow

import (
	"context"
	"testing"

	"github.com/rs/zerolog"
)

// lyingListStore lists every workflow as active while the workflows themselves are not: what a
// listing built from a file that says is_active:true would have said.
type lyingListStore struct{ *SQLiteWorkflowStore }

func (l lyingListStore) ListAllProfilesWorkflows(ctx context.Context) ([]Workflow, error) {
	all, err := l.SQLiteWorkflowStore.ListAllProfilesWorkflows(ctx)
	for i := range all {
		all[i].IsActive = true
	}
	return all, err
}

// Starting the engine arms triggers only for workflows that are active themselves, whatever a
// listing says.
func TestRestoreAndReregisterArmNothingForAnInactiveWorkflow(t *testing.T) {
	store := newFullEngineStore(t)
	ctx := context.Background()
	saveTriggerWorkflow(t, ctx, store, "wf", scheduleNode("s", "0 0 9 * * *", ""))
	sched := newRecScheduler()
	eng := NewWorkflowEngineWithStore(lyingListStore{store}, store.RawDB(), sched, NewNodeTypeRegistry(),
		EngineConfig{ProfileID: "default", AllowAllProfiles: true, WebhookAddr: "127.0.0.1:0"}, zerolog.Nop())
	t.Cleanup(func() { _ = eng.Stop() })
	if err := eng.RestoreActiveWorkflows(ctx); err != nil {
		t.Fatal(err)
	}
	if got := sched.specs(); len(got) != 0 {
		t.Fatalf("restore armed %v for an inactive workflow", got)
	}
	_ = eng.reregisterTriggers(ctx)
	if got := sched.specs(); len(got) != 0 {
		t.Fatalf("reregister armed %v for an inactive workflow", got)
	}
}

// toggleFailingStore refuses to switch a workflow off, as a store whose database stayed busy would.
type toggleFailingStore struct {
	*SQLiteWorkflowStore
	failOff bool
}

func (s *toggleFailingStore) SetWorkflowActive(ctx context.Context, id string, active bool) error {
	if !active && s.failOff {
		return context.DeadlineExceeded
	}
	return s.SQLiteWorkflowStore.SetWorkflowActive(ctx, id, active)
}

// When the store refuses a deactivation, the triggers stay armed: the workflow is still active.
func TestADeactivationTheStoreRefusesLeavesTheTriggersArmed(t *testing.T) {
	base := newFullEngineStore(t)
	ctx := context.Background()
	saveTriggerWorkflow(t, ctx, base, "wf", scheduleNode("s", "0 0 9 * * *", ""))
	store := &toggleFailingStore{SQLiteWorkflowStore: base}
	sched := newRecScheduler()
	eng := NewWorkflowEngineWithStore(store, base.RawDB(), sched, NewNodeTypeRegistry(),
		EngineConfig{ProfileID: "default", AllowAllProfiles: true, WebhookAddr: "127.0.0.1:0"}, zerolog.Nop())
	t.Cleanup(func() { _ = eng.Stop() })
	if err := eng.ActivateWorkflow(ctx, "wf"); err != nil {
		t.Fatal(err)
	}
	if len(sched.specs()) == 0 {
		t.Fatal("setup: nothing armed")
	}
	store.failOff = true
	if err := eng.DeactivateWorkflow(ctx, "wf"); err == nil {
		t.Fatal("the refusal was swallowed")
	}
	if len(sched.specs()) == 0 {
		t.Fatal("the triggers were disarmed although the workflow is still active")
	}
}
