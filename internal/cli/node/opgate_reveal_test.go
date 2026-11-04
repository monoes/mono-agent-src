package node

import (
	"context"
	"strings"
	"testing"

	"github.com/monoes/mono-agent/internal/cli/clicore"
	"github.com/monoes/mono-agent/internal/cli/opgate"
	"github.com/monoes/mono-agent/internal/operator"
	"github.com/monoes/mono-agent/internal/secrets"
	"github.com/monoes/mono-agent/internal/storage"
)

// `node run vault.secret_get` has an agent path next to the operator's: under the profile's Reveal level it
// runs as an unapproved step that audits the reveal. What opgate refuses gets that path, so a process inside a
// coder turn that cleared its markers can no longer read through the operator's unaudited run. The real gate's
// contexts are in cmd/monoagentcli; here the registered body is a stand-in.

const (
	nodeRevealValue = "node-reveal-value"
	nodeRevealTail  = "; run it yourself in your terminal or the app"
	nodeRevealHead  = "node run vault.secret_get prints decrypted secrets, which only the user reads: "
)

func nodeRevealFixture(t *testing.T, level string) string {
	t.Helper()
	dbPath := nodeVaultTestDB(t)
	seedVaultSecret(t, dbPath, "vs", nodeRevealValue)
	db, err := storage.NewDatabase(dbPath)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	if level != "strict" {
		if err := secrets.SetAgentAccessGrant(context.Background(), db.DB, "default", secrets.AccessGrant{Level: level, AnyHost: true}); err != nil {
			t.Fatal(err)
		}
	}
	return dbPath
}

func nodeRevealAuditRows(t *testing.T, dbPath string) int {
	t.Helper()
	db, err := storage.NewDatabase(dbPath)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	rows, err := secrets.AgentAccessAudit(context.Background(), db.DB, "default")
	if err != nil {
		t.Fatal(err)
	}
	return len(rows)
}

type nodeRevealContext struct {
	name  string
	setup func(t *testing.T)
	why   func() string
}

func nodeRevealContexts() []nodeRevealContext {
	standIn := func(why string) nodeRevealContext {
		return nodeRevealContext{why, func(t *testing.T) {
			asTheOperator(t)
			opgate.SetForTest(t, func(bool) string { return why })
		}, func() string { return why }}
	}
	return []nodeRevealContext{
		{"marked agent", func(t *testing.T) { t.Setenv("CLAUDECODE", "1") }, operator.Refusal},
		standIn("stand-in: inside a coder turn"),
		standIn("stand-in: an agent token"),
		standIn("stand-in: cannot tell whether a coder session's turn runs this command (boom)"),
	}
}

const nodeRevealPayload = `{"config":{"name":"vs"},"input":[{"json":{}}]}`

// Below the Reveal level the run is refused (exit 3, the text), prints no plaintext and audits nothing.
func TestNodeRunVaultSecretGetRefusesTheAgentPathBelowTheRevealLevel(t *testing.T) {
	for _, level := range []string{"strict", "use"} {
		for _, c := range nodeRevealContexts() {
			t.Run(level+"/"+c.name, func(t *testing.T) {
				dbPath := nodeRevealFixture(t, level)
				c.setup(t)
				out, err := runNodeSubcmdStdin(t, testDeps(dbPath, "default", false), nodeRevealPayload, "vault.secret_get")
				if err == nil {
					t.Fatalf("not refused: %s", out)
				}
				if code := clicore.ExitCodeFor(err); code != 3 {
					t.Fatalf("exit %d: %v", code, err)
				}
				if want := nodeRevealHead + c.why() + nodeRevealTail; err.Error() != want {
					t.Fatalf("refusal text\n got: %s\nwant: %s", err.Error(), want)
				}
				if strings.Contains(out, nodeRevealValue) || strings.Contains(err.Error(), nodeRevealValue) {
					t.Fatalf("plaintext leaked: %q %v", out, err)
				}
				if n := nodeRevealAuditRows(t, dbPath); n != 0 {
					t.Fatalf("%d audit row(s) for a refused run", n)
				}
			})
		}
	}
}

// Under the Reveal level every agent context gets the marked agent's result through the engine's node as well:
// the same output (or the same refusal) and the same audit rows.
func TestNodeRunVaultSecretGetUnderTheRevealLevelIsTheMarkedAgentsForEveryAgentContext(t *testing.T) {
	var golden, goldenErr string
	var goldenRows int
	for i, c := range nodeRevealContexts() {
		t.Run(c.name, func(t *testing.T) {
			dbPath := nodeRevealFixture(t, "reveal")
			c.setup(t)
			out, err := runNodeSubcmdStdin(t, testDeps(dbPath, "default", false), nodeRevealPayload, "vault.secret_get")
			errText := ""
			if err != nil {
				errText = err.Error()
			}
			rows := nodeRevealAuditRows(t, dbPath)
			if i == 0 { // the marked agent, as it was before this change
				golden, goldenErr, goldenRows = out, errText, rows
				t.Logf("marked agent under Reveal: err=%q audit rows=%d out=%q", errText, rows, out)
				if err != nil || !strings.Contains(out, nodeRevealValue) || rows != 1 {
					t.Fatalf("the marked agent's reveal: err=%v rows=%d out=%q", err, rows, out)
				}
			}
			if out != golden || errText != goldenErr || rows != goldenRows {
				t.Fatalf("differs from the marked agent's\n out: %q\nwant: %q\n err: %q\nwant: %q\nrows %d want %d", out, golden, errText, goldenErr, rows, goldenRows)
			}
		})
	}
}

// The operator's own terminal reads at any level, with no audit row, as before.
func TestNodeRunVaultSecretGetOperatorIsUnchanged(t *testing.T) {
	dbPath := nodeRevealFixture(t, "strict")
	asTheOperator(t)
	out, err := runNodeSubcmdStdin(t, testDeps(dbPath, "default", false), nodeRevealPayload, "vault.secret_get")
	if err != nil || !strings.Contains(out, nodeRevealValue) {
		t.Fatalf("the operator: %v %q", err, out)
	}
	if n := nodeRevealAuditRows(t, dbPath); n != 0 {
		t.Fatalf("%d audit row(s) for the operator", n)
	}
}
