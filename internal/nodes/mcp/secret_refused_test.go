package mcpnode

import (
	"context"
	"path/filepath"
	"strings"
	"testing"

	"github.com/monoes/mono-agent/internal/secrets"
	"github.com/monoes/mono-agent/internal/storage"
	"github.com/monoes/mono-agent/internal/vault"
)

// A server definition or a header secret this run may not read stops the
// step with why, naming the reference: the refusal is not reported as a
// missing entry (#845 code review, 4).
func TestARefusedServerSecretSaysWhy(t *testing.T) {
	db, err := storage.NewDatabase(filepath.Join(t.TempDir(), "vault.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	ctx := secrets.RefuseResolving(vault.ContextWithProfileID(vault.ContextWithDB(context.Background(), db.DB), "default"), "an agent runs this step")
	for name, c := range map[string]struct {
		config map[string]interface{}
		want   string
	}{
		"a server definition": {map[string]interface{}{"server": "crm"},
			`server "crm": @secret:crm is not read from the vault: an agent runs this step`},
		"a header": {map[string]interface{}{"url": "http://127.0.0.1:1/mcp", "headers": map[string]interface{}{"Authorization": "Bearer @secret:tok"}},
			"Authorization: @secret:tok is not read from the vault: an agent runs this step"},
	} {
		if _, err := loadServer(ctx, c.config); err == nil || !strings.Contains(err.Error(), c.want) {
			t.Errorf("%s: %v, want it to say %q", name, err, c.want)
		}
	}
}
