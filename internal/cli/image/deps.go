// Package image is `monoagentcli image`: the active profile's image vault (internal/vault's
// vault_images). Everything the commands need from the CLI around them comes in through Deps,
// so they can be tested in isolation.
package image

import (
	"database/sql"
	"io"

	"github.com/olekukonko/tablewriter"
	"github.com/spf13/cobra"

	"github.com/monoes/mono-agent/internal/storage"
	"github.com/monoes/mono-agent/internal/vault"
)

// Deps is what the commands take from the CLI around them.
type Deps struct {
	// JSONOutput reports the global --json flag; it is read when a command runs.
	JSONOutput func() bool
	// ProfileID is the profile the command runs in once OpenDB has resolved it; it is read when
	// a command runs.
	ProfileID func() string
	// OpenDB opens (and migrates) the database the command is pointed at.
	OpenDB func() (*storage.Database, error)
	// NotFound builds the CLI's not-found error (exit code 2).
	NotFound func(format string, a ...interface{}) error
	// InvalidInput builds the CLI's invalid-input error (exit code 3).
	InvalidInput func(format string, a ...interface{}) error
	// WriteJSON writes the --json document.
	WriteJSON func(w io.Writer, v any) error
	// NewTable is the CLI's borderless table with these headers.
	NewTable func(w io.Writer, headers []string) *tablewriter.Table
	// PrintFolderSync prints what one reconcile of the profile folder changed, as
	// `profile documents sync` does (noun names what was scanned).
	PrintFolderSync func(cmd *cobra.Command, noun, profileID, root string, scanned int, r vault.ReconcileResult) error
}

// NewCommand returns the `image` command group.
func NewCommand(d Deps) *cobra.Command { return d.newImageCmd() }

// WithDB opens the database and runs fn on it (the CLI's withImageDB).
func (d Deps) WithDB(fn func(db *sql.DB) error) error { return d.withImageDB(fn) }

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
