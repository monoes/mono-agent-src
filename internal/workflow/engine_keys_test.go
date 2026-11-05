package workflow

import (
	"context"
	"net/http"
	"testing"

	"github.com/monoes/mono-agent/internal/vault"
)

// A caller's input keeps its own fields, the items it passes (_items)
// included, and loses the ones only the engine writes.
func TestStripEngineKeys(t *testing.T) {
	data := map[string]interface{}{
		AICallerKey: "unapproved-workflow", UnapprovedCallerKey: "wf-x", SubWorkflowItemsKey: []interface{}{1},
		SubWorkflowParentKey: map[string]interface{}{}, "keep": 1, "_mine": 2,
	}
	StripEngineKeys(data)
	if len(data) != 3 || data["keep"] != 1 || data["_mine"] != 2 || data[SubWorkflowItemsKey] == nil {
		t.Fatalf("data = %v", data)
	}
	StripEngineKeys(nil) // no input
}

// A webhook's body can't set the engine's fields at the top level: not who
// started the run, and not the items it starts with, so one delivery stays
// one item. $json.body keeps what was sent.
func TestAWebhookBodyCantSetTheEnginesFields(t *testing.T) {
	s, fired := itemHook(t, &WebhookRegistration{})
	rec := send(s, http.MethodPost, "/webhook/hook", "application/json", []byte(
		`{"keep":1,"_ai_caller":"unapproved-workflow","_unapproved_caller":"wf-x","_subworkflow":{"depth":30},"_items":[{"n":1},{"n":2}]}`))
	got := firedJSON(t, rec, fired)
	for _, k := range []string{AICallerKey, UnapprovedCallerKey, SubWorkflowParentKey, SubWorkflowItemsKey} {
		if _, ok := got[k]; ok {
			t.Errorf("the body set %s: %v", k, got)
		}
	}
	body, _ := got["body"].(map[string]interface{})
	if got["keep"] != float64(1) || body[SubWorkflowItemsKey] == nil || body[AICallerKey] == nil {
		t.Errorf("the body's own fields: %v", got)
	}
	if items := buildTriggerItems(got); len(items) != 1 {
		t.Errorf("one delivery became %d items", len(items))
	}
}

// An evaluation's dataset row is the case's item and carries none of the
// engine's fields into it; the row itself is left as it was.
func TestAnEvaluationRowCarriesNoEngineFields(t *testing.T) {
	_, store := approvalsTestDB(t)
	eng := newTestEngine(t, store, NewNodeTypeRegistry())
	wf := approvalsTestWorkflow("wf-eval", 1)
	wf.Nodes[0].Type = TriggerTypeEvaluation
	createSQLWorkflow(t, store, wf)
	ctx := vault.ContextWithProfileID(context.Background(), "default")
	row := map[string]interface{}{"question": "2+2", AICallerKey: "unapproved-workflow", UnapprovedCallerKey: "wf-x", SubWorkflowParentKey: map[string]interface{}{}}
	id, err := eng.StartEvaluationCase(ctx, "wf-eval", "wf-eval-t", row, EvaluationCase{RunID: "run-1", CaseID: "case-1"})
	if err != nil {
		t.Fatal(err)
	}
	exec, err := store.GetExecution(ctx, id)
	if err != nil || exec == nil {
		t.Fatalf("the case's execution: %v", err)
	}
	items, _ := exec.TriggerData[SubWorkflowItemsKey].([]interface{})
	item, _ := items[0].(map[string]interface{})
	if len(items) != 1 || item["question"] != "2+2" || len(item) != 1 {
		t.Errorf("the case's item = %v", items)
	}
	if len(row) != 4 {
		t.Errorf("the row was changed: %v", row)
	}
}

// The mark a command carries in ctx (WithRunMark) is the case's AI-caller mark; the row cannot set one, and a
// ctx without a mark leaves the case unmarked (issue 1455).
func TestAnEvaluationCaseTakesTheCommandsRunMark(t *testing.T) {
	_, store := approvalsTestDB(t)
	eng := newTestEngine(t, store, NewNodeTypeRegistry())
	wf := approvalsTestWorkflow("wf-eval", 1)
	wf.Nodes[0].Type = TriggerTypeEvaluation
	createSQLWorkflow(t, store, wf)
	base := vault.ContextWithProfileID(context.Background(), "default")
	markOf := func(ctx context.Context) interface{} {
		t.Helper()
		row := map[string]interface{}{"question": "2+2", AICallerKey: "user"}
		id, err := eng.StartEvaluationCase(ctx, "wf-eval", "wf-eval-t", row, EvaluationCase{RunID: "run-1", CaseID: "case-1"})
		if err != nil {
			t.Fatal(err)
		}
		exec, err := store.GetExecution(ctx, id)
		if err != nil || exec == nil {
			t.Fatalf("the case's execution: %v", err)
		}
		return exec.TriggerData[AICallerKey]
	}
	if got := markOf(base); got != nil {
		t.Errorf("no mark in ctx: the case is marked %v", got)
	}
	if got := markOf(WithRunMark(base, "")); got != nil {
		t.Errorf("an empty mark: the case is marked %v", got)
	}
	if got := markOf(WithRunMark(base, "other")); got != "other" {
		t.Errorf("the command's mark: the case is marked %v", got)
	}
}
