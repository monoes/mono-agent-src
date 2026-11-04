package control

import (
	"context"
	"errors"
	"testing"

	"github.com/zalando/go-keyring"

	"github.com/monoes/mono-agent/internal/jev/jevconf"
	"github.com/monoes/mono-agent/internal/jev/jevtest"
	"github.com/monoes/mono-agent/internal/jev/jevtest/callers"
	"github.com/monoes/mono-agent/internal/secrets"
	"github.com/monoes/mono-agent/internal/testdb"
	"github.com/monoes/mono-agent/internal/vault"
	"github.com/monoes/mono-agent/internal/workflow"
)

// human_in_loop's auto_decide sends the stored TypeSafe key only to the
// TypeSafe host; an unapproved step reads it only under agent access, audited.
// The node swallows the key's refusal into a note on its rows, so the note is
// what is read.
func TestHILAutoDecideSendsTheStoredKeyOnlyToTheTypeSafeHost(t *testing.T) {
	keyring.MockInit()
	const key = "ts-STORED-KEY-hil-0123456"
	for _, c := range callers.All() {
		t.Run(c.Name, func(t *testing.T) {
			db := testdb.Open(t).DB
			if _, err := secrets.Add(context.Background(), db, jevconf.VaultScope, "secret", jevconf.SecretName, map[string]string{"secret": key}, "", "", ""); err != nil {
				t.Fatal(err)
			}
			if err := jevconf.SetEnabled(db, "default", jevconf.HIL, true); err != nil {
				t.Fatal(err)
			}
			legit, envHost := jevtest.TwoHosts(t)
			c.Prepare(t)
			ctx := vault.ContextWithProfileID(vault.ContextWithDB(context.Background(), db), "default")
			ctx = c.Ctx(t, db, ctx, "default", "control.human_in_loop")
			_, err := (&HumanInLoopNode{}).Execute(ctx, threeItems(), autoConfig(map[string]interface{}{"policy": "ok"}))
			if err != nil && !errors.Is(err, workflow.ErrNodePaused) {
				t.Fatal(err)
			}
			note := ""
			var cfgJSON string
			if db.QueryRow(`SELECT node_config FROM hil_pending LIMIT 1`).Scan(&cfgJSON) == nil {
				note = cfgJSON
			}
			c.StoredKeyReach(t, legit, envHost, key, "@secret:typesafe is not read from the vault", note)
			c.Audited(t, db, "default", jevconf.SecretName)
		})
	}
}
