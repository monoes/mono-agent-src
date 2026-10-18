package vault

import (
	"errors"
	"testing"

	"github.com/monoes/mono-agent/internal/secrets"
	"github.com/monoes/mono-agent/internal/workflow"
)

// A workflow's vault.secret_save writes no httpapi-token, the HTTP API's
// bearer, whoever approved it: that is the user's own command's to write
// (#845 security review).
func TestSecretSaveWritesNoHTTPAPIToken(t *testing.T) {
	ctx := newSecretsTestCtx(t)
	in := workflow.NodeInput{Items: []workflow.Item{workflow.NewItem(map[string]interface{}{"token": "workflow-made"})}}
	_, err := (&SecretSaveNode{}).Execute(ctx, in, map[string]interface{}{"name": "httpapi-token", "field_keys": []interface{}{"token"}})
	if !errors.Is(err, secrets.ErrReservedEntry) {
		t.Errorf("saving httpapi-token: %v, want ErrReservedEntry", err)
	}
}
