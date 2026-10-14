// Package workflowmanage is the part of `monoagentcli workflow` that lists, shows, creates,
// activates, deactivates and deletes workflows, lists their executions and trigger inputs, and
// lists, shows, restores and prunes their versions. Everything the commands need from the CLI
// around them comes in through Deps, so they can be tested in isolation.
package workflowmanage

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
	// LoadWorkflow is workflow id from the store, or the CLI's not-found error.
	LoadWorkflow func(ctx context.Context, id string) (*workflow.Workflow, error)
	// CreateWorkflow persists a new workflow (metadata, nodes and connections) all or nothing.
	CreateWorkflow func(ctx context.Context, store *workflow.HybridWorkflowStore, wf *workflow.Workflow, overwrite bool) error
	// BuildEngine is the CLI's workflow engine and the cleanup to defer with engine.Stop.
	BuildEngine func(allowAllProfiles bool) (*workflow.WorkflowEngine, func(), error)
	// CheckOrgReferences refuses (or, with force, revokes) the org grants and automation roles
	// that use a workflow about to be deleted.
	CheckOrgReferences func(ctx context.Context, workflowID string, force bool) error
	// NotFound builds the CLI's not-found error (exit code 2).
	NotFound func(format string, a ...interface{}) error
	// InvalidInput builds the CLI's invalid-input error (exit code 3).
	InvalidInput func(format string, a ...interface{}) error
}

// The subcommands of `workflow`, by the name main registers them under.

func (d Deps) NewListCmd() *cobra.Command       { return d.newListCmd() }
func (d Deps) NewGetCmd() *cobra.Command        { return d.newGetCmd() }
func (d Deps) NewCreateCmd() *cobra.Command     { return d.newCreateCmd() }
func (d Deps) NewActivateCmd() *cobra.Command   { return d.newActivateCmd() }
func (d Deps) NewDeactivateCmd() *cobra.Command { return d.newDeactivateCmd() }
func (d Deps) NewDeleteCmd() *cobra.Command     { return d.newDeleteCmd() }
func (d Deps) NewExecutionsCmd() *cobra.Command { return d.newExecutionsCmd() }
func (d Deps) NewInputsCmd() *cobra.Command     { return d.newInputsCmd() }
func (d Deps) NewVersionsCmd() *cobra.Command   { return d.newVersionsCmd() }
