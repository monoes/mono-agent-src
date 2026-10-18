package control

import (
	"context"
	"strings"
	"testing"

	"github.com/zalando/go-keyring"

	"github.com/monoes/mono-agent/internal/secrets"
	"github.com/monoes/mono-agent/internal/workflow"
)

// A workflow the user never approved calls an approved one with "@secret:vs"
// as its input: the child's step that interpolates it is refused, and the
// value is in no run. The child's own reference is the user's, and it
// resolves under the same parent (#845 review: secret references as data).
func TestAnUnapprovedParentCantPassASecretReferenceToAChild(t *testing.T) {
	keyring.MockInit()
	f := newSubflowFixture(t)
	ctx := context.Background()
	if _, err := secrets.Add(ctx, f.store.RawDB(), "default", "secret", "vs", map[string]string{"secret": "vsecret-777"}, "", "", ""); err != nil {
		t.Fatal(err)
	}
	stop := func(message string) workflow.WorkflowNode {
		return workflow.WorkflowNode{Type: "core.stop_error", Config: map[string]interface{}{"message": message}}
	}
	for _, id := range []string{f.chain(t, "echo", workflow.TriggerTypeExecuteWorkflow, false, stop("{{ $json.msg }}")),
		f.chain(t, "own", workflow.TriggerTypeExecuteWorkflow, false, stop("@secret:vs"))} {
		if err := f.store.ApproveDefinition(ctx, id); err != nil {
			t.Fatal(err)
		}
	}
	f.chain(t, "calls-echo", "trigger.manual", true, execNode(map[string]interface{}{"workflowId": "echo"}))
	f.chain(t, "calls-own", "trigger.manual", true, execNode(map[string]interface{}{"workflowId": "own"}))

	run := func(parent string) string {
		t.Helper()
		id, err := f.eng.TriggerWorkflow(ctx, parent, map[string]interface{}{
			workflow.SubWorkflowItemsKey: []interface{}{map[string]interface{}{"msg": "@secret:vs"}}})
		if err != nil {
			t.Fatal(err)
		}
		f.waitTerminal(t, id)
		var b strings.Builder
		rows, err := f.store.RawDB().Query(`SELECT e.error_message, COALESCE(n.error_message, ''), COALESCE(n.output_items, '')
			FROM workflow_executions e LEFT JOIN workflow_execution_nodes n ON n.execution_id = e.id`)
		if err != nil {
			t.Fatal(err)
		}
		defer rows.Close()
		for rows.Next() {
			var a, c, d string
			if err := rows.Scan(&a, &c, &d); err != nil {
				t.Fatal(err)
			}
			b.WriteString(a + "\n" + c + "\n" + d + "\n")
		}
		return b.String()
	}
	got := run("calls-echo")
	if strings.Contains(got, "vsecret-777") {
		t.Fatalf("the parent's data resolved in the child:\n%s", got)
	}
	if !strings.Contains(got, secrets.ErrDataReference.Error()) {
		t.Errorf("the child's step was not refused for a reference that came from data:\n%s", got)
	}
	if got := run("calls-own"); !strings.Contains(got, "StopError: vsecret-777") {
		t.Errorf("the approved child's own reference did not resolve:\n%s", got)
	}
}
