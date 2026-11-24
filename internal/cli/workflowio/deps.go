// Package workflowio is the part of `monoagentcli workflow` that moves workflows in and out:
// `import` (idempotent, with `--from n8n` and bundled automations), `export` (optionally with
// its automations bundled), `convert` (n8n to mono-agent, nothing saved) and `migrate` (SQLite
// rows to workflow files). Everything the commands need from the CLI around them comes in
// through Deps, so they can be tested in isolation.
package workflowio

import (
	"context"
	"os"
	"path/filepath"
	"strings"

	"github.com/spf13/cobra"

	"github.com/monoes/mono-agent/internal/cli/workflowbundle"
	"github.com/monoes/mono-agent/internal/storage"
	"github.com/monoes/mono-agent/internal/workflow"
)

// Deps is what the commands take from the CLI around them.
type Deps struct {
	// Refusal says why this process is not the operator's own terminal ("" when it is). nil
	// means operator.Refusal. `workflow import --replace-automations` asks it.
	Refusal func() string
	// JSONOutput reports the global --json flag; it is read when a command runs.
	JSONOutput func() bool
	// ProfileID is the profile the command runs in once OpenDB has resolved it; it is read when
	// a command runs.
	ProfileID func() string
	// OpenDB opens (and migrates) the database the command is pointed at.
	OpenDB func() (*storage.Database, error)
	// WorkflowStore is the profile's workflows, files and SQLite alike.
	WorkflowStore func(db *storage.Database) *workflow.HybridWorkflowStore
	// CreateWorkflow persists a workflow (metadata, nodes and connections) all or nothing,
	// restoring the previous one on a failed overwrite.
	CreateWorkflow func(ctx context.Context, store *workflow.HybridWorkflowStore, wf *workflow.Workflow, overwrite bool) error
	// NotFound builds the CLI's not-found error (exit code 2).
	NotFound func(format string, a ...interface{}) error
	// InvalidInput builds the CLI's invalid-input error (exit code 3).
	InvalidInput func(format string, a ...interface{}) error
	// Reported marks err as already printed (the --json error document is not written again).
	Reported func(err error) error
	// WithJSONErrors makes a failure of cmd also a {"error","code"} document on stdout under
	// --json.
	WithJSONErrors func(cmd *cobra.Command)
	// StdinIsTerminal reports whether a person can answer a prompt on stdin.
	StdinIsTerminal func() bool
	// Bundle is the automation bundling of `export --bundle-automations` and `import`.
	Bundle workflowbundle.Deps
}

// NewImportCmd is `workflow import`.
func (d Deps) NewImportCmd() *cobra.Command { return d.newImportCmd() }

// NewExportCmd is `workflow export`.
func (d Deps) NewExportCmd() *cobra.Command { return d.newExportCmd() }

// NewConvertCmd is `workflow convert`.
func (d Deps) NewConvertCmd() *cobra.Command { return d.newConvertCmd() }

// NewMigrateCmd is `workflow migrate`.
func (d Deps) NewMigrateCmd() *cobra.Command { return d.newMigrateCmd() }

// expandPath expands a leading "~/" to the home directory (the CLI's expandPath).
func expandPath(path string) string {
	if strings.HasPrefix(path, "~/") {
		home, err := os.UserHomeDir()
		if err != nil {
			return path
		}
		return filepath.Join(home, path[2:])
	}
	return path
}
