package workflow

import (
	"context"
	"errors"
	"testing"
	"time"
)

// The error workflow of an org-started run keeps the run's org marker, so its
// file nodes stay confined to the role's workdir (r17-R17-18).
func TestErrorWorkflowKeepsTheOrgConfinementMarker(t *testing.T) {
	r := newErrorWorkflowRig(t, errors.New("boom"))
	r.addWorkflow(t, "wf-err", TriggerNodeTypeError, "test.capture", nil, false)
	r.addWorkflow(t, "wf-main", "trigger.manual", "test.fail", map[string]interface{}{ErrorWorkflowSetting: "wf-err"}, true)

	data := map[string]interface{}{"org": map[string]interface{}{"workdir": "/tmp/role-dir", "name": "acme"}}
	execID, err := r.eng.TriggerWorkflow(context.Background(), "wf-main", data)
	if err != nil {
		t.Fatal(err)
	}
	waitForTerminalStatus(t, r.store, execID, 10*time.Second)
	run := r.waitErrorRun(t, "wf-err")
	got, err := r.store.GetExecution(context.Background(), run.ID)
	if err != nil || got == nil {
		t.Fatalf("load error run: %v", err)
	}
	org, _ := got.TriggerData["org"].(map[string]interface{})
	if org["workdir"] != "/tmp/role-dir" {
		t.Fatalf("error workflow lost the org marker: trigger data %#v", got.TriggerData)
	}
}
