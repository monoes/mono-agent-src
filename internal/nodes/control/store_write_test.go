package control

import (
	"context"
	"errors"
	"path/filepath"
	"testing"

	"github.com/zalando/go-keyring"

	"github.com/monoes/mono-agent/internal/secrets"
	"github.com/monoes/mono-agent/internal/storage"
	"github.com/monoes/mono-agent/internal/vault"
	"github.com/monoes/mono-agent/internal/workflow"
)

// Issue #1418 (cross-run persistence): the taint is per run, but a run's
// output persists in stores another run reads. A tainted run may not write a
// value it read into a local store: the next run would read the row back
// with a clean access state and publish it.
func TestAgentAccessTaintedRunDoesNotWriteASecretToALocalStore(t *testing.T) {
	keyring.MockInit()
	db, err := storage.NewDatabase(filepath.Join(t.TempDir(), "vault.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	if err := db.ApplyMigrations(); err != nil {
		t.Fatal(err)
	}
	base := vault.ContextWithProfileID(vault.ContextWithDB(context.Background(), db.DB), "default")
	if _, err := secrets.Add(base, db.DB, "default", "secret", "vs", map[string]string{"secret": "vsecret-777"}, "", "", ""); err != nil {
		t.Fatal(err)
	}
	if err := secrets.SetAgentAccessGrant(base, db.DB, "default", secrets.AccessGrant{AnyHost: true}); err != nil {
		t.Fatal(err)
	}
	ctx := secrets.WithAccessNodeType(secrets.WithAccessNode(secrets.AllowResolving(base, db.DB, "default", secrets.AccessOrigin{WorkflowID: "wf1"}), "n1", "Table"), "core.data_table")
	cfg := map[string]interface{}{"x": "@secret:vs"}
	if err := secrets.ResolveConfig(ctx, db.DB, "default", map[string]interface{}{"x": "@secret:vs"}, cfg); err != nil {
		t.Fatal(err)
	}
	node := &DataTableNode{}
	insert := map[string]interface{}{"operation": "insert", "table": "T", "columns_mode": "auto"}
	row := workflow.NodeInput{Items: []workflow.Item{workflow.NewItem(map[string]interface{}{"k": cfg["x"]})}}

	_, handled, err := workflow.RunExecGuardFor(ctx, node, row, insert)
	if !handled || !errors.Is(err, secrets.ErrPublishesSecret) {
		t.Errorf("a tainted run wrote the secret into a data table (issue #1418): handled=%v err=%v", handled, err)
	}
	// A row without it is written: the control.
	clean := workflow.NodeInput{Items: []workflow.Item{workflow.NewItem(map[string]interface{}{"k": "status: done"})}}
	if _, handled, err := workflow.RunExecGuardFor(ctx, node, clean, insert); handled || err != nil {
		t.Errorf("a row without the secret was held: handled=%v err=%v", handled, err)
	}
}

// A per-item field the node evaluates itself ({{ $node["Src"].json.v }})
// reaches the output of another node, which is not the node's input: the
// write is refused all the same, and the next run reads no row.
func TestAgentAccessTaintedRunDoesNotWriteAnotherNodesSecretToALocalStore(t *testing.T) {
	keyring.MockInit()
	db, err := storage.NewDatabase(filepath.Join(t.TempDir(), "vault.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	if err := db.ApplyMigrations(); err != nil {
		t.Fatal(err)
	}
	base := vault.ContextWithProfileID(vault.ContextWithDB(context.Background(), db.DB), "default")
	if _, err := secrets.Add(base, db.DB, "default", "secret", "vs", map[string]string{"secret": "vsecret-777"}, "", "", ""); err != nil {
		t.Fatal(err)
	}
	if err := secrets.SetAgentAccessGrant(base, db.DB, "default", secrets.AccessGrant{AnyHost: true}); err != nil {
		t.Fatal(err)
	}
	run := func(wf string) context.Context {
		return secrets.WithAccessNodeType(secrets.WithAccessNode(secrets.AllowResolving(base, db.DB, "default", secrets.AccessOrigin{WorkflowID: wf}), "n1", "Table"), "core.data_table")
	}
	c1 := run("wf1")
	dtRun(t, c1, nil, map[string]interface{}{"resource": "table", "operation": "create", "table": "T", "columns": `[{"name":"k","type":"string"}]`})
	if err := secrets.ResolveConfig(c1, db.DB, "default", map[string]interface{}{"x": "@secret:vs"}, map[string]interface{}{"x": "@secret:vs"}); err != nil {
		t.Fatal(err)
	}
	in := workflow.NodeInput{Items: []workflow.Item{workflow.NewItem(map[string]interface{}{"n": 1})},
		NodeOutputs: map[string][]workflow.Item{"Src": {workflow.NewItem(map[string]interface{}{"v": "vsecret-777"})}}}
	cfg := map[string]interface{}{"operation": "insert", "table": "T", "values": map[string]interface{}{"k": `{{ $node["Src"].json.v }}`}}
	// The engine runs the node unless the guard held it.
	_, handled, gerr := workflow.RunExecGuardFor(c1, &DataTableNode{}, in, cfg)
	if !handled {
		_, _ = (&DataTableNode{}).Execute(c1, in, cfg)
	}
	if !errors.Is(gerr, secrets.ErrPublishesSecret) {
		t.Errorf("a tainted run wrote another node's secret into a data table (issue #1418): handled=%v err=%v", handled, gerr)
	}
	// The next run, with a clean access state, finds no row.
	rows := dtRun(t, run("wf2"), nil, map[string]interface{}{"operation": "get", "table": "T"})
	if len(rows) != 0 {
		t.Errorf("the next run reads %v", rows)
	}
}
