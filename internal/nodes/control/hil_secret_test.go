package control

import (
	"context"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"net/url"
	"path/filepath"
	"strings"
	"testing"

	"github.com/zalando/go-keyring"

	"github.com/monoes/mono-agent/internal/commsync"
	"github.com/monoes/mono-agent/internal/jev/jevconf"
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

// Each form of a secret the node can see is refused by the tracked-value check
// (its message names it); a value cut in two is not seen by it, which the taint
// guard of #1418 covers.
func TestHILRefusesEachEncodedFormOfASecretByTheTrackedValueCheck(t *testing.T) {
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
	const v = `vsecret-777"x`
	if _, err := secrets.Add(base, d.DB, "default", "secret", "vs", map[string]string{"secret": v}, "", "", ""); err != nil {
		t.Fatal(err)
	}
	if err := secrets.SetAgentAccessGrant(base, d.DB, "default", secrets.AccessGrant{AnyHost: true}); err != nil {
		t.Fatal(err)
	}
	ctx := secrets.WithAccessNode(secrets.AllowResolving(base, d.DB, "default", secrets.AccessOrigin{WorkflowID: "wf"}), "n0", "Read")
	if _, err := secrets.Resolve(ctx, d.DB, "default", "@secret:vs"); err != nil {
		t.Fatal(err)
	}
	forms := map[string]string{
		"plain": v, "base64": base64.StdEncoding.EncodeToString([]byte(v)), "hex": hex.EncodeToString([]byte(v)),
		"url": url.QueryEscape(v), "json-escaped": strings.Trim(mustJSON(v), `"`),
	}
	for name, form := range forms {
		in := workflow.NodeInput{ExecutionID: "e-" + name, WorkflowID: "w1", NodeID: "n1", NodeName: "Review",
			Items: []workflow.Item{workflow.NewItem(map[string]interface{}{"note": "x " + form})}}
		_, err := (&HumanInLoopNode{}).Execute(ctx, in, map[string]interface{}{})
		if err == nil || !strings.Contains(err.Error(), "does not keep one") {
			t.Errorf("%s: %v", name, err)
		}
	}
	in := workflow.NodeInput{ExecutionID: "e-split", WorkflowID: "w1", NodeID: "n1", NodeName: "Review",
		Items: []workflow.Item{workflow.NewItem(map[string]interface{}{"a": v[:5], "b": v[5:]})}}
	if _, err := (&HumanInLoopNode{}).Execute(ctx, in, map[string]interface{}{}); err != nil && strings.Contains(err.Error(), "does not keep one") {
		t.Errorf("a value cut in two should not be seen by this check: %v", err)
	}
}

func mustJSON(s string) string { b, _ := json.Marshal(s); return string(b) }

// Issue #1418 (merge review): with auto_decide on, the card's data goes to
// the remote Jev API before it is stored. A run that read a granted secret
// sends nothing: the card scan comes first.
func TestHILAutoDecideSendsNothingToJevForAGrantedSecret(t *testing.T) {
	keyring.MockInit()
	d, err := storage.NewDatabase(filepath.Join(t.TempDir(), "v.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer d.Close()
	if err := d.ApplyMigrations(); err != nil {
		t.Fatal(err)
	}
	srv := jevByCaption(t, nil)
	if err := jevconf.SetEnabled(d.DB, "default", jevconf.HIL, true); err != nil {
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
		Items: []workflow.Item{workflow.NewItem(map[string]interface{}{"caption": "key is " + v, "to": "a@example.com"})}}
	_, err = (&HumanInLoopNode{}).Execute(ctx, in, autoConfig(map[string]interface{}{"threshold": 0.5}))
	if err == nil || !strings.Contains(err.Error(), "does not keep one") {
		t.Fatalf("err = %v, want the card refusal", err)
	}
	if srv.Calls() != 0 || strings.Contains(srv.RequestJSON(), "vsecret-777") {
		t.Errorf("the item reached Jev before the refusal (issue #1418): %d call(s)", srv.Calls())
	}
}
