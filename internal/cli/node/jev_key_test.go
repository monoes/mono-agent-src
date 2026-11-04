package node

import (
	"context"
	"strings"
	"testing"

	"github.com/zalando/go-keyring"

	"github.com/monoes/mono-agent/internal/cli/opgate"
	"github.com/monoes/mono-agent/internal/jev/jevconf"
	"github.com/monoes/mono-agent/internal/jev/jevtest"
	"github.com/monoes/mono-agent/internal/secrets"
	"github.com/monoes/mono-agent/internal/storage"
)

// `node run ai.choose` with the TypeSafe key stored in the vault: an agent's
// run is an unapproved step, so it does not read the key (agent access off),
// and the key never goes to the TYPESAFE_BASE_URL of the caller's environment
// (the user's own run, a marked one or a coder turn's alike).
func TestNodeRunDoesNotGiveTheStoredTypeSafeKeyToAnAgentOrItsHost(t *testing.T) {
	keyring.MockInit()
	const key = "ts-STORED-KEY-noderun-0123"
	payload := `{"config":{"input":"x","cases":["a","b"]}}`
	for _, c := range []struct {
		name string
		prep func(t *testing.T)
		sent bool // the key reaches the TypeSafe host
	}{
		{"operator", func(t *testing.T) {}, true},
		{"marked agent", func(t *testing.T) { t.Setenv("CLAUDECODE", "1") }, false},
		{"coder turn that cleared its markers", func(t *testing.T) {
			opgate.SetForTest(t, func(bool) string { return "a coder session's turn runs this command" })
		}, true}, // node run's own marker check is #1413's: until then the key is read, but only to the TypeSafe host
	} {
		t.Run(c.name, func(t *testing.T) {
			dbPath := nodeVaultTestDB(t)
			db, err := storage.NewDatabase(dbPath)
			if err != nil {
				t.Fatal(err)
			}
			if err := db.ApplyMigrations(); err != nil {
				t.Fatal(err)
			}
			if _, err := secrets.Add(context.Background(), db.DB, jevconf.VaultScope, "secret", jevconf.SecretName, map[string]string{"secret": key}, "", "", ""); err != nil {
				t.Fatal(err)
			}
			db.Close()
			legit, envHost := jevtest.TwoHosts(t)
			c.prep(t)
			out, err := runNodeSubcmdStdin(t, testDeps(dbPath, "default", false), payload, "ai.choose")
			if err != nil {
				out += err.Error()
			}
			if envHost.Calls() != 0 || envHost.Saw(key) {
				t.Errorf("the stored key reached the environment's host")
			}
			if legit.Saw(key) != c.sent {
				t.Errorf("TypeSafe host saw the key = %v, want %v", legit.Saw(key), c.sent)
			}
			if !c.sent && !strings.Contains(out, "@secret:typesafe is not read from the vault") {
				t.Errorf("an agent's run was not refused the key:\n%s", out)
			}
			if strings.Contains(out, key) {
				t.Errorf("the key is in the output")
			}
		})
	}
}
