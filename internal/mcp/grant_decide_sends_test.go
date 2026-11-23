package mcp

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/monoes/mono-agent/internal/orgdecide"
	"github.com/monoes/mono-agent/internal/orggrant"
	"github.com/monoes/mono-agent/internal/workflow"
)

// A decider role's grant never lets it settle an item whose approval lets a
// message to a person go out (#831 round 7): not a Human-in-Loop item in
// front of a send, not a call of an automation that sends. It can still
// hand one to a person (escalate), and decide the others.
func TestDecisionResolveRefusesItemsThatSendAMessage(t *testing.T) {
	s, db := newOrgToolServer(t, []orggrant.OrgTool{
		{Tool: orgdecide.ToolDecisionList, Orgs: []string{"sales"}},
		{Tool: orgdecide.ToolDecisionResolve, Orgs: []string{"sales"}},
	})
	ctx := context.Background()
	client := &recordingClient{}
	old := decisionClient
	decisionClient = client
	t.Cleanup(func() { decisionClient = old })

	wfs := workflow.NewSQLiteWorkflowStore(db.DB)
	for id, node := range map[string]workflow.WorkflowNode{
		"wf-send": {ID: "wf-send-e", WorkflowID: "wf-send", Type: "comm.email_send", Name: "Email",
			Config: map[string]interface{}{"to": "ada@example.test", "subject": "Hi", "body": "Hello"}},
		"wf-sum": {ID: "wf-sum-s", WorkflowID: "wf-sum", Type: "core.set", Name: "Set"},
	} {
		if err := wfs.CreateWorkflow(ctx, &workflow.Workflow{ID: id, Name: id, ProfileID: "default"}); err != nil {
			t.Fatal(err)
		}
		if err := wfs.SaveWorkflowNodes(ctx, id, []workflow.WorkflowNode{node}); err != nil {
			t.Fatal(err)
		}
		alias := strings.TrimPrefix(id, "wf-")
		if _, err := orggrant.NewStore(db.DB).UpsertGrant(ctx, orggrant.GrantInput{ProfileID: "default", OrgName: "sales", RoleID: "lead",
			Tool: orggrant.Tool{Alias: alias, WorkflowID: id, Tier: "consequential"}}); err != nil {
			t.Fatal(err)
		}
	}
	for _, q := range []string{
		`INSERT INTO workflow_executions (id, workflow_id, status, trigger_type, trigger_data, profile_id) VALUES ('e-send', 'wf-send', 'WAITING', 'org_tool', '{"org":{"name":"sales","role":"lead","automation":"send"}}', 'default')`,
		`INSERT INTO hil_pending (id, execution_id, workflow_id, node_id, node_name, node_config, profile_id) VALUES ('hil-send', 'e-send', 'wf-send', 'h', 'Review email', '{"outbound":true}', 'default')`,
	} {
		if _, err := db.DB.Exec(q); err != nil {
			t.Fatal(err)
		}
	}
	store := orgdecide.NewStore(db.DB)
	setOrgLevel(t, store, "sales", "mid", nil)
	delegate := func(it orgdecide.Item) string {
		t.Helper()
		d, err := store.Delegate(ctx, "default", "sales", "mid", "hq", "ceo", it, time.Now().Add(time.Minute))
		if err != nil {
			t.Fatal(err)
		}
		return d.ID
	}
	resolve := func(id, verdict string) error {
		_, err := s.callGrantTool(ctx, orgdecide.ToolDecisionResolve, json.RawMessage(`{"id":"`+id+`","verdict":"`+verdict+`","rationale":"x"}`))
		return err
	}
	hil := delegate(orgdecide.Item{Kind: orgdecide.KindHIL, Ref: "hil-send", Requester: "lead", Class: "hil:send", Tier: "consequential"})
	send := delegate(orgdecide.Item{Kind: orgdecide.KindApproval, Ref: "req-send", RequestID: "req-send", Requester: "lead", Class: "grant:send", Tier: "consequential", Action: "monoagent__automation_send"})
	sum := delegate(orgdecide.Item{Kind: orgdecide.KindApproval, Ref: "req-sum", RequestID: "req-sum", Requester: "lead", Class: "grant:sum", Tier: "consequential", Action: "monoagent__automation_sum"})

	for _, id := range []string{hil, send} {
		for _, verdict := range []string{"approve", "deny"} {
			if err := resolve(id, verdict); err == nil || !strings.HasPrefix(err.Error(), codeRefusedGrant) || !strings.Contains(err.Error(), "only the user decides") {
				t.Errorf("%s %s: %v", verdict, id, err)
			}
		}
		if d, _ := store.GetDelegation(ctx, id); d == nil || d.Status != orgdecide.DelegationPending {
			t.Errorf("%s after the refusals: %+v", id, d)
		}
	}
	var status string
	_ = db.DB.QueryRow(`SELECT status FROM hil_pending WHERE id = 'hil-send'`).Scan(&status)
	if status != "pending" || len(client.calls) != 0 {
		t.Fatalf("a refused item was settled: hil %s, calls %v", status, client.calls)
	}
	if err := resolve(hil, "escalate"); err != nil {
		t.Errorf("handing an item that sends to a person: %v", err)
	}
	if err := resolve(sum, "approve"); err != nil || len(client.calls) != 1 || !strings.Contains(client.calls[0], "automation_sum:approve") {
		t.Errorf("a call of an automation that sends nothing: %v %v", err, client.calls)
	}
}
