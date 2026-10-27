package workflow

import (
	"bytes"
	"context"
	"errors"
	"strings"
	"testing"
)

// unapprovedRig is a hybrid store with one inactive workflow nobody approved (what an agent's save
// leaves behind).
func unapprovedRig(t *testing.T) (*HybridWorkflowStore, *SQLiteWorkflowStore) {
	t.Helper()
	h, s := newHybridForTest(t)
	if err := h.SaveWorkflow(context.Background(), mirrorWF("wf-u", "u-trig", "u-set")); err != nil {
		t.Fatal(err)
	}
	if got, _ := h.GetWorkflow(context.Background(), "wf-u"); got == nil || got.IsActive || approvals(t, s, "wf-u") != 0 {
		t.Fatalf("the rig is not an inactive, unapproved workflow: %+v", got)
	}
	return h, s
}

// Switching a workflow on needs an approval of its definition: by the operator's approving change, or
// one made earlier. An unapproved definition is refused, nothing is written and no change is left open.
func TestActivationNeedsAnApprovedDefinition(t *testing.T) {
	ctx := context.Background()
	h, s := unapprovedRig(t)
	before := fileBytes(t, h, "wf-u")

	err := h.SetWorkflowActive(ctx, "wf-u", true)
	if !errors.Is(err, ErrApprovalRequired) || !strings.Contains(err.Error(), "workflow activate wf-u") {
		t.Fatalf("an unapproved activation: %v", err)
	}
	if got, _ := h.GetWorkflow(ctx, "wf-u"); got.IsActive {
		t.Fatal("a refused activation switched the workflow on")
	}
	if !bytes.Equal(fileBytes(t, h, "wf-u"), before) || openIntentCount(t, s) != 0 || approvals(t, s, "wf-u") != 0 {
		t.Fatal("a refused activation left a trace (file, open change or approval)")
	}

	// Approved earlier (the operator's own save or activation): it goes through.
	approveForTest(t, h, "wf-u")
	if err := h.SetWorkflowActive(ctx, "wf-u", true); err != nil {
		t.Fatalf("an approved definition: %v", err)
	}
	if got, _ := h.GetWorkflow(ctx, "wf-u"); !got.IsActive {
		t.Fatal("not active")
	}
	// Switching off is never refused for want of an approval.
	if err := h.SetWorkflowActive(ctx, "wf-u", false); err != nil {
		t.Fatalf("switching off: %v", err)
	}
}

// An approval given with the activation lands in the same commit as the flag, and a guard that no
// longer matches stops both.
func TestActivationApprovalRidesTheActivation(t *testing.T) {
	ctx := context.Background()
	h, s := unapprovedRig(t)
	cur, _ := h.GetWorkflow(ctx, "wf-u")
	hash, _ := ReviewHash(cur)

	// The workflow changed since the caller read it: no activation and no approval.
	err := h.SetWorkflowActive(WithExpectHash(WithApproval(ctx), "not-what-was-read"), "wf-u", true)
	if !errors.Is(err, ErrChangedSincePreview) {
		t.Fatalf("a stale guard: %v", err)
	}
	if got, _ := h.GetWorkflow(ctx, "wf-u"); got.IsActive || approvals(t, s, "wf-u") != 0 {
		t.Fatal("a refused activation switched the workflow on or approved it")
	}

	if err := h.SetWorkflowActive(WithExpectHash(WithApproval(ctx), hash), "wf-u", true); err != nil {
		t.Fatal(err)
	}
	now, _ := h.GetWorkflow(ctx, "wf-u")
	defHash, _ := definitionHash(now)
	if ok, _ := h.DefinitionApproved(ctx, "wf-u", defHash); !now.IsActive || !ok {
		t.Fatalf("active=%v approved=%v: the flag and the approval must come together", now.IsActive, ok)
	}
}

// An approval removed after the activation was planned must not let it through: the check is made
// again when the change commits.
func TestActivationIsRefusedWhenTheApprovalIsGoneByCommit(t *testing.T) {
	ctx := context.Background()
	h, s := unapprovedRig(t)
	approveForTest(t, h, "wf-u")
	before := fileBytes(t, h, "wf-u")
	guardedCommitFault = func() error {
		_, err := s.db.Exec(revokeApprovalsSQL, "wf-u")
		return err
	}
	t.Cleanup(func() { guardedCommitFault = nil })

	if err := h.SetWorkflowActive(ctx, "wf-u", true); !errors.Is(err, ErrApprovalRequired) {
		t.Fatalf("%v", err)
	}
	if got, _ := h.GetWorkflow(ctx, "wf-u"); got.IsActive || !bytes.Equal(fileBytes(t, h, "wf-u"), before) || openIntentCount(t, s) != 0 {
		t.Fatal("the refused activation was not rolled back")
	}
}

// changingStore runs after() once the activation has been committed, as a concurrent writer would.
type changingStore struct {
	*HybridWorkflowStore
	after func()
}

func (c *changingStore) SetWorkflowActive(ctx context.Context, id string, active bool) error {
	err := c.HybridWorkflowStore.SetWorkflowActive(ctx, id, active)
	if err == nil && active && c.after != nil {
		c.after()
	}
	return err
}

// A rollback after a failed trigger registration carries neither the approval nor the expected hash
// of the activation: if it kept the approval, it would approve whatever the workflow became in the
// meantime (here an agent's change made after the activation committed).
func TestActivationRollbackCarriesNeitherApprovalNorGuard(t *testing.T) {
	ctx := WithApproval(context.Background())
	h, s := newHybridForTest(t)
	cs := &changingStore{HybridWorkflowStore: h}
	eng := newVersionsEngineOver(t, cs, s)
	bad := scheduledWF("wf-r", "x")
	bad.Nodes[0].Config = map[string]interface{}{} // no cron: registration fails
	if err := eng.CreateWorkflow(ctx, bad); err != nil {
		t.Fatal(err)
	}
	var changedHash string
	cs.after = func() { // an agent's save, after the activation committed and before its rollback
		w, _ := h.GetWorkflow(context.Background(), "wf-r")
		w.Name = "changed by someone else"
		if err := h.SaveWorkflow(context.Background(), w); err != nil {
			t.Error(err)
			return
		}
		now, _ := h.GetWorkflow(context.Background(), "wf-r")
		changedHash, _ = definitionHash(now)
	}
	err := eng.ActivateWorkflowWith(ctx, "wf-r", ActivateOptions{})
	if err == nil || strings.Contains(err.Error(), "could not be reverted") {
		t.Fatalf("a failed registration must roll back cleanly: %v", err)
	}
	got, _ := h.GetWorkflow(context.Background(), "wf-r")
	if got.IsActive {
		t.Fatal("the failed activation left the workflow on")
	}
	if ok, _ := h.DefinitionApproved(context.Background(), "wf-r", changedHash); changedHash == "" || ok {
		t.Fatalf("the rollback approved the definition someone else wrote (hash %q, approved %v)", changedHash, ok)
	}

	c := withoutPublishIntent(WithExpectHash(WithApproval(context.Background()), "h"))
	if approvalFrom(c) || expectFrom(c) != "" {
		t.Fatal("withoutPublishIntent keeps the approval or the guard")
	}
}

// ActivateWorkflowWith refuses a hash the caller did not read.
func TestActivateWorkflowWithExpectHash(t *testing.T) {
	ctx := WithApproval(context.Background())
	eng, h, _ := newVersionsEngine(t)
	if err := eng.CreateWorkflow(ctx, scheduledWF("wf-e", "old")); err != nil {
		t.Fatal(err)
	}
	old, _ := h.GetWorkflow(ctx, "wf-e")
	oldHash, _ := ReviewHash(old)
	if err := eng.SaveWorkflow(ctx, scheduledWF("wf-e", "new")); err != nil {
		t.Fatal(err)
	}
	err := eng.ActivateWorkflowWith(ctx, "wf-e", ActivateOptions{ExpectHash: oldHash})
	if !errors.Is(err, ErrChangedSincePreview) {
		t.Fatalf("an --expect that names an older definition: %v", err)
	}
	if got, _ := h.GetWorkflow(ctx, "wf-e"); got.IsActive {
		t.Fatal("activated on a stale hash")
	}
	cur, _ := h.GetWorkflow(ctx, "wf-e")
	curHash, _ := ReviewHash(cur)
	if err := eng.ActivateWorkflowWith(ctx, "wf-e", ActivateOptions{ExpectHash: curHash}); err != nil {
		t.Fatalf("the hash that was read: %v", err)
	}
}

// countingStore counts the node and connection saves an engine makes on top of CreateWorkflow.
type countingStore struct {
	*HybridWorkflowStore
	nodeSaves, connSaves int
}

func (c *countingStore) SaveWorkflowNodes(ctx context.Context, id string, n []WorkflowNode) error {
	c.nodeSaves++
	return c.HybridWorkflowStore.SaveWorkflowNodes(ctx, id, n)
}

func (c *countingStore) SaveWorkflowConnections(ctx context.Context, id string, n []WorkflowConnection) error {
	c.connSaves++
	return c.HybridWorkflowStore.SaveWorkflowConnections(ctx, id, n)
}

// engine.CreateWorkflow on a store that keeps the whole definition makes one publication, so a
// change cannot land between the creation and the approval: the nodes and the connections are not
// saved again (each of those would be rebuilt on whatever the workflow is by then).
func TestCreateWorkflowIsOnePublicationAndApprovedWithIt(t *testing.T) {
	ctx := WithApproval(context.Background())
	h, s := newHybridForTest(t)
	cs := &countingStore{HybridWorkflowStore: h}
	eng := newVersionsEngineOver(t, cs, s)
	if err := eng.CreateWorkflow(ctx, scheduledWF("wf-c", "v")); err != nil {
		t.Fatal(err)
	}
	if cs.nodeSaves != 0 || cs.connSaves != 0 {
		t.Fatalf("the nodes (%d) and connections (%d) were published again after the creation", cs.nodeSaves, cs.connSaves)
	}
	if n := versionCount(t, s, "wf-c"); n != 1 {
		t.Fatalf("%d versions: the creation was more than one publication", n)
	}
	got, _ := h.GetWorkflow(context.Background(), "wf-c")
	hash, _ := definitionHash(got)
	if ok, _ := h.DefinitionApproved(context.Background(), "wf-c", hash); !ok || got.IsActive {
		t.Fatalf("approved=%v active=%v: created inactive, approved with the creation", ok, got.IsActive)
	}
}

// A temporary workflow (`workflow templates run`) is created under the operator's approval and then
// activated without one: it only goes through for exactly the definition the creation approved, so a
// rewrite between the two (an agent's save, a file edit) is refused and nothing runs.
func TestTempRunActivationOnlyGoesThroughForWhatTheCreationApproved(t *testing.T) {
	operator := WithApproval(context.Background())
	plain := context.Background()
	eng, h, _ := newVersionsEngine(t)

	if err := eng.CreateWorkflow(operator, scheduledWF("wf-t1", "v")); err != nil {
		t.Fatal(err)
	}
	if err := eng.ActivateWorkflow(plain, "wf-t1"); err != nil {
		t.Fatalf("activating what the creation approved: %v", err)
	}

	if err := eng.CreateWorkflow(operator, scheduledWF("wf-t2", "v")); err != nil {
		t.Fatal(err)
	}
	rewritten, _ := h.GetWorkflow(plain, "wf-t2")
	rewritten.Name = "rewritten between the creation and the activation"
	if err := h.SaveWorkflow(plain, rewritten); err != nil { // an agent's save: not approving
		t.Fatal(err)
	}
	if err := eng.ActivateWorkflow(plain, "wf-t2"); !errors.Is(err, ErrApprovalRequired) {
		t.Fatalf("activating a rewritten definition: %v", err)
	}
	if got, _ := h.GetWorkflow(plain, "wf-t2"); got.IsActive || registeredTriggers(eng, "wf-t2") != 0 {
		t.Fatal("a refused activation switched the workflow on or armed its triggers")
	}
}

// racingStore runs before() just before an activation reaches the store, as a concurrent writer would
// between the engine reading the workflow and publishing the activation.
type racingStore struct {
	*HybridWorkflowStore
	before func()
}

func (r *racingStore) SetWorkflowActive(ctx context.Context, id string, active bool) error {
	if active && r.before != nil {
		r.before()
	}
	return r.HybridWorkflowStore.SetWorkflowActive(ctx, id, active)
}

// The engine pins the hash it read to the activation (WithExpectHash): a save that lands between the
// read and the publish is refused, and not approved under the operator's context.
func TestActivationPinsTheHashTheEngineRead(t *testing.T) {
	ctx := WithApproval(context.Background())
	h, s := newHybridForTest(t)
	rs := &racingStore{HybridWorkflowStore: h}
	eng := newVersionsEngineOver(t, rs, s)
	if err := eng.CreateWorkflow(ctx, scheduledWF("wf-p", "v")); err != nil {
		t.Fatal(err)
	}
	var changedHash string
	rs.before = func() { // an agent's save (not approving) between the read and the publish
		w, _ := h.GetWorkflow(context.Background(), "wf-p")
		w.Name = "changed in the window"
		if err := h.SaveWorkflow(context.Background(), w); err != nil {
			t.Error(err)
			return
		}
		now, _ := h.GetWorkflow(context.Background(), "wf-p")
		changedHash, _ = definitionHash(now)
	}
	if err := eng.ActivateWorkflowWith(ctx, "wf-p", ActivateOptions{}); !errors.Is(err, ErrChangedSincePreview) {
		t.Fatalf("a save between the read and the publish: %v", err)
	}
	got, _ := h.GetWorkflow(context.Background(), "wf-p")
	if ok, _ := h.DefinitionApproved(context.Background(), "wf-p", changedHash); got.IsActive || changedHash == "" || ok {
		t.Fatalf("active=%v approved(changed)=%v: the change was approved or armed under the operator's context", got.IsActive, ok)
	}
}
