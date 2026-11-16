package workflow

import (
	"context"
	"testing"

	"github.com/monoes/mono-agent/internal/vault"
)

// A sub-workflow is an AI run when its parent is: one an AI started (its
// mark goes on), one the org side started, or one whose definition the user
// never approved. The user's own run starts the child as theirs. The child's
// own approval, not the parent's, decides whether it is unapproved itself
// (#831 round 5, F2). The workflow the user never approved that is up the
// chain is recorded too, whoever started the run (CalledByUnapproved).
func TestASubWorkflowInheritsItsParentsAIRun(t *testing.T) {
	_, store := approvalsTestDB(t)
	eng := newTestEngine(t, store, NewNodeTypeRegistry())
	base := vault.ContextWithExecIDs(vault.ContextWithProfileID(context.Background(), "default"), "wf-parent", "exec-parent")
	child := approvalsTestWorkflow("wf-child", 1)
	child.Nodes[0].Type = TriggerTypeExecuteWorkflow
	createSQLWorkflow(t, store, child)

	called := map[string]interface{}{AICallerKey: UnapprovedWorkflowCaller, UnapprovedCallerKey: "wf-grandparent"}
	before := map[string]interface{}{AICallerKey: UnapprovedWorkflowCaller} // queued before ids were recorded
	for name, tc := range map[string]struct {
		ctx        context.Context
		want       string
		unapproved string
		calledBy   bool
	}{
		"the user's run":                 {WithTrigger(base, "trigger.manual", map[string]interface{}{}), "", "", false},
		"a chat's run":                   {WithTrigger(base, "trigger.manual", map[string]interface{}{AICallerKey: "chat"}), "chat", "", false},
		"an org's call":                  {WithTrigger(base, TriggerTypeOrgTool, map[string]interface{}{}), "org", "", false},
		"an unapproved run":              {WithAIAuthored(WithTrigger(base, "trigger.schedule", map[string]interface{}{})), UnapprovedWorkflowCaller, "wf-parent", true},
		"a chat's unapproved run":        {WithAIAuthored(WithTrigger(base, "trigger.manual", map[string]interface{}{AICallerKey: "chat"})), "chat", "wf-parent", true},
		"a run an unapproved one called": {WithTrigger(base, TriggerTypeExecuteWorkflow, called), UnapprovedWorkflowCaller, "wf-grandparent", true},
		"a run called before ids":        {WithTrigger(base, TriggerTypeExecuteWorkflow, before), UnapprovedWorkflowCaller, "", true},
	} {
		id, err := eng.StartSubWorkflow(tc.ctx, SubWorkflowStart{ParentExecutionID: "parent", ParentNodeID: "call", Workflow: "wf-child", ItemIndex: -1})
		if err != nil {
			t.Fatalf("%s: %v", name, err)
		}
		exec, err := store.GetExecution(base, id)
		if err != nil || exec == nil {
			t.Fatalf("%s: the child's execution: %v", name, err)
		}
		if got, _ := exec.TriggerData[AICallerKey].(string); got != tc.want {
			t.Errorf("%s: the child's AI caller is %q, want %q", name, got, tc.want)
		}
		if got, ok := CalledByUnapproved(exec.TriggerData); got != tc.unapproved || ok != tc.calledBy {
			t.Errorf("%s: the child is called by unapproved %q (%v), want %q (%v)", name, got, ok, tc.unapproved, tc.calledBy)
		}
	}
}

// A workflow the user never approved, run by the chat, calls an approved
// one, which calls another: both keep the chat's mark, and both name the
// unapproved workflow up their chain.
func TestTheUnapprovedCallerGoesDownTheChain(t *testing.T) {
	_, store := approvalsTestDB(t)
	eng := newTestEngine(t, store, NewNodeTypeRegistry())
	profile := vault.ContextWithProfileID(context.Background(), "default")
	for _, id := range []string{"wf-child", "wf-grandchild"} {
		wf := approvalsTestWorkflow(id, 1)
		wf.Nodes[0].Type = TriggerTypeExecuteWorkflow
		createSQLWorkflow(t, store, wf)
	}
	call := func(ctx context.Context, ref string) map[string]interface{} {
		t.Helper()
		id, err := eng.StartSubWorkflow(ctx, SubWorkflowStart{ParentExecutionID: "parent", ParentNodeID: "call", Workflow: ref, ItemIndex: -1})
		if err != nil {
			t.Fatal(err)
		}
		exec, err := store.GetExecution(profile, id)
		if err != nil || exec == nil {
			t.Fatalf("the execution of %s: %v", ref, err)
		}
		return exec.TriggerData
	}
	parent := WithAIAuthored(WithTrigger(vault.ContextWithExecIDs(profile, "wf-chat-built", "exec-1"), "trigger.manual",
		map[string]interface{}{AICallerKey: "chat"}))
	child := call(parent, "wf-child")
	// The child is approved: its run is not unapproved itself.
	grandchild := call(WithTrigger(vault.ContextWithExecIDs(profile, "wf-child", "exec-2"), TriggerTypeExecuteWorkflow, child), "wf-grandchild")
	for name, data := range map[string]map[string]interface{}{"child": child, "grandchild": grandchild} {
		if mark, _ := data[AICallerKey].(string); mark != "chat" {
			t.Errorf("the %s's AI caller is %q, want chat", name, mark)
		}
		if id, ok := CalledByUnapproved(data); id != "wf-chat-built" || !ok {
			t.Errorf("the %s is called by unapproved %q (%v), want wf-chat-built", name, id, ok)
		}
	}
}

// The unapproved-workflow mark may carry "/declared" after its kind: it still says the caller is unapproved.
func TestCalledByUnapprovedReadsAMarkWithASuffix(t *testing.T) {
	for mark, want := range map[string]bool{
		UnapprovedWorkflowCaller: true, UnapprovedWorkflowCaller + "/org": true, "chat": false, "other/unapproved-workflow": false,
	} {
		if _, ok := CalledByUnapproved(map[string]interface{}{AICallerKey: mark}); ok != want {
			t.Errorf("mark %q: called by unapproved = %v, want %v", mark, ok, want)
		}
	}
}
