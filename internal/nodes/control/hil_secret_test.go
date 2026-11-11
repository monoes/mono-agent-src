package control

import (
	"context"
	"path/filepath"
	"strings"
	"testing"

	"github.com/zalando/go-keyring"

	"github.com/monoes/mono-agent/internal/secrets"
	"github.com/monoes/mono-agent/internal/storage"
	"github.com/monoes/mono-agent/internal/vault"
	"github.com/monoes/mono-agent/internal/workflow"
)

// #1594: a card of a workflow's HIL node does not keep a value the run read
// under agent access; nothing is queued.
func TestHILQueuesNoCardHoldingAGrantedSecret(t *testing.T) {
	keyring.MockInit()
	d, err := storage.NewDatabase(filepath.Join(t.TempDir(), "v.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer d.Close()
	if err := d.ApplyMigrations(); err != nil {
		t.Fatal(err)
	}
	base := vault.ContextWithProfileID(vault.ContextWithDB(context.Background(), d.DB), "default")
	if _, err := secrets.Add(base, d.DB, "default", "secret", "vs", map[string]string{"secret": "vsecret-777"}, "", "", ""); err != nil {
		t.Fatal(err)
	}
	if err := secrets.SetAgentAccessGrant(base, d.DB, "default", secrets.AccessGrant{AnyHost: true}); err != nil {
		t.Fatal(err)
	}
	ctx := secrets.WithAccessNode(secrets.AllowResolving(base, d.DB, "default", secrets.AccessOrigin{WorkflowID: "wf"}), "n0", "Read")
	v, err := secrets.Resolve(ctx, d.DB, "default", "@secret:vs")
	if err != nil {
		t.Fatal(err)
	}
	in := workflow.NodeInput{ExecutionID: "e1", WorkflowID: "w1", NodeID: "n1", NodeName: "Review",
		Items: []workflow.Item{workflow.NewItem(map[string]interface{}{"caption": "key is " + v})}}
	_, err = (&HumanInLoopNode{}).Execute(ctx, in, map[string]interface{}{})
	if err == nil || !strings.Contains(err.Error(), "does not keep one") {
		t.Fatalf("err = %v", err)
	}
	var n int
	_ = d.DB.QueryRow(`SELECT COUNT(*) FROM hil_pending`).Scan(&n)
	if n != 0 {
		t.Errorf("%d rows queued", n)
	}
}
