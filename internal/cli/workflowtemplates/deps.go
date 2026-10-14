// Package workflowtemplates is `monoagentcli workflow templates list|show|use` and `workflow
// search`: the bundled workflow templates, and one search over them and the profile's saved
// workflows. Everything the commands need from the CLI around them comes in through Deps, so
// they can be tested in isolation.
package workflowtemplates

import (
	"context"

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
	// CreateWorkflow persists a new workflow (metadata, nodes and connections) all or nothing.
	CreateWorkflow func(ctx context.Context, store *workflow.HybridWorkflowStore, wf *workflow.Workflow, overwrite bool) error
	// CheckNodeTypes fails, with the install hint, when template id uses node types this
	// install does not have.
	CheckNodeTypes func(id string, tmpl workflow.WorkflowFile) error
	// RunCmd is `workflow templates run`, which stays with `workflow run`.
	RunCmd func() *cobra.Command
}

// NewTemplatesCmd is `workflow templates` (list, show, use, and run from RunCmd).
func (d Deps) NewTemplatesCmd() *cobra.Command { return d.newTemplatesCmd() }

// NewSearchCmd is `workflow search`.
func (d Deps) NewSearchCmd() *cobra.Command { return d.newSearchCmd() }
