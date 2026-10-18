package workflow

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"

	"github.com/google/uuid"
	"github.com/rs/zerolog"
	"github.com/zalando/go-keyring"

	"github.com/monoes/mono-agent/internal/secrets"
	"github.com/monoes/mono-agent/internal/vault"
)

// emitNode hands on the items it holds, whatever came in.
type emitNode struct{ items []map[string]interface{} }

func (emitNode) Type() string { return "test.emit" }
func (n emitNode) Execute(context.Context, NodeInput, map[string]interface{}) ([]NodeOutput, error) {
	var out []Item
	for _, m := range n.items {
		out = append(out, NewItem(m))
	}
	return []NodeOutput{{Handle: "main", Items: out}}, nil
}

// subRoot hands its sub-nodes' configs on; with call, it calls its ai_tool
// sub-node once per item, with the item's msg as the tool's q.
type subRoot struct {
	call bool
	got  *[]map[string]interface{}
}

func (subRoot) Type() string { return "test.subroot" }
func (r subRoot) Execute(ctx context.Context, in NodeInput, _ map[string]interface{}) ([]NodeOutput, error) {
	for _, s := range in.SubNodes {
		if !r.call {
			*r.got = append(*r.got, s.Config)
			continue
		}
		for _, it := range in.Items {
			items, err := s.Call(ctx, map[string]interface{}{"q": it.JSON["msg"]})
			if err != nil {
				return nil, err
			}
			for _, out := range items {
				*r.got = append(*r.got, out.JSON)
			}
		}
	}
	return []NodeOutput{{Handle: "main", Items: in.Items}}, nil
}

// pathsFixture is an approved workflow's run over a vault holding vs.
type pathsFixture struct {
	ctx   context.Context
	store *SQLiteWorkflowStore
}

func newPathsFixture(t *testing.T) pathsFixture {
	t.Helper()
	keyring.MockInit()
	db, store := approvalsTestDB(t)
	ctx := vault.ContextWithProfileID(vault.ContextWithDB(context.Background(), db.DB), "default")
	if _, err := secrets.Add(ctx, db.DB, "default", "secret", "vs", map[string]string{"secret": "vsecret-777"}, "", "", ""); err != nil {
		t.Fatal(err)
	}
	return pathsFixture{ctx, store}
}

// run saves nodes behind a manual trigger (the first one connected from it,
// the rest by conns), approves the workflow and runs it with input.
func (f pathsFixture) run(t *testing.T, reg *NodeTypeRegistry, input map[string]interface{}, nodes []WorkflowNode, conns ...WorkflowConnection) error {
	t.Helper()
	id := "wf-" + uuid.NewString()[:8]
	// Node ids are unique across workflows: each is the workflow's id and its own.
	wf := &Workflow{ID: id, Name: id, ProfileID: "default", Nodes: []WorkflowNode{{ID: id + "-t", WorkflowID: id, Type: "trigger.manual", Name: "Start"}}}
	for _, n := range nodes {
		n.ID, n.WorkflowID = id+"-"+n.ID, id
		wf.Nodes = append(wf.Nodes, n)
	}
	wf.Connections = append([]WorkflowConnection{{SourceNodeID: "t", SourceHandle: "main", TargetNodeID: nodes[0].ID, TargetHandle: "main"}}, conns...)
	for i := range wf.Connections {
		c := &wf.Connections[i]
		c.ID, c.WorkflowID, c.SourceNodeID, c.TargetNodeID = fmt.Sprintf("%s-c%d", id, i), id, id+"-"+c.SourceNodeID, id+"-"+c.TargetNodeID
	}
	createSQLWorkflow(t, f.store, wf)
	if err := f.store.ApproveDefinition(f.ctx, id); err != nil {
		t.Fatal(err)
	}
	dag, err := BuildDAG(wf.Nodes, wf.Connections)
	if err != nil {
		t.Fatal(err)
	}
	exec := &WorkflowExecution{ID: uuid.NewString(), WorkflowID: id, ProfileID: "default", TriggerType: "trigger.manual", TriggerNodeID: id + "-t", TriggerData: input}
	return RunExecution(f.ctx, exec, wf, dag, reg, f.store, nil, NewExpressionEngine(), zerolog.Nop())
}

// dataChoices are what data must not choose through a step's config: a
// secret, a vault image and a saved connection, each with the check that
// refuses it.
var dataChoices = []struct {
	name, field, value string
	refused            func(error) bool
}{
	{"a secret", "v", "@secret:vs", func(err error) bool { return errors.Is(err, secrets.ErrDataReference) }},
	{"an image", "v", "@img-1", func(err error) bool { return errors.Is(err, vault.ErrDataReference) }},
	{"a connection", "credential_id", "conn-1", func(err error) bool {
		return err != nil && strings.Contains(err.Error(), `"credential_id": the saved connection must be written in the workflow`)
	}},
}

// Each path that resolves a step's config against data checks it against
// the config as the workflow holds it, for a secret, an image and a saved
// connection alike: run once per item (the second item carries the value,
// so the batch's first-item pass doesn't see it), a sub-node's config, and
// an ai_tool sub-node's config when its root calls it. The workflow's own
// reference on each path still resolves (#845 code review, 3).
func TestEveryConfigPathRefusesWhatDataChooses(t *testing.T) {
	f := newPathsFixture(t)
	if _, err := f.store.RawDB().Exec(`INSERT INTO vault_images (id, seq, path, filename, profile_id) VALUES ('img-1', 1, '/vault/passport.png', 'passport.png', 'default')`); err != nil {
		t.Fatal(err)
	}
	for _, c := range dataChoices {
		t.Run("once per item, "+c.name, func(t *testing.T) {
			var got []map[string]interface{}
			reg := NewNodeTypeRegistry()
			reg.Register("test.emit", func() NodeExecutor {
				return emitNode{[]map[string]interface{}{{"v": ""}, {"v": c.value}}}
			})
			reg.Register("test.configprobe", func() NodeExecutor { return configProbe{&got} })
			err := f.run(t, reg, map[string]interface{}{}, []WorkflowNode{
				{ID: "e", Type: "test.emit", Name: "Emit"},
				{ID: "p", Type: "test.configprobe", Name: "Probe", Config: map[string]interface{}{c.field: "{{ $json.v }}", "own": "@secret:vs", "execute_once_per_item": true}},
			}, WorkflowConnection{SourceNodeID: "e", SourceHandle: "main", TargetNodeID: "p", TargetHandle: "main"})
			if !c.refused(err) {
				t.Errorf("run %v, want the second item refused", err)
			}
			for _, cfg := range got {
				if cfg[c.field] != "" || cfg["own"] != "vsecret-777" {
					t.Errorf("the step ran with %v, want only the first item, with its own reference resolved", cfg)
				}
			}
		})

		t.Run("a sub-node, "+c.name, func(t *testing.T) {
			var got []map[string]interface{}
			reg := NewNodeTypeRegistry()
			reg.Register("test.subroot", func() NodeExecutor { return subRoot{got: &got} })
			reg.Register("test.configprobe", func() NodeExecutor { return configProbe{&got} })
			err := f.run(t, reg, map[string]interface{}{"v": c.value}, []WorkflowNode{
				{ID: "r", Type: "test.subroot", Name: "Root"},
				{ID: "m", Type: "test.configprobe", Name: "Model", Config: map[string]interface{}{c.field: "{{ $json.v }}"}},
			}, WorkflowConnection{SourceNodeID: "m", SourceHandle: "ai_languageModel", TargetNodeID: "r", TargetHandle: "ai_languageModel"})
			if !c.refused(err) || !strings.Contains(err.Error(), "-m (Model)") || len(got) != 0 {
				t.Errorf("run %v, the root got %v", err, got)
			}
		})

		t.Run("an ai_tool its root calls, "+c.name, func(t *testing.T) {
			var got []map[string]interface{}
			reg := NewNodeTypeRegistry()
			reg.Register("test.subroot", func() NodeExecutor { return subRoot{call: true, got: &got} })
			reg.Register("test.configprobe", func() NodeExecutor { return configProbe{&[]map[string]interface{}{}} })
			run := func(msg string) error {
				return f.run(t, reg, map[string]interface{}{"msg": msg}, []WorkflowNode{
					{ID: "r", Type: "test.subroot", Name: "Root"},
					{ID: "w", Type: "test.configprobe", Name: "Tool", Config: map[string]interface{}{c.field: "{{ $json.q }}", "own": "@secret:vs"}},
				}, WorkflowConnection{SourceNodeID: "w", SourceHandle: "ai_tool", TargetNodeID: "r", TargetHandle: "ai_tool"})
			}
			if err := run(c.value); !c.refused(err) || !strings.Contains(err.Error(), `tool "Tool"`) || len(got) != 0 {
				t.Errorf("run %v, the tool gave %v", err, got)
			}
			if err := run(""); err != nil || len(got) != 1 || got[0]["own"] != "vsecret-777" {
				t.Errorf("run %v, the tool gave %v, want its own reference resolved", err, got)
			}
		})
	}
}
