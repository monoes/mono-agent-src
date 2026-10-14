package main

import (
	"context"

	"github.com/spf13/cobra"

	"github.com/monoes/mono-agent/internal/cli/workflowedit"
	"github.com/monoes/mono-agent/internal/storage"
	"github.com/monoes/mono-agent/internal/workflow"
)

// `workflow node`, `connect`, `disconnect` and `save` live in internal/cli/workflowedit; this is
// their adapter: the CLI's globals and helpers handed over as workflowedit.Deps, under the old
// names.

func workflowEditDeps(cfg *globalConfig) workflowedit.Deps {
	return workflowedit.Deps{
		JSONOutput:    func() bool { return cfg.JSONOutput },
		ProfileID:     func() string { return cfg.ProfileID },
		OpenDB:        func() (*storage.Database, error) { return initDB(cfg) },
		WorkflowStore: openWorkflowStore,
		OwnedWorkflow: ownedWorkflow,
		NotFound:      errNotFound,
		InvalidInput:  errInvalidInput,
	}
}

func newWorkflowNodeCmd(cfg *globalConfig) *cobra.Command {
	return workflowEditDeps(cfg).NewNodeCmd()
}

func newWorkflowConnectCmd(cfg *globalConfig) *cobra.Command {
	return workflowEditDeps(cfg).NewConnectCmd()
}

func newWorkflowDisconnectCmd(cfg *globalConfig) *cobra.Command {
	return workflowEditDeps(cfg).NewDisconnectCmd()
}

func newWorkflowSaveCmd(cfg *globalConfig) *cobra.Command {
	return workflowEditDeps(cfg).NewSaveCmd()
}

// saveWorkflowDocument persists doc for profileID (workflowedit.Deps.SaveDocument), for
// `workflow generate`.
func saveWorkflowDocument(ctx context.Context, store *workflow.HybridWorkflowStore, profileID string, doc workflow.Workflow) (*workflow.Workflow, error) {
	// SaveDocument reads only OwnedWorkflow and NotFound.
	return workflowedit.Deps{OwnedWorkflow: ownedWorkflow, NotFound: errNotFound}.SaveDocument(ctx, store, profileID, doc)
}
