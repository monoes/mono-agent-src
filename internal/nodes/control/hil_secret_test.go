package control

import (
	"context"
	"path/filepath"
	"strings"
	"testing"

	"github.com/zalando/go-keyring"

	"github.com/monoes/mono-agent/internal/commsync"
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

// The Communications row is built from the whole item through the next
// node's template: a value in a field the card does not show reaches it.
func TestHILQueuesNothingWhenAFieldOutsideTheCardCarriesASecret(t *testing.T) {
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
	v, _ := secrets.Resolve(ctx, d.DB, "default", "@secret:vs")
	ctx = commsync.WithDownstream(ctx, "wf", []commsync.Node{{ID: "pub", Name: "Publish", Type: "comm.slack",
		Config: map[string]interface{}{"operation": "post_message", "channel": "#g", "text": "{{ $json.note }}"}}})
	in := workflow.NodeInput{ExecutionID: "e1", WorkflowID: "w1", NodeID: "n1", NodeName: "Review",
		Items: []workflow.Item{workflow.NewItem(map[string]interface{}{"caption": "hi", "note": "key is " + v})}}
	_, err = (&HumanInLoopNode{}).Execute(ctx, in, map[string]interface{}{"editable_fields": []interface{}{"caption"}})
	var n, m int
	_ = d.DB.QueryRow(`SELECT COUNT(*) FROM hil_pending`).Scan(&n)
	_ = d.DB.QueryRow(`SELECT COUNT(*) FROM person_messages WHERE body LIKE '%vsecret-777%'`).Scan(&m)
	if err == nil || n != 0 || m != 0 {
		t.Errorf("err=%v cards=%d messages holding the value=%d", err, n, m)
	}
}
