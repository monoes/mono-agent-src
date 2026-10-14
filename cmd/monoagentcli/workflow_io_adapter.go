package main

import (
	"github.com/spf13/cobra"

	"github.com/monoes/mono-agent/internal/cli/workflowio"
	"github.com/monoes/mono-agent/internal/storage"
	"github.com/monoes/mono-agent/internal/workflow"
)

// `workflow import`, `export`, `convert` and `migrate` live in internal/cli/workflowio; this is
// their adapter: the CLI's globals and helpers handed over as workflowio.Deps, under the old
// names.

func workflowIODeps(cfg *globalConfig) workflowio.Deps {
	return workflowio.Deps{
		JSONOutput:      func() bool { return cfg.JSONOutput },
		ProfileID:       func() string { return cfg.ProfileID },
		OpenDB:          func() (*storage.Database, error) { return initDB(cfg) },
		WorkflowStore:   openWorkflowStore,
		CreateWorkflow:  createOrOverwriteWorkflowAtomically,
		NotFound:        errNotFound,
		InvalidInput:    errInvalidInput,
		Reported:        func(err error) error { return reportedError{Err: err} },
		WithJSONErrors:  func(cmd *cobra.Command) { withJSONErrors(cfg, cmd) },
		StdinIsTerminal: stdinIsTerminal,
		Bundle:          workflowBundleDeps(),
	}
}

func newWorkflowImportCmd(cfg *globalConfig) *cobra.Command {
	return workflowIODeps(cfg).NewImportCmd()
}

func newWorkflowExportCmd(cfg *globalConfig) *cobra.Command {
	return workflowIODeps(cfg).NewExportCmd()
}

func newWorkflowConvertCmd(cfg *globalConfig) *cobra.Command {
	return workflowIODeps(cfg).NewConvertCmd()
}

func newWorkflowMigrateCmd(cfg *globalConfig) *cobra.Command {
	return workflowIODeps(cfg).NewMigrateCmd()
}

// parseWorkflowDefinition parses workflow definition JSON as `workflow import` does
// (workflowio.Deps.ParseWorkflowDefinition), for `workflow validate` and `library install`.
func parseWorkflowDefinition(raw []byte) (workflow.Workflow, error) {
	// ParseWorkflowDefinition reads only InvalidInput.
	return workflowio.Deps{InvalidInput: errInvalidInput}.ParseWorkflowDefinition(raw)
}

// Import statuses (`workflow import --json` "status").
const (
	importCreated   = workflowio.ImportCreated
	importUpdated   = workflowio.ImportUpdated
	importUnchanged = workflowio.ImportUnchanged
)

// syncMarkerName sits next to the file store directory (workflowio.SyncMarkerName).
const syncMarkerName = workflowio.SyncMarkerName

var (
	// syncFileWorkflowsToSQL mirrors file-store workflows into SQLite (workflowio.SyncFileWorkflowsToSQL).
	syncFileWorkflowsToSQL      = workflowio.SyncFileWorkflowsToSQL
	normalizeLegacyWorkflowJSON = workflowio.NormalizeLegacyWorkflowJSON
	workflowFileFromWorkflow    = workflowio.WorkflowFileFromWorkflow
	ownedWorkflow               = workflowio.OwnedWorkflow
)
