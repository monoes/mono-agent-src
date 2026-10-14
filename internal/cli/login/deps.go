// Package login is `monoagentcli login` and `monoagentcli logout`: browser sessions of
// platforms and browser automations, captured from a tab of the user's own browser through the
// extension bridge. Everything the commands need from the CLI around them comes in through
// Deps, so they can be tested in isolation.
package login

import (
	"context"
	"database/sql"
	"encoding/json"
	"io"
	"os"

	"github.com/olekukonko/tablewriter"
	"github.com/olekukonko/tablewriter/tw"
	"github.com/spf13/cobra"

	"github.com/monoes/mono-agent/internal/automation"
	browserpkg "github.com/monoes/mono-agent/internal/browser"
	"github.com/monoes/mono-agent/internal/storage"
)

// Deps is what the commands take from the CLI around them.
type Deps struct {
	// JSONOutput reports the global --json flag; it is read when a command runs.
	JSONOutput func() bool
	// ProfileID is the profile --profile names, resolved to its id once OpenDB has run ("" when
	// none is set); it is read when a command runs.
	ProfileID func() string
	// BrowserProfile resolves the profile this invocation runs as to its id, opening the
	// database to do it (the CLI's browserProfile).
	BrowserProfile func() string
	// OpenDB opens (and migrates) the database the command is pointed at, and resolves the
	// profile ProfileID reports.
	OpenDB func() (*storage.Database, error)
	// OpenRegistry returns the booted automation registry, or nil when there is none.
	OpenRegistry func() (*automation.Registry, error)
	// ConnectBridge returns the extension bridge narrowed to profileID's browser once it is
	// connected, or why it is not.
	ConnectBridge func(profileID string) (browserpkg.ExtensionBridge, error)
	// NotFound, InvalidInput and AuthConnection build the CLI's errors (exit codes 2, 3 and 4).
	NotFound       func(format string, a ...interface{}) error
	InvalidInput   func(format string, a ...interface{}) error
	AuthConnection func(format string, a ...interface{}) error
	// WriteJSON writes the --json document.
	WriteJSON func(w io.Writer, v any) error
	// NewTable is the CLI's borderless table with these headers and, optionally, one alignment
	// per column.
	NewTable func(w io.Writer, headers []string, perColumnAlign []tw.Align) *tablewriter.Table
}

// NewCommand returns the `login` command group.
func NewCommand(d Deps) *cobra.Command { return d.newLoginCmd() }

// NewLogoutCommand returns `logout`.
func NewLogoutCommand(d Deps) *cobra.Command { return d.newLogoutCmd() }

// UpsertSessionRow stores platform/username's cookie jar as a "session" vault entry and points
// the crawler_sessions row at it, with the default 30-day lifetime.
func UpsertSessionRow(ctx context.Context, db *sql.DB, profileID, platform, username string, cookiesJSON []byte) error {
	return upsertSessionRow(ctx, db, profileID, platform, username, cookiesJSON)
}

// printJSON writes v to standard output as indented JSON (the CLI's printReviewJSON).
func printJSON(v interface{}) error {
	enc := json.NewEncoder(os.Stdout)
	enc.SetIndent("", "  ")
	return enc.Encode(v)
}
