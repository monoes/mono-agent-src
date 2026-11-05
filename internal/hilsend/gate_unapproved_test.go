package hilsend

import (
	"context"
	"database/sql"
	"encoding/json"
	"reflect"
	"testing"

	"github.com/rs/zerolog"

	"github.com/monoes/mono-agent/internal/workflow"
)

// engineStore is the engine's store for these runs: it answers whether the
// user approved the workflow's definition and records nothing else.
type engineStore struct {
	workflow.WorkflowStore
	approved bool
}

func (s *engineStore) DefinitionApproved(context.Context, string, string) (bool, error) {
	return s.approved, nil
}
func (s *engineStore) CreateExecutionNode(context.Context, *workflow.WorkflowExecutionNode) error {
	return nil
}
func (s *engineStore) UpdateExecutionNode(context.Context, *workflow.WorkflowExecutionNode) error {
	return nil
}
func (s *engineStore) SetExecutionNodeFinished(context.Context, string, string, []workflow.Item, string) error {
	return nil
}

// stubSend stands in for a sending node: it counts what it would send.
type stubSend struct {
	typ  string
	sent *[]map[string]interface{}
}

func (s stubSend) Type() string { return s.typ }
func (s stubSend) Execute(_ context.Context, _ workflow.NodeInput, config map[string]interface{}) ([]workflow.NodeOutput, error) {
	*s.sent = append(*s.sent, config)
	return []workflow.NodeOutput{{Handle: "main", Items: []workflow.Item{workflow.NewItem(map[string]interface{}{"success": true})}}}, nil
}

// runEmailWorkflow runs trigger → comm.email_send through the engine with
// Guard installed, as the user's own process (no agent marker), and returns
// what the stub sent and the send's output.
func runEmailWorkflow(t *testing.T, db *sql.DB, approved bool, trigger map[string]interface{}) ([]map[string]interface{}, []workflow.Item) {
	t.Helper()
	cfg := emailConfig()
	cfg["body"] = "{{ $json.body }}" // resolved by the run before Guard sees it
	return runSendWorkflow(t, db, approved, trigger, "comm.email_send", cfg)
}

func runSendWorkflow(t *testing.T, db *sql.DB, approved bool, trigger map[string]interface{}, nodeType string, cfg map[string]interface{}) ([]map[string]interface{}, []workflow.Item) {
	t.Helper()
	workflow.SetExecGuard(Guard)
	t.Cleanup(func() { workflow.SetExecGuard(nil) })
	var sent []map[string]interface{}
	reg := workflow.NewNodeTypeRegistry()
	reg.Register(nodeType, func() workflow.NodeExecutor { return stubSend{typ: nodeType, sent: &sent} })
	wf := &workflow.Workflow{
		ID: "wf1", Name: "outreach",
		Nodes: []workflow.WorkflowNode{
			{ID: "t", WorkflowID: "wf1", Type: "trigger.manual", Name: "T"},
			{ID: "mail", WorkflowID: "wf1", Type: nodeType, Name: "Mail", Config: cfg},
		},
		Connections: []workflow.WorkflowConnection{{SourceNodeID: "t", SourceHandle: "main", TargetNodeID: "mail", TargetHandle: "main"}},
	}
	dag, err := workflow.BuildDAG(wf.Nodes, wf.Connections)
	if err != nil {
		t.Fatal(err)
	}
	if trigger == nil {
		trigger = map[string]interface{}{}
	}
	trigger["body"] = "Hi Ada"
	var out []workflow.Item
	exec := &workflow.WorkflowExecution{ID: "ex1", ProfileID: "p1", WorkflowID: "wf1", TriggerNodeID: "t", TriggerData: trigger}
	store := &capturingStore{engineStore: engineStore{approved: approved}, out: &out}
	if err := workflow.RunExecution(ctxFor(db), exec, wf, dag, reg, store, nil, workflow.NewExpressionEngine(), zerolog.Nop()); err != nil {
		t.Fatalf("run: %v", err)
	}
	return sent, out
}

// capturingStore keeps the send node's output items.
type capturingStore struct {
	engineStore
	out *[]workflow.Item
}

func (s *capturingStore) SetExecutionNodeFinished(_ context.Context, _ string, _ string, items []workflow.Item, _ string) error {
	*s.out = append(*s.out, items...)
	return nil
}

func sendItems(t *testing.T, db *sql.DB) []Payload {
	t.Helper()
	items, err := (Store{db, "p1"}).List(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	return items
}

// A workflow the chat or an agent wrote and the user hasn't approved sends
// nothing on its own, whoever starts the run: its send waits as an agent's
// does, named for the workflow. The user's own workflow sends as before,
// even when the chat starts it (#831 round 7).
func TestUnapprovedWorkflowsSendWaits(t *testing.T) {
	asOperator(t)
	db := testDB(t)
	sent, out := runEmailWorkflow(t, db, false, nil)
	if len(sent) != 0 {
		t.Fatalf("an unapproved workflow sent %d email(s)", len(sent))
	}
	items := sendItems(t, db)
	if len(items) != 1 {
		t.Fatalf("queued %d item(s), want 1", len(items))
	}
	p := items[0]
	if p.State != StatePending || p.Text != "Hi Ada" || p.Recipient.Name != "ada@example.test" ||
		p.PreparedBy != (PreparedBy{AgentID: UnapprovedWorkflowAgent, Role: "unapproved workflow wf1", Workflow: "wf1", System: true}) {
		t.Fatalf("item = %+v", p)
	}
	if len(out) != 1 || out[0].JSON["pending"] != true || out[0].JSON["hil_id"] != p.ID {
		t.Fatalf("the run went on with %+v, want the queued item", out)
	}

	for name, trigger := range map[string]map[string]interface{}{
		"the user's run":       nil,
		"a run the chat began": {workflow.AICallerKey: "chat"},
	} {
		db := testDB(t)
		if sent, _ := runEmailWorkflow(t, db, true, trigger); len(sent) != 1 || len(sendItems(t, db)) != 0 {
			t.Errorf("%s of an approved workflow: sent %d, queued %d", name, len(sent), len(sendItems(t, db)))
		}
	}
}

// A workflow an unapproved one called sends nothing on its own either, even
// when the user approved the called one.
func TestCalledByAnUnapprovedWorkflowWaits(t *testing.T) {
	asOperator(t)
	db := testDB(t)
	sent, _ := runEmailWorkflow(t, db, true, map[string]interface{}{workflow.UnapprovedCallerKey: "parent-wf"})
	items := sendItems(t, db)
	if len(sent) != 0 || len(items) != 1 {
		t.Fatalf("sent %d, queued %d", len(sent), len(items))
	}
	if want := (PreparedBy{AgentID: UnapprovedWorkflowAgent, Role: "workflow wf1, called by unapproved workflow parent-wf", Workflow: "wf1", System: true}); items[0].PreparedBy != want {
		t.Fatalf("preparedBy = %+v, want %+v", items[0].PreparedBy, want)
	}
}

// The machinery is an agent's: the setting off, or a destination the user
// always allows, lets the send go; the user's approval sends exactly the
// queued payload, once.
func TestUnapprovedWorkflowsSendFollowsTheRules(t *testing.T) {
	asOperator(t)

	off := testDB(t)
	if _, err := off.Exec(`INSERT INTO configs (name, config_data) VALUES ('coder_mode', '{"sendApproval":false}')`); err != nil {
		t.Fatal(err)
	}
	if sent, _ := runEmailWorkflow(t, off, false, nil); len(sent) != 1 || len(sendItems(t, off)) != 0 {
		t.Fatalf("setting off: sent %d", len(sent))
	}

	allowed := testDB(t)
	if err := (Store{allowed, "p1"}).Allow(context.Background(), Allow{Action: ActionEmail, Platform: "email", Account: "me@example.test", Recipient: "ada@example.test"}); err != nil {
		t.Fatal(err)
	}
	if sent, _ := runEmailWorkflow(t, allowed, false, nil); len(sent) != 1 {
		t.Fatalf("always allowed: sent %d", len(sent))
	}

	db := testDB(t)
	runEmailWorkflow(t, db, false, nil)
	id := sendItems(t, db)[0].ID
	// The operator's `hil resolve --approve` runs the node as `node run` does
	// in their own process: through Guard, outside any unapproved run.
	var sent []map[string]interface{}
	var runs []Run
	perform := func(ctx context.Context, r Run) (json.RawMessage, error) {
		runs = append(runs, r)
		in := workflow.NodeInput{}
		for _, j := range r.Input {
			in.Items = append(in.Items, workflow.NewItem(j))
		}
		if _, handled, err := Guard(ctxFor(db), r.NodeType, in, r.Config); handled {
			t.Fatalf("the approved send was held again: %v", err)
		}
		stubSend{typ: r.NodeType, sent: &sent}.Execute(ctx, in, r.Config)
		return json.RawMessage(`{"ok":true}`), nil
	}
	for i := 0; i < 2; i++ {
		if _, err := Resolve(context.Background(), Store{db, "p1"}, id, true, Edit{}, false, perform); err != nil {
			t.Fatal(err)
		}
	}
	if len(sent) != 1 || len(runs) != 1 {
		t.Fatalf("approving twice sent %d time(s)", len(sent))
	}
	want := map[string]interface{}{"to": "ada@example.test", "subject": "Hello", "body": "Hi Ada", "from": "me@example.test"}
	for k, v := range want {
		if !reflect.DeepEqual(sent[0][k], v) {
			t.Errorf("sent %s = %v, want %v", k, sent[0][k], v)
		}
	}
	if p, _ := (Store{db, "p1"}).Get(context.Background(), id); p.State != StateSent {
		t.Fatalf("item = %s, want sent", p.State)
	}
}

// A send whose operation only the run resolves is judged on the resolved
// one: Guard sees the config the run resolved.
func TestUnapprovedWorkflowsResolvedOperation(t *testing.T) {
	asOperator(t)
	cfg := map[string]interface{}{"operation": "{{ $json.op }}", "to": "ada@example.test", "subject": "Hello", "body": "Hi"}
	for op, queued := range map[string]bool{"send_message": true, "list_messages": false} {
		db := testDB(t)
		sent, _ := runSendWorkflow(t, db, false, map[string]interface{}{"op": op}, "service.gmail", cfg)
		if got := len(sendItems(t, db)) == 1 && len(sent) == 0; got != queued {
			t.Errorf("operation %s: sent %d, queued %d", op, len(sent), len(sendItems(t, db)))
		}
	}
}

// A run an agent started with `workflow run` (its mark, also from a coder turn without markers) sends
// nothing on its own, even from a workflow the user approved; a chat's run of an approved workflow does.
func TestAMarkedAgentRunOfAnApprovedWorkflowSendWaits(t *testing.T) {
	asOperator(t)
	for _, mark := range []string{"other", "coder", "other/org"} {
		db := testDB(t)
		sent, _ := runEmailWorkflow(t, db, true, map[string]interface{}{workflow.AICallerKey: mark})
		if len(sent) != 0 || len(sendItems(t, db)) != 1 {
			t.Errorf("mark %q: sent %d, queued %d; want the send queued", mark, len(sent), len(sendItems(t, db)))
		}
	}
}
