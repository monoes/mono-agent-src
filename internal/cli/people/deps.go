// Package people is `monoagentcli people` (alias `person`): the people found by searches and
// interactions, their messages, posts, status, review queue, tags and links. Everything the
// commands need from the CLI around them comes in through Deps, so they can be tested in
// isolation.
package people

import (
	"context"
	"database/sql"
	"encoding/json"
	"io"

	"github.com/olekukonko/tablewriter"
	"github.com/olekukonko/tablewriter/tw"
	"github.com/spf13/cobra"

	"github.com/monoes/mono-agent/internal/connections"
	"github.com/monoes/mono-agent/internal/storage"
	"github.com/monoes/mono-agent/internal/workflow"
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
	// AuthConnection builds the CLI's auth-or-connection error (exit code 4).
	AuthConnection func(format string, a ...interface{}) error
	// NewTable is the CLI's borderless table with these headers and per-column alignment.
	NewTable func(w io.Writer, headers []string, perColumnAlign []tw.Align) *tablewriter.Table
	// WorkflowStore is the profile's workflows, files and SQLite alike.
	WorkflowStore func(db *storage.Database) *workflow.HybridWorkflowStore
	// RunWorkflow runs `workflow run` with these arguments (`people review approve --send`).
	RunWorkflow func(ctx context.Context, args []string) error
	// NodeRegistry is the CLI's node registry, for running service.outlook_mail in-process.
	NodeRegistry func(db *sql.DB) *workflow.NodeTypeRegistry
	// ResolveCredential is a connection's credential data, by id or platform name, in a profile.
	ResolveCredential func(ctx context.Context, store *connections.Store, credentialOrPlatform, profileID string) (map[string]interface{}, error)
}

// NewCommand returns the `people` command group.
func NewCommand(d Deps) *cobra.Command { return d.newPeopleCmd() }

// PrintJSON writes v to stdout as indented JSON (the CLI's printReviewJSON).
func PrintJSON(v interface{}) error { return printReviewJSON(v) }

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

// writeJSONTo writes v to w as indented JSON (the CLI's writeJSONTo).
func writeJSONTo(w io.Writer, v any) error {
	enc := json.NewEncoder(w)
	enc.SetIndent("", "  ")
	return enc.Encode(v)
}
