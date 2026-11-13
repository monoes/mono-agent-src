package data

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

// Issue #1418: a binary payload is content too. A tainted run may not hand a
// file writer a binary property that holds a value it read.
func TestAgentAccessTaintedRunDoesNotWriteASecretInBinaryDataToAFile(t *testing.T) {
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
	ctx := secrets.WithAccessNode(secrets.AllowResolving(base, db.DB, "default", secrets.AccessOrigin{WorkflowID: "wf1"}), "n1", "Write")
	if err := secrets.ResolveConfig(ctx, db.DB, "default", map[string]interface{}{"x": "@secret:vs"}, map[string]interface{}{"x": "@secret:vs"}); err != nil {
		t.Fatal(err)
	}
	cfg := map[string]interface{}{"file_name": filepath.Join(t.TempDir(), "out.bin")}
	secretItem := workflow.Item{JSON: map[string]interface{}{}, Binary: map[string][]byte{"data": []byte("token=vsecret-777")}}
	_, handled, err := workflow.RunExecGuardFor(ctx, &WriteBinaryFileNode{}, workflow.NodeInput{Items: []workflow.Item{secretItem}}, cfg)
	if !handled || !errors.Is(err, secrets.ErrPublishesSecret) {
		t.Errorf("a tainted run handed a file writer binary data holding the secret (issue #1418): handled=%v err=%v", handled, err)
	}
	clean := workflow.Item{JSON: map[string]interface{}{}, Binary: map[string][]byte{"data": []byte("token=other")}}
	if _, handled, err := workflow.RunExecGuardFor(ctx, &WriteBinaryFileNode{}, workflow.NodeInput{Items: []workflow.Item{clean}}, cfg); handled || err != nil {
		t.Errorf("binary data without the secret was held: handled=%v err=%v", handled, err)
	}
}
