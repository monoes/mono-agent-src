package node

import (
	"context"
	"strings"
	"testing"

	"github.com/zalando/go-keyring"

	"github.com/monoes/mono-agent/internal/cli/opgate"
	"github.com/monoes/mono-agent/internal/jev/jevconf"
	"github.com/monoes/mono-agent/internal/jev/jevtest"
	"github.com/monoes/mono-agent/internal/jev/jevtest/callers"
	"github.com/monoes/mono-agent/internal/secrets"
	"github.com/monoes/mono-agent/internal/storage"
)

// `node run ai.choose` with the TypeSafe key stored in the vault: an agent's
// run is an unapproved step, so it does not read the key (agent access off),
// and the key never goes to the TYPESAFE_BASE_URL of the caller's environment
// (the user's own run, a marked one or a coder turn's alike). A coder turn that
// cleared its markers gives the same result as a marked agent.
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
		}, false}, // since #1413 node run decides once with opgate.Refusal: an unmarked in-turn process is an agent, exactly like the marked one
	} {
		for _, override := range []bool{true, false} {
			t.Run(callers.ModeName(override)+"/"+c.name, func(t *testing.T) {
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
				var legit, envHost *jevtest.Probe
				if override {
					legit, envHost = jevtest.TwoHosts(t)
				} else {
					legit = jevtest.TypeSafeHost(t)
				}
				c.prep(t)
				out, err := runNodeSubcmdStdin(t, testDeps(dbPath, "default", false), payload, "ai.choose")
				if err != nil {
					out += err.Error()
				}
				if envHost != nil && (envHost.Calls() != 0 || envHost.Saw(key)) {
					t.Errorf("the stored key reached the environment's host")
				}
				switch {
				case !c.sent && !strings.Contains(out, "@secret:typesafe is not read from the vault"):
					t.Errorf("an agent's run was not refused the key:\n%s", out)
				case c.sent && override && (legit.Saw(key) || !strings.Contains(out, "TYPESAFE_BASE_URL ignored")):
					t.Errorf("a stored key with an override set was sent or not refused:\n%s", out)
				case c.sent && !override && !legit.Saw(key):
					t.Errorf("TypeSafe host did not see the key")
				}
				if strings.Contains(out, key) {
					t.Errorf("the key is in the output")
				}
			})
		}
	}
}

// With an agent-access grant for `typesafe` the key is usable by an agent's run, but still only at the
// default TypeSafe host (never the environment's), and each use is audited: a marked agent and a coder turn that
// cleared its markers give IDENTICAL results; the operator is unchanged (no audit row).
func TestNodeRunWithATypeSafeGrantSendsTheKeyOnlyToTheDefaultHostAndAudits(t *testing.T) {
	keyring.MockInit()
	const key = "ts-STORED-KEY-noderun-grant-0123"
	payload := `{"config":{"input":"x","cases":["a","b"]}}`
	type result struct {
		legitSaw, envCalls bool
		refused            bool
		audit              int
	}
	results := map[string]result{}
	for _, c := range []struct {
		name      string
		prep      func(t *testing.T)
		wantAudit int
	}{
		{"operator", func(t *testing.T) {}, 0},
		{"marked agent", func(t *testing.T) { t.Setenv("CLAUDECODE", "1") }, 1},
		{"coder turn that cleared its markers", func(t *testing.T) {
			opgate.SetForTest(t, func(bool) string { return "a coder session's turn runs this command" })
		}, 1},
	} {
		for _, override := range []bool{true, false} {
			name := callers.ModeName(override) + "/" + c.name
			t.Run(name, func(t *testing.T) {
				dbPath := nodeVaultTestDB(t)
				db, err := storage.NewDatabase(dbPath)
				if err != nil {
					t.Fatal(err)
				}
				if err := db.ApplyMigrations(); err != nil {
					t.Fatal(err)
				}
				ctx := context.Background()
				if _, err := secrets.Add(ctx, db.DB, jevconf.VaultScope, "secret", jevconf.SecretName, map[string]string{"secret": key}, "", "", ""); err != nil {
					t.Fatal(err)
				}
				if err := secrets.SetAgentAccessGrant(ctx, db.DB, "default", secrets.AccessGrant{AnyHost: true, Secrets: []string{jevconf.SecretName}}); err != nil {
					t.Fatal(err)
				}
				db.Close()
				var legit, envHost *jevtest.Probe
				if override {
					legit, envHost = jevtest.TwoHosts(t)
				} else {
					legit = jevtest.TypeSafeHost(t)
				}
				c.prep(t)
				out, err := runNodeSubcmdStdin(t, testDeps(dbPath, "default", false), payload, "ai.choose")
				if err != nil {
					out += err.Error()
				}
				if envHost != nil && (envHost.Calls() != 0 || envHost.Saw(key)) {
					t.Errorf("the stored key reached the environment's host")
				}
				if strings.Contains(out, key) {
					t.Errorf("the key is in the output")
				}
				db2, err := storage.NewDatabase(dbPath)
				if err != nil {
					t.Fatal(err)
				}
				defer db2.Close()
				rows, _ := secrets.AgentAccessAudit(ctx, db2.DB, "default")
				r := result{legitSaw: legit.Saw(key), envCalls: envHost != nil && envHost.Calls() != 0,
					refused: strings.Contains(out, "TYPESAFE_BASE_URL ignored"), audit: len(rows)}
				results[name] = r
				switch {
				case override && (r.legitSaw || !r.refused):
					t.Errorf("a stored key with an override set was sent or not refused (%+v):\n%s", r, out)
				case !override && !r.legitSaw:
					t.Errorf("the default TypeSafe host did not see the key (%+v):\n%s", r, out)
				}
				if !override && r.audit != c.wantAudit {
					t.Errorf("audit rows = %d, want %d", r.audit, c.wantAudit)
				}
			})
		}
	}
	// the marked agent and the coder turn that cleared its markers give identical results
	for _, mode := range []string{callers.ModeName(true), callers.ModeName(false)} {
		a, b := results[mode+"/marked agent"], results[mode+"/coder turn that cleared its markers"]
		if a != b {
			t.Errorf("%s: marked agent %+v differs from the coder turn %+v", mode, a, b)
		}
	}
}
