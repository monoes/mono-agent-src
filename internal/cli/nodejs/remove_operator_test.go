package nodejs

import (
	"bytes"
	"testing"

	"github.com/monoes/mono-agent/internal/cli/clicore"

	"github.com/monoes/mono-agent/internal/testhome"
)

// An agent's `nodejs remove --yes` is refused before anything is touched.
func TestRemoveIsOperatorOnly(t *testing.T) {
	home := t.TempDir()
	testhome.SetHome(t, home)
	cmd := NewCommand(Deps{
		JSONOutput:      func() bool { return true },
		StdinIsTerminal: func() bool { return false },
		Refusal:         func() string { return "an agent is running this" },
	})
	cmd.SetOut(&bytes.Buffer{})
	cmd.SetErr(&bytes.Buffer{})
	cmd.SetArgs([]string{"remove", "--yes"})
	cmd.SilenceErrors, cmd.SilenceUsage = true, true
	if err := cmd.Execute(); clicore.ExitCodeFor(err) != 3 {
		t.Fatalf("remove by an agent: %v", err)
	}
}
