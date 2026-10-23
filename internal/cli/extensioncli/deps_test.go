package extensioncli

import (
	"sort"
	"strings"
	"testing"

	"github.com/spf13/cobra"
)

// The command main registers: its alias, its subcommands, and `serve` marked as serving
// through Deps.ServingCommand.
func TestCommandTreeAndServingServe(t *testing.T) {
	t.Parallel()
	d := *testDeps(false)
	var marked []string
	d.ServingCommand = func(c *cobra.Command) *cobra.Command { marked = append(marked, c.Name()); return c }
	cmd := NewCommand(d)
	var subs []string
	for _, c := range cmd.Commands() {
		subs = append(subs, c.Name())
	}
	sort.Strings(subs)
	if got := strings.Join(subs, " "); got != "approve autopair bind browsers deny info install pair reset serve status unbind" {
		t.Errorf("subcommands = %q", got)
	}
	if len(cmd.Aliases) != 1 || cmd.Aliases[0] != "bridge" {
		t.Errorf("aliases = %v", cmd.Aliases)
	}
	if len(marked) != 1 || marked[0] != "serve" {
		t.Errorf("serving commands = %v, want [serve]", marked)
	}
}
