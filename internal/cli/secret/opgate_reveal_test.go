package secret

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

// `secret reveal` has an agent path next to the operator's (a reveal under the profile's Reveal level, audited).
// What opgate refuses gets that path: a marked agent, the chat, and a process inside a coder turn that cleared its
// markers. The real gate's contexts are in cmd/monoagentcli; here the registered body is a stand-in.

const (
	revealValue   = "level-secret-value"
	revealRefusal = "secret reveal prints decrypted secrets, which only the user reads: "
	revealTail    = "; run it yourself in your terminal or the app"
	revealRepair  = "; retry, or repair the database"
)

func revealAuditRows(t *testing.T, dbPath string) int {
	t.Helper()
	db, err := storage.NewDatabase(dbPath)
	if err != nil {
		t.Fatal(err)
	}
	defer db.DB.Close()
	rows, err := secrets.AgentAccessAudit(context.Background(), db.DB, "p1")
	if err != nil {
		t.Fatal(err)
	}
	return len(rows)
}

// revealFixture is a vault with one entry and the profile's agent-access level set by the operator.
func revealFixture(t *testing.T, level string) string {
	t.Helper()
	db := newSecretCLITestDB(t)
	profileP1(t, db)
	if _, err := runSecretCmd(t, db, "add", "--name", "level-password", "--value", revealValue); err != nil {
		t.Fatal(err)
	}
	args := []string{"agent-access", level}
	if level != "strict" {
		args = append(args, "--yes")
	}
	if _, err := runSecretCmd(t, db, args...); err != nil {
		t.Fatal(err)
	}
	return db
}

type revealContext struct {
	name  string
	setup func(t *testing.T)
	why   func() string
}

func revealContexts() []revealContext {
	standIn := func(why string) func(*testing.T) {
		return func(t *testing.T) {
			asTheOperator(t) // the markers are cleared: only the gate says it is not the operator
			opgate.SetForTest(t, func(bool) string { return why })
		}
	}
	return []revealContext{
		{"marked agent", func(t *testing.T) { t.Setenv("CLAUDECODE", "1") }, operator.Refusal},
		{"in a turn, markers cleared", standIn("stand-in: inside a coder turn"), func() string { return "stand-in: inside a coder turn" }},
		{"token holder", standIn("stand-in: an agent token"), func() string { return "stand-in: an agent token" }},
		{"database unreadable", standIn(opgate.UnplacedPrefix + "boom)"), func() string { return opgate.UnplacedPrefix + "boom)" }},
	}
}

// Refused when the level does not allow the reveal: exit 3, the text, no plaintext, no audit row.
func TestSecretRevealRefusesTheAgentPathBelowTheRevealLevel(t *testing.T) {
	for _, level := range []string{"strict", "use"} {
		for _, c := range revealContexts() {
			t.Run(level+"/"+c.name, func(t *testing.T) {
				db := revealFixture(t, level)
				c.setup(t)
				out, err := runSecretCmd(t, db, "reveal", "level-password", "--reveal")
				if err == nil || clicore.ExitCodeFor(err) != 3 {
					t.Fatalf("not refused (exit 3): %v", err)
				}
				if want := revealRefusal + c.why() + revealTailFor(c.why()); err.Error() != want {
					t.Fatalf("refusal text\n got: %s\nwant: %s", err.Error(), want)
				}
				if strings.Contains(out, revealValue) || strings.Contains(err.Error(), revealValue) {
					t.Fatalf("plaintext leaked: %q %v", out, err)
				}
				if n := revealAuditRows(t, db); n != 0 {
					t.Fatalf("a refused reveal wrote %d audit row(s)", n)
				}
			})
		}
	}
}

// Under the Reveal level every agent context gets the marked agent's result: the same bytes, one name-only
// audit row.
func TestSecretRevealUnderTheRevealLevelIsTheMarkedAgentsForEveryAgentContext(t *testing.T) {
	var golden string
	for i, c := range revealContexts() {
		t.Run(c.name, func(t *testing.T) {
			db := revealFixture(t, "reveal")
			c.setup(t)
			out, err := runSecretCmd(t, db, "reveal", "level-password", "--reveal")
			if err != nil {
				t.Fatal(err)
			}
			if i == 0 { // the marked agent, as it was before this change
				golden = out
				const before = "{\n  \"fields\": {\n    \"secret\": \"level-secret-value\"\n  },\n  \"notes\": \"\"\n}\n" // byte for byte, as before
				if out != before {
					t.Fatalf("the marked agent's reveal changed\n got: %q\nwant: %q", out, before)
				}
			}
			if out != golden {
				t.Fatalf("output differs from the marked agent's\n got: %q\nwant: %q", out, golden)
			}
			if n := revealAuditRows(t, db); n != 1 {
				t.Fatalf("%d audit row(s), want 1", n)
			}
		})
	}
}

// The operator's own terminal reveals at any level, with no audit row, as before.
func TestSecretRevealOperatorIsUnchanged(t *testing.T) {
	db := revealFixture(t, "strict")
	asTheOperator(t)
	out, err := runSecretCmd(t, db, "reveal", "level-password", "--reveal")
	if err != nil || !strings.Contains(out, revealValue) {
		t.Fatalf("the operator: %v %q", err, out)
	}
	if n := revealAuditRows(t, db); n != 0 {
		t.Fatalf("the operator's reveal wrote %d audit row(s)", n)
	}
}

// revealTailFor is how a refusal ends: a process the gate could not place is told to retry or repair.
func revealTailFor(why string) string {
	if opgate.Unplaced(why) {
		return revealRepair
	}
	return revealTail
}
