package hil

import (
	"context"
	"strings"
	"testing"

	"github.com/zalando/go-keyring"

	"github.com/monoes/mono-agent/internal/secrets"
	"github.com/monoes/mono-agent/internal/storage"
	"github.com/monoes/mono-agent/internal/vault"
)

// #1594: a card an older version wrote with a granted secret's value in it is
// clean by the time `hil list` prints it.
func TestHILListScrubsAnOlderCardHoldingAGrantedSecret(t *testing.T) {
	keyring.MockInit()
	cfg := &testConfig{DBPath: newHILTestDB(t), JSONOutput: true, ProfileID: "default"}
	sdb, err := storage.NewDatabase(cfg.DBPath)
	if err != nil {
		t.Fatal(err)
	}
	if err := sdb.ApplyMigrations(); err != nil {
		t.Fatal(err)
	}
	ctx := vault.ContextWithProfileID(vault.ContextWithDB(context.Background(), sdb.DB), "default")
	const value = "vsecret-777"
	if _, err := secrets.Add(ctx, sdb.DB, "default", "secret", "vs", map[string]string{"secret": value}, "", "", ""); err != nil {
		t.Fatal(err)
	}
	if err := secrets.SetAgentAccessGrant(ctx, sdb.DB, "default", secrets.AccessGrant{AnyHost: true}); err != nil {
		t.Fatal(err)
	}
	if _, err := sdb.DB.Exec(`INSERT INTO hil_pending (id, execution_id, workflow_id, node_id, node_name, status, readonly_data, editable_data, node_config, profile_id)
		VALUES ('h1', 'ex-t', 'wf-t', 'n', 'Review', 'pending', '{}', ?, '{"outbound":false}', 'default')`, `{"note":"key is `+value+`"}`); err != nil {
		t.Fatal(err)
	}
	// The card is an older one: the scrub's cutoff lies after it.
	if _, err := sdb.DB.Exec(`INSERT OR REPLACE INTO settings (key, value) VALUES ('hilscrub.cutoff', datetime('now','+1 day'))`); err != nil {
		t.Fatal(err)
	}
	sdb.Close()
	items, err := runHIL(t, cfg, "list")
	if err != nil || len(items) != 1 {
		t.Fatalf("%+v %v", items, err)
	}
	for k, v := range items[0].EditableData {
		if s, _ := v.(string); strings.Contains(s, value) {
			t.Errorf("hil list printed the value in %s: %q", k, s)
		}
	}
}
