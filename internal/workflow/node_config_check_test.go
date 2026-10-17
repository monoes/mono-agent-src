package workflow

import (
	"context"
	"fmt"
	"strings"
	"testing"

	"github.com/rs/zerolog"
)

// A node config the installed check refuses is a validate error naming the
// node, and activation refuses it before the workflow goes live; a
// disabled node isn't checked, and a config the check accepts activates.
func TestNodeConfigCheckAtValidateAndActivation(t *testing.T) {
	SetNodeConfigCheck(func(nodeType string, config map[string]interface{}) error {
		if nodeType == "app.fake" && config["bad"] != nil {
			return fmt.Errorf(`apipkg fake ticket.update: updateFields has no option "bad"`)
		}
		return nil
	})
	t.Cleanup(func() { SetNodeConfigCheck(nil) })
	wf := &Workflow{ID: "wf1", Name: "w", ProfileID: "default", Nodes: []WorkflowNode{
		{ID: "t", WorkflowID: "wf1", Type: "trigger.manual", Name: "T"},
		{ID: "upd", WorkflowID: "wf1", Type: "app.fake", Name: "Update ticket", Config: map[string]interface{}{"bad": 1}},
		{ID: "off", WorkflowID: "wf1", Type: "app.fake", Name: "Off", Disabled: true, Config: map[string]interface{}{"bad": 1}},
	}, Connections: []WorkflowConnection{{SourceNodeID: "t", SourceHandle: "main", TargetNodeID: "upd", TargetHandle: "main"}}}

	rep := ValidateWorkflow(wf, ValidateOptions{})
	want := `node "upd": apipkg fake ticket.update: updateFields has no option "bad"`
	if rep.Valid || !strings.Contains(strings.Join(rep.Errors, "\n"), want) || strings.Contains(strings.Join(rep.Errors, "\n"), `"off"`) {
		t.Errorf("validate: valid=%v errors %q; want %q, and nothing for the disabled node", rep.Valid, rep.Errors, want)
	}

	e := &WorkflowEngine{profileID: "default", store: &stubStore{workflowToReturn: wf},
		triggerMgr: NewTriggerManager(nil, nil, &fakeScheduler{}, func(string, string, []Item) {}, zerolog.Nop()), logger: zerolog.Nop()}
	err := e.ActivateWorkflow(context.Background(), "wf1")
	if err == nil || !strings.Contains(err.Error(), `node upd (Update ticket): apipkg fake ticket.update: updateFields has no option "bad"`) {
		t.Errorf("activate: %v", err)
	}
	wf.Nodes[1].Config = map[string]interface{}{}
	if err := e.ActivateWorkflow(context.Background(), "wf1"); err != nil {
		t.Errorf("activate once declared: %v", err)
	}
}
