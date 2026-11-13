package httpapi

import (
	"context"
	"encoding/json"
	"fmt"
	"github.com/monoes/mono-agent/internal/testhome"
	"net/http"
	"testing"

	"github.com/monoes/mono-agent/internal/workflow"
)

// The run route's input can't plant the engine's marks (who started the
// run, which call it is), and still passes several items with _items.
func TestRunRouteInputCantPlantTheEnginesMarks(t *testing.T) {
	testhome.SetHome(t, t.TempDir())
	s := newTestServer(t, true)
	tok := testToken(t, s, "default")
	wf := &workflow.Workflow{ProfileID: "default", ID: "wf-in", Name: "in", IsActive: true,
		Nodes: []workflow.WorkflowNode{{ID: "t", Type: "trigger.manual", Name: "Start"}}}
	if err := s.rt.store.CreateWorkflow(workflow.WithApproval(context.Background()), wf); err != nil {
		t.Fatal(err)
	}
	// Whatever mark the input plants (blank, a chat's, an unapproved caller's, a suffixed one), the run
	// carries the server's own: an agent's ("other").
	for _, planted := range []string{"unapproved-workflow", "", "chat", "unapproved-workflow/x"} {
		body := fmt.Sprintf(`{"input":{"keep":"yes","_ai_caller":%q,"_unapproved_caller":"wf-x","_subworkflow":{},"_items":[{"n":1},{"n":2}]}}`, planted)
		r := doReq(t, s, "POST", "/profiles/default/workflows/wf-in/run", tok, []byte(body))
		if r.Code != http.StatusOK {
			t.Fatalf("run: %d %s", r.Code, r.Body.String())
		}
		var mark string
		if err := s.rt.db.DB.QueryRow(`SELECT COALESCE(json_extract(trigger_data, '$._ai_caller'), '') FROM workflow_executions
			WHERE workflow_id = 'wf-in' ORDER BY rowid DESC LIMIT 1`).Scan(&mark); err != nil {
			t.Fatal(err)
		}
		if mark != "other" {
			t.Errorf("planted %q: the run's mark is %q, want the server's own %q", planted, mark, "other")
		}
	}
	var raw, out string
	if err := s.rt.db.DB.QueryRow(`SELECT e.trigger_data, n.output_items FROM workflow_executions e
		JOIN workflow_execution_nodes n ON n.execution_id = e.id AND n.node_id = 't' WHERE e.workflow_id = 'wf-in'`).Scan(&raw, &out); err != nil {
		t.Fatal(err)
	}
	var td map[string]interface{}
	var items []workflow.Item
	if err := json.Unmarshal([]byte(raw), &td); err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal([]byte(out), &items); err != nil {
		t.Fatal(err)
	}
	if got, _ := td[workflow.AICallerKey].(string); got != "other" {
		t.Errorf("the run's mark is %q, want the server's own %q", got, "other")
	}
	for _, k := range []string{workflow.UnapprovedCallerKey, workflow.SubWorkflowParentKey} {
		if _, ok := td[k]; ok {
			t.Errorf("the input planted %s: %v", k, td)
		}
	}
	if td["keep"] != "yes" || len(items) != 2 || items[1].JSON["n"] != float64(2) {
		t.Errorf("trigger data %v, the trigger's items %v", td, items)
	}
}
