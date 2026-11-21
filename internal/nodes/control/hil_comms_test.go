package control

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"path/filepath"
	"strings"
	"testing"

	"github.com/rs/zerolog"

	"github.com/monoes/mono-agent/internal/commsync"
	"github.com/monoes/mono-agent/internal/storage"
	"github.com/monoes/mono-agent/internal/testdb"
	"github.com/monoes/mono-agent/internal/vault"
	"github.com/monoes/mono-agent/internal/workflow"
)

// commsStore is the part of a workflow store a run needs, in memory.
type commsStore struct {
	workflow.WorkflowStore
	db    *sql.DB
	state string
}

func (s *commsStore) RawDB() *sql.DB { return s.db }

func (s *commsStore) CreateExecutionNode(context.Context, *workflow.WorkflowExecutionNode) error {
	return nil
}
func (s *commsStore) SetExecutionNodeFinished(context.Context, string, string, []workflow.Item, string) error {
	return nil
}
func (s *commsStore) SetExecutionWaiting(_ context.Context, _ string, state string) error {
	s.state = state
	return nil
}

// fakeSend stands in for a publishing node; it sends nothing.
type fakeSend struct {
	typ   string
	calls *int
	err   error
}

func (f fakeSend) Type() string { return f.typ }
func (f fakeSend) Execute(_ context.Context, in workflow.NodeInput, _ map[string]interface{}) ([]workflow.NodeOutput, error) {
	*f.calls++
	if f.err != nil {
		return nil, f.err
	}
	return []workflow.NodeOutput{{Handle: "main", Items: in.Items}}, nil
}

type commsRig struct {
	t     *testing.T
	db    *sql.DB
	ctx   context.Context
	wf    *workflow.Workflow
	dag   *workflow.DAG
	reg   *workflow.NodeTypeRegistry
	store *commsStore
	calls int

	trigger map[string]interface{}
}

func newCommsRig(t *testing.T, sendType string, sendConfig map[string]interface{}, sendErr error, trigger map[string]interface{}) *commsRig {
	t.Helper()
	d, err := storage.NewDatabase(filepath.Join(t.TempDir(), "t.db"))
	if err != nil {
		t.Fatal(err)
	}
	if err := d.ApplyMigrations(); err != nil {
		t.Fatal(err)
	}
	testdb.AddDefaultProfile(t, d.DB)
	t.Cleanup(func() { d.DB.Close() })
	r := &commsRig{t: t, db: d.DB, ctx: vault.ContextWithProfileID(vault.ContextWithDB(context.Background(), d.DB), "default"), store: &commsStore{db: d.DB}}
	r.reg = workflow.NewNodeTypeRegistry()
	r.reg.Register("core.human_in_loop", func() workflow.NodeExecutor { return &HumanInLoopNode{} })
	r.reg.Register(sendType, func() workflow.NodeExecutor { return fakeSend{typ: sendType, calls: &r.calls, err: sendErr} })
	r.wf = &workflow.Workflow{
		ID: "wf1", Name: "growth publish",
		Nodes: []workflow.WorkflowNode{
			{ID: "t", WorkflowID: "wf1", Type: "trigger.manual", Name: "T"},
			{ID: "hil", WorkflowID: "wf1", Type: "core.human_in_loop", Name: "Approve", Config: map[string]interface{}{}},
			{ID: "pub", WorkflowID: "wf1", Type: sendType, Name: "Publish", Config: sendConfig},
		},
		Connections: []workflow.WorkflowConnection{
			{SourceNodeID: "t", SourceHandle: "main", TargetNodeID: "hil", TargetHandle: "main"},
			{SourceNodeID: "hil", SourceHandle: "main", TargetNodeID: "pub", TargetHandle: "main"},
		},
	}
	r.dag, err = workflow.BuildDAG(r.wf.Nodes, r.wf.Connections)
	if err != nil {
		t.Fatal(err)
	}
	r.trigger = trigger
	return r
}

func (r *commsRig) run(resume string) error {
	r.t.Helper()
	exec := &workflow.WorkflowExecution{ID: "ex1", ProfileID: "default", WorkflowID: "wf1", TriggerNodeID: "t", TriggerData: r.trigger, ResumeState: resume}
	return workflow.RunExecution(r.ctx, exec, r.wf, r.dag, r.reg, r.store, nil, workflow.NewExpressionEngine(), zerolog.Nop())
}

func (r *commsRig) hilID() string {
	r.t.Helper()
	var id string
	if err := r.db.QueryRow(`SELECT id FROM hil_pending WHERE execution_id = 'ex1'`).Scan(&id); err != nil {
		r.t.Fatal(err)
	}
	return id
}

func (r *commsRig) message() (status, source, body string, person struct{ user, platform, kind, details string }) {
	r.t.Helper()
	err := r.db.QueryRow(`SELECT pm.status, pm.source, COALESCE(pm.body,''), p.platform_username, p.platform, p.kind, COALESCE(p.profile_details,'')
		FROM person_messages pm JOIN people p ON p.id = pm.person_id WHERE pm.hil_ref = ?`, r.hilID()).
		Scan(&status, &source, &body, &person.user, &person.platform, &person.kind, &person.details)
	if err != nil {
		r.t.Fatalf("message: %v", err)
	}
	return
}

func (r *commsRig) decide(approve bool, edited string) {
	r.t.Helper()
	id := r.hilID()
	if approve {
		if edited == "" {
			edited = "{}"
		}
		r.db.Exec(`UPDATE hil_pending SET status='approved', edited_data=? WHERE id=?`, edited, id)
		var m map[string]interface{}
		_ = json.Unmarshal([]byte(edited), &m)
		if err := commsync.SetStatus(r.ctx, r.db, id, commsync.Approved, commsync.Detail{Edited: m}); err != nil {
			r.t.Fatal(err)
		}
		return
	}
	r.db.Exec(`UPDATE hil_pending SET status='rejected' WHERE id=?`, id)
	if err := commsync.SetStatus(r.ctx, r.db, id, commsync.Rejected, commsync.Detail{}); err != nil {
		r.t.Fatal(err)
	}
}

func TestHILQueuesPostWithOwnAccountAndFollowsToSent(t *testing.T) {
	r := newCommsRig(t, "x.publish_post", map[string]interface{}{"account": "acme", "text": "{{ $json.post }}"}, nil,
		map[string]interface{}{"post": "Big news"})
	if err := r.run(""); !errors.Is(err, workflow.ErrExecutionPaused) {
		t.Fatalf("first run = %v", err)
	}
	status, source, body, p := r.message()
	if status != "pending_approval" || source != "x" || body != "Big news" {
		t.Fatalf("queued row = %s %s %q", status, source, body)
	}
	if p.user != "acme" || p.platform != "x" || p.kind != "person" || p.details != `{"own_account":true}` {
		t.Fatalf("own account = %+v", p)
	}

	r.decide(true, `{"post":"Edited news"}`)
	if _, _, body, _ := r.message(); body != "Edited news" {
		t.Fatalf("edited body = %q", body)
	}
	if r.calls != 0 {
		t.Fatal("publish ran before the approval was used")
	}
	if err := r.run(r.store.state); err != nil {
		t.Fatalf("resume = %v", err)
	}
	if status, _, _, _ := r.message(); status != "sent" || r.calls != 1 {
		t.Fatalf("after run: status %s, publish ran %d times", status, r.calls)
	}
	var n int
	r.db.QueryRow(`SELECT COUNT(*) FROM person_messages`).Scan(&n)
	if n != 1 {
		t.Fatalf("%d message rows, want the one that followed the item", n)
	}
}

func TestHILQueuesDMToPersonAndCompany(t *testing.T) {
	r := newCommsRig(t, "linkedin.send_dms", map[string]interface{}{"messageText": "Hello"}, nil,
		map[string]interface{}{"name": "Acme Inc", "profileUrl": "https://www.linkedin.com/company/acme-inc/", "messageText": "Hello Acme"})
	if err := r.run(""); !errors.Is(err, workflow.ErrExecutionPaused) {
		t.Fatalf("first run = %v", err)
	}
	status, source, body, p := r.message()
	if status != "pending_approval" || source != "linkedin" || body != "Hello" {
		t.Fatalf("row = %s %s %q", status, source, body)
	}
	if p.user != "acme-inc" || p.kind != "company" {
		t.Fatalf("company = %+v", p)
	}

	r2 := newCommsRig(t, "instagram.send_dms", map[string]interface{}{}, nil,
		map[string]interface{}{"username": "ada", "fullName": "Ada L", "messageText": "Hi Ada"})
	r2.run("")
	status, _, body, p = r2.message()
	if status != "pending_approval" || body != "Hi Ada" || p.user != "ada" || p.kind != "person" || p.platform != "instagram" {
		t.Fatalf("dm = %s %q %+v", status, body, p)
	}
}

func TestHILRejectNeverSends(t *testing.T) {
	r := newCommsRig(t, "x.publish_post", map[string]interface{}{"text": "{{ $json.post }}"}, nil, map[string]interface{}{"post": "no"})
	r.run("")
	r.decide(false, "")
	if err := r.run(r.store.state); err == nil {
		t.Fatal("a rejected item must stop the run")
	}
	if status, _, _, _ := r.message(); status != "rejected" || r.calls != 0 {
		t.Fatalf("status %s, publish ran %d times", status, r.calls)
	}
}

func TestHILSendFailureMarksFailed(t *testing.T) {
	r := newCommsRig(t, "x.publish_post", map[string]interface{}{"text": "{{ $json.post }}"}, errors.New("429 rate limited"),
		map[string]interface{}{"post": "hi"})
	r.run("")
	r.decide(true, "")
	if err := r.run(r.store.state); err == nil {
		t.Fatal("expected the publish error")
	}
	status, _, _, _ := r.message()
	if status != "failed" {
		t.Fatalf("status = %s", status)
	}
	var meta string
	r.db.QueryRow(`SELECT metadata FROM person_messages WHERE hil_ref = ?`, r.hilID()).Scan(&meta)
	if !strings.Contains(meta, "429 rate limited") {
		t.Fatalf("error not kept: %s", meta)
	}
}

func TestHILWithoutOutboundNodeQueuesNothing(t *testing.T) {
	r := newCommsRig(t, "test.review", map[string]interface{}{}, nil, map[string]interface{}{"lead": "x"})
	r.run("")
	var n int
	r.db.QueryRow(`SELECT COUNT(*) FROM person_messages`).Scan(&n)
	if n != 0 {
		t.Fatalf("%d rows for a review that sends nothing", n)
	}
}

func TestHILTimeoutExpiresMessage(t *testing.T) {
	r := newCommsRig(t, "x.publish_post", map[string]interface{}{"text": "{{ $json.post }}"}, nil, map[string]interface{}{"post": "late"})
	r.wf.Nodes[1].Config = map[string]interface{}{"timeout_minutes": float64(1)}
	r.dag, _ = workflow.BuildDAG(r.wf.Nodes, r.wf.Connections)
	r.run("")
	r.db.Exec(`UPDATE hil_pending SET created_at = datetime('now','-10 minutes')`)
	if err := r.run(r.store.state); err == nil {
		t.Fatal("a timed-out item must stop the run")
	}
	if status, _, _, _ := r.message(); status != "expired" {
		t.Fatalf("status = %s", status)
	}
}
