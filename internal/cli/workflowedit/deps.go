// Package workflowedit is the part of `monoagentcli workflow` that edits a workflow's
// definition: `node add|list|set|remove`, `connect`, `disconnect` and `save` (the desktop
// editor's save). Everything the commands need from the CLI around them comes in through Deps,
// so they can be tested in isolation.
package workflowedit

import (
	"context"
	"database/sql"

	"github.com/spf13/cobra"

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
	// WorkflowStore is the profile's workflows, files and SQLite alike.
	WorkflowStore func(db *storage.Database) *workflow.HybridWorkflowStore
	// OwnedWorkflow is workflow id when it exists and profileID owns it, else nil.
	OwnedWorkflow func(ctx context.Context, store *workflow.HybridWorkflowStore, db *sql.DB, profileID, id string) *workflow.Workflow
	// NotFound builds the CLI's not-found error (exit code 2).
	NotFound func(format string, a ...interface{}) error
	// InvalidInput builds the CLI's invalid-input error (exit code 3).
	InvalidInput func(format string, a ...interface{}) error
}

// NewNodeCmd is `workflow node` (add, list, set, remove).
func (d Deps) NewNodeCmd() *cobra.Command { return d.newNodeCmd() }

// NewConnectCmd is `workflow connect`.
func (d Deps) NewConnectCmd() *cobra.Command { return d.newConnectCmd() }

// NewDisconnectCmd is `workflow disconnect`.
func (d Deps) NewDisconnectCmd() *cobra.Command { return d.newDisconnectCmd() }

// NewSaveCmd is `workflow save`.
func (d Deps) NewSaveCmd() *cobra.Command { return d.newSaveCmd() }
