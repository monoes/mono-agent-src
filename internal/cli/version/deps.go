// Package version is `monoagentcli version`. The version and build date come in through Deps
// (they are stamped into the CLI binary at link time).
package version

import "github.com/spf13/cobra"

// Deps is what the command takes from the CLI around it.
type Deps struct {
	// Version is the CLI's version ("dev" for an unstamped build).
	Version func() string
	// BuildDate is when the CLI was built.
	BuildDate func() string
}

// NewCommand returns the `version` command.
func NewCommand(d Deps) *cobra.Command { return d.newVersionCmd() }
