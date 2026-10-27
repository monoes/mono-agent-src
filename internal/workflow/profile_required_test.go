package workflow

import (
	"context"
	"errors"
	"testing"
)

func profiledWF(id, profile string) *Workflow {
	w := mirrorWF(id, id+"-trig")
	w.ProfileID = profile
	return w
}

// Two profiles, one hybrid store: each profile lists (and counts) only its own
// workflows, empty is ErrNoProfile, and ListAllProfilesWorkflows is the one
// explicit way to see everything.
func TestHybridStoreListsPerProfileAndRefusesNone(t *testing.T) {
	ctx := context.Background()
	h, sqlStore := newHybridForTest(t)
	for _, p := range []string{"pa", "pb"} {
		if _, err := sqlStore.db.Exec(`INSERT INTO profiles (id, name) VALUES (?, ?)`, p, p); err != nil {
			t.Fatal(err)
		}
	}
	if err := h.CreateWorkflow(ctx, profiledWF("wf-a", "pa")); err != nil {
		t.Fatal(err)
	}
	if err := h.CreateWorkflow(ctx, profiledWF("wf-b", "pb")); err != nil {
		t.Fatal(err)
	}

	for profile, want := range map[string]string{"pa": "wf-a", "pb": "wf-b"} {
		list, err := h.ListWorkflows(ctx, profile)
		if err != nil || len(list) != 1 || list[0].ID != want {
			t.Fatalf("ListWorkflows(%s) = %+v, %v; want only %s", profile, list, err, want)
		}
		counts, err := h.NodeCounts(ctx, profile)
		if err != nil || len(counts) != 1 || counts[want] != 1 {
			t.Fatalf("NodeCounts(%s) = %v, %v; want only %s", profile, counts, err, want)
		}
	}
	if _, err := h.ListWorkflows(ctx, ""); !errors.Is(err, ErrNoProfile) {
		t.Errorf("ListWorkflows(\"\") err = %v, want ErrNoProfile", err)
	}
	if _, err := h.NodeCounts(ctx, ""); !errors.Is(err, ErrNoProfile) {
		t.Errorf("NodeCounts(\"\") err = %v, want ErrNoProfile", err)
	}
	all, err := h.ListAllProfilesWorkflows(ctx)
	if err != nil || len(all) != 2 {
		t.Fatalf("ListAllProfilesWorkflows = %d, %v; want 2", len(all), err)
	}
}

// Nothing is created, saved or started without a profile.
func TestWorkflowStoreRefusesWritesWithNoProfile(t *testing.T) {
	ctx := context.Background()
	h, s := newHybridForTest(t)
	if err := s.CreateWorkflow(ctx, profiledWF("wf-x", "")); !errors.Is(err, ErrNoProfile) {
		t.Errorf("CreateWorkflow err = %v, want ErrNoProfile", err)
	}
	if err := h.SaveWorkflow(ctx, profiledWF("wf-y", "")); !errors.Is(err, ErrNoProfile) {
		t.Errorf("SaveWorkflow err = %v, want ErrNoProfile", err)
	}
	if err := s.CreateExecution(ctx, &WorkflowExecution{WorkflowID: "wf-x"}); !errors.Is(err, ErrNoProfile) {
		t.Errorf("CreateExecution err = %v, want ErrNoProfile", err)
	}
}

// CreateUnownedExecution applies OwnedBy: no profile asked, no profile on the
// workflow, and another profile's workflow are each refused.
func TestCreateUnownedExecutionOwnership(t *testing.T) {
	ctx := context.Background()
	_, s := newHybridForTest(t)
	if err := s.CreateWorkflow(ctx, profiledWF("wf-own", "pa")); err != nil {
		t.Fatal(err)
	}
	run := func(profile string) error {
		_, err := CreateUnownedExecution(ctx, s, UnownedExecutionOptions{WorkflowID: "wf-own", ProfileID: profile, AllowInactive: true})
		return err
	}
	if err := run(""); !errors.Is(err, ErrNoProfile) {
		t.Errorf("no profile: err = %v, want ErrNoProfile", err)
	}
	if err := run("pb"); !errors.Is(err, ErrWorkflowProfileMismatch) {
		t.Errorf("other profile: err = %v, want ErrWorkflowProfileMismatch", err)
	}
	if err := run("pa"); err != nil {
		t.Errorf("owner: %v", err)
	}
}
