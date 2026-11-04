package browserjev

import (
	"context"
	"strings"
	"testing"

	"github.com/zalando/go-keyring"

	"github.com/monoes/mono-agent/internal/jev/jevconf"
	"github.com/monoes/mono-agent/internal/jev/jevtest/callers"
	"github.com/monoes/mono-agent/internal/secrets"
	"github.com/monoes/mono-agent/internal/testdb"
	"github.com/monoes/mono-agent/internal/vault"
	"github.com/monoes/mono-agent/internal/workflow"
)

// browser.jev with no api_key sends the stored TypeSafe key only to the
// TypeSafe host, and an unapproved step reads it only under agent access,
// audited (see choose's test of the same name).
func TestBrowserJevSendsTheStoredKeyOnlyToTheTypeSafeHost(t *testing.T) {
	keyring.MockInit()
	const key = "ts-STORED-KEY-browserjev-0123"
	for _, mode := range callers.Modes {
		for _, c := range callers.All() {
			t.Run(callers.ModeName(mode)+"/"+c.Name, func(t *testing.T) {
				db := testdb.Open(t).DB
				if _, err := secrets.Add(context.Background(), db, jevconf.VaultScope, "secret", jevconf.SecretName, map[string]string{"secret": key}, "", "", ""); err != nil {
					t.Fatal(err)
				}
				verify, legit := callers.Hosts(t, mode)
				c.Prepare(t)
				ctx := vault.ContextWithProfileID(vault.ContextWithDB(context.Background(), db), "default")
				ctx = c.Ctx(t, db, ctx, "default", "browser.jev")
				_, err := testNode(&fakeDriver{}, zurich).Execute(ctx, workflow.NodeInput{},
					map[string]interface{}{"url": "https://example.test/", "goal": "Search for zurich"})
				got := ""
				if err != nil {
					got = err.Error()
				}
				if strings.Contains(got, "started this run") {
					// The node's own gate refuses an AI caller's run before the key
					// is looked up: nothing is sent anywhere.
					if legit.Calls() != 0 {
						t.Errorf("a run the node refused sent %d request(s)", legit.Calls())
					}
					return
				}
				verify(c, key, "@secret:typesafe is not read from the vault", got)
				c.Audited(t, db, "default", jevconf.SecretName)
			})
		}
	}
}
