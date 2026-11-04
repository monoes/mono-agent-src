package choose

import (
	"context"
	"testing"

	"github.com/zalando/go-keyring"

	"github.com/monoes/mono-agent/internal/jev/jevconf"
	"github.com/monoes/mono-agent/internal/jev/jevtest/callers"
	"github.com/monoes/mono-agent/internal/secrets"
	"github.com/monoes/mono-agent/internal/testdb"
	"github.com/monoes/mono-agent/internal/vault"
	"github.com/monoes/mono-agent/internal/workflow"
)

// The stored TypeSafe key (no api_key in the step) goes only to the TypeSafe
// host: not to the TYPESAFE_BASE_URL of a marked process, a chat, a workflow's
// child, a coder turn that cleared its markers or an unapproved step; an
// unapproved step with agent access off does not read it at all, and under
// agent access it is read through the grant and audited.
func TestChooseSendsTheStoredKeyOnlyToTheTypeSafeHost(t *testing.T) {
	keyring.MockInit()
	const key = "ts-STORED-KEY-choose-0123"
	for _, mode := range callers.Modes {
		for _, c := range callers.All() {
			t.Run(callers.ModeName(mode)+"/"+c.Name, func(t *testing.T) {
				db := testdb.Open(t).DB
				if _, err := secrets.Add(context.Background(), db, jevconf.VaultScope, "secret", jevconf.SecretName, map[string]string{"secret": key}, "", "", ""); err != nil {
					t.Fatal(err)
				}
				verify, _ := callers.Hosts(t, mode)
				c.Prepare(t)
				ctx := vault.ContextWithProfileID(vault.ContextWithDB(context.Background(), db), "default")
				ctx = c.Ctx(t, db, ctx, "default", NodeType)
				cfg := baseConfig()
				delete(cfg, "api_key")
				_, err := (&Node{}).Execute(ctx, workflow.NodeInput{Items: items("about billing")}, cfg)
				got := ""
				if err != nil {
					got = err.Error()
				}
				verify(c, key, "@secret:typesafe is not read from the vault", got)
				c.Audited(t, db, "default", jevconf.SecretName)
			})
		}
	}
}
