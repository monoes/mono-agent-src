// Package secret is `monoagentcli secret`: the encrypted vault of secrets and logins (the store
// is internal/secrets'). Everything the commands need from the CLI around them comes in through
// Deps, so they can be tested in isolation.
package secret

import (
	"io"

	"github.com/olekukonko/tablewriter"
	"github.com/spf13/cobra"

	"github.com/monoes/mono-agent/internal/storage"
)

// Deps is what the commands take from the CLI around them.
type Deps struct {
	// JSONOutput reports the global --json flag; it is read when a command runs.
	JSONOutput func() bool
	// ProfileID is the profile the command runs in once OpenDB has resolved it ("" means
	// "default"); it is read when a command runs.
	ProfileID func() string
	// OpenDB opens (and migrates) the database the command is pointed at.
	OpenDB func() (*storage.Database, error)
	// NotFound builds the CLI's not-found error (exit code 2).
	NotFound func(format string, a ...interface{}) error
	// WriteJSON writes the --json document.
	WriteJSON func(w io.Writer, v any) error
	// NewTable is the CLI's borderless table with these headers.
	NewTable func(w io.Writer, headers []string) *tablewriter.Table
}

// NewCommand returns the `secret` command group.
func NewCommand(d Deps) *cobra.Command { return d.newSecretCmd() }

// ReadValue reads a secret value from r up to the first newline (see readSecretValue); the
// CLI's other commands that read a secret from stdin share it.
func ReadValue(r io.Reader) (string, error) { return readSecretValue(r) }

// RematerializeConnection and RematerializeSession are the hooks `secret import` hands
// secrets.Import.
var (
	RematerializeConnection = rematerializeConnection
	RematerializeSession    = rematerializeSession
)

// truncateStr shortens s to at most n characters, appending "..." if truncated (the CLI's
// truncateStr).
func truncateStr(s string, n int) string {
	if len(s) <= n {
		return s
	}
	if n <= 3 {
		return s[:n]
	}
	return s[:n-3] + "..."
}
