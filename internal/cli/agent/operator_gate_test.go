package agent

import (
	"strings"
	"testing"

	"github.com/monoes/mono-agent/internal/cli/clicore"
)

// install --yes/--approve-script, a bare or --all validate that makes model
// calls, and auto-revalidate on are the user's own: refused for an agent.
func TestAgentSpendingAndInstallGatesRefuseNonOperator(t *testing.T) {
	dbPath := newTestDBPath(t)
	writeRosterMonomind(t)
	for name, args := range map[string][]string{
		"install --yes":            {"install", "alpha", "--yes"},
		"install --approve-script": {"install", "alpha", "--approve-script", "https://example.com/x.sh"},
		"validate bare":            {"validate"},
		"validate --all":           {"validate", "--all"},
		"auto-revalidate on":       {"roster", "auto-revalidate", "on"},
	} {
		for who, refusal := range map[string]func() string{
			"agent":     func() string { return "CLAUDECODE is set" },
			"not wired": nil,
		} {
			d := testDeps(dbPath, true)
			d.Refusal = refusal
			cmd := NewCommand(d)
			cmd.SetArgs(args)
			cmd.SilenceErrors, cmd.SilenceUsage = true, true
			var err error
			captureStdout(t, func() { err = cmd.Execute() })
			if clicore.ExitCodeFor(err) != 3 || err == nil || !strings.Contains(err.Error(), "only the user") {
				t.Errorf("%s as %s: exit %d, %v", name, who, clicore.ExitCodeFor(err), err)
			}
		}
	}
}

// A dry run and a validate scoped to one runtime stay open to an agent.
func TestAgentValidateScopedAndDryRunStayOpen(t *testing.T) {
	dbPath := newTestDBPath(t)
	writeRosterMonomind(t)
	d := testDeps(dbPath, true)
	d.Refusal = func() string { return "an agent" }
	cmd := NewCommand(d)
	cmd.SetArgs([]string{"validate", "--dry-run"})
	cmd.SilenceErrors, cmd.SilenceUsage = true, true
	var err error
	captureStdout(t, func() { err = cmd.Execute() })
	if err != nil {
		t.Fatalf("dry run refused: %v", err)
	}
}
