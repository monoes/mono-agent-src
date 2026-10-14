// Package hil is `monoagentcli hil`: the Human-in-Loop queue, and the operator's side of the
// queue of sends an agent prepared (internal/hilsend). Everything the commands need from the CLI
// around them comes in through Deps, so they can be tested in isolation. Who may decide a send
// (RequireOperator) and how an approved send runs (SendRunner) stay in the CLI.
package hil

import (
	"database/sql"
	"io"

	"github.com/olekukonko/tablewriter"
	"github.com/spf13/cobra"

	"github.com/monoes/mono-agent/internal/hilsend"
	"github.com/monoes/mono-agent/internal/storage"
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
	// RequireOperator refuses an agent: it may prepare a send, never decide one (the CLI's
	// requireOperator). what says what was refused.
	RequireOperator func(what string) error
	// SendRunner is what performs an approved send.
	SendRunner func() hilsend.Runner
	// WorkspaceRoot is the coder workspace root, a folder a send's attachment may come from.
	WorkspaceRoot func(db *sql.DB) (string, error)
}

// NewCommand returns the `hil` command group.
func NewCommand(d Deps) *cobra.Command { return d.newHILCmd() }

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

// Item is one `hil list --json` row.
type Item = hilItem
