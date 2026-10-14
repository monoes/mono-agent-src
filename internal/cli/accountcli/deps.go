// Package accountcli is `monoagentcli account`: the monoes.me sign-in of this machine (login,
// logout, status). The sign-in itself (account_login.go) stays in the CLI, which the library's
// alias shares; it comes in through Deps, as everything else the commands need, so they can be
// tested in isolation.
package accountcli

import (
	"io"

	"github.com/spf13/cobra"

	"github.com/monoes/mono-agent/internal/account"
	"github.com/monoes/mono-agent/internal/cli/clicore"
)

// Deps is what the commands take from the CLI around them.
type Deps struct {
	// JSONOutput reports the global --json flag; it is read when a command runs.
	JSONOutput func() bool
	// WithJSONErrors makes a failing command also print {"error": …} with --json.
	WithJSONErrors func(cmd *cobra.Command)
	// AddSignIn adds the sign-in flags to cmd and returns the sign-in they ask for: the
	// session's status, and sent when it only emailed a code. name is the command, for the
	// hints.
	AddSignIn func(cmd *cobra.Command, name string) func(cmd *cobra.Command) (st account.Status, sent bool, err error)
}

// print writes v as indented JSON with --json, else calls human (the CLI's printLib).
func (d Deps) print(cmd *cobra.Command, v any, human func(w io.Writer)) error {
	if d.JSONOutput() {
		return clicore.WriteJSONTo(cmd.OutOrStdout(), v)
	}
	human(cmd.OutOrStdout())
	return nil
}

func nonEmptyStr(a, b string) string {
	if a != "" {
		return a
	}
	return b
}
