package hilsend

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

// Issue #1418 (the card question): a send an agent's run holds for approval
// is stored in the queue. A granted secret the run read and put into the
// message must not be kept there in the clear: the card is persisted and is
// shown to, and kept for, a person who never granted anything.
// Red until the card masks what the run read (it stores the resolved text).
func TestAHeldSendDoesNotPersistAGrantedSecretInTheQueue(t *testing.T) {
	keyring.MockInit()
	asAgent(t)
	db, err := storage.NewDatabase(filepath.Join(t.TempDir(), "vault.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	if err := db.ApplyMigrations(); err != nil {
		t.Fatal(err)
	}
	const value = "vsecret-777"
	base := vault.ContextWithProfileID(vault.ContextWithDB(context.Background(), db.DB), "default")
	if _, err := secrets.Add(base, db.DB, "default", "secret", "vs", map[string]string{"secret": value}, "", "", ""); err != nil {
		t.Fatal(err)
	}
	if err := secrets.SetAgentAccessGrant(base, db.DB, "default", secrets.AccessGrant{AnyHost: true}); err != nil {
		t.Fatal(err)
	}
	ctx := secrets.WithAccessNode(secrets.AllowResolving(base, db.DB, "default", secrets.AccessOrigin{WorkflowID: "wf"}), "n1", "Post")
	resolved, err := secrets.Resolve(ctx, db.DB, "default", "@secret:vs")
	if err != nil || resolved != value {
		t.Fatalf("resolving the grant: %q %v", resolved, err)
	}

	// The engine hands Guard the resolved config.
	cfg := map[string]interface{}{"operation": "post_message", "channel": "#general", "text": "key is " + resolved}
	outs, handled, err := Guard(ctx, "comm.slack", workflow.NodeInput{}, cfg)
	if err != nil || !handled || len(outs) == 0 {
		t.Fatalf("the send was not held for a card: handled=%v outs=%d err=%v", handled, len(outs), err)
	}

	rows, err := db.DB.Query(`SELECT COALESCE(readonly_data,'') || ' ' || COALESCE(editable_data,'') || ' ' || COALESCE(node_config,'') FROM hil_pending`)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	n := 0
	for rows.Next() {
		var all string
		if err := rows.Scan(&all); err != nil {
			t.Fatal(err)
		}
		n++
		if strings.Contains(all, value) {
			t.Errorf("the queued card holds the granted secret in the clear (issue #1418): %s", all)
		}
	}
	if n == 0 {
		t.Fatal("no card was queued")
	}
}
