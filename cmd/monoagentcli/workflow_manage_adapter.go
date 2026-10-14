package main

import (
	"context"

	"github.com/spf13/cobra"

	"github.com/monoes/mono-agent/internal/cli/workflowmanage"
	"github.com/monoes/mono-agent/internal/storage"
	"github.com/monoes/mono-agent/internal/workflow"
)

// `workflow list get create activate deactivate delete executions inputs versions` live in
// internal/cli/workflowmanage; this is their adapter: the CLI's globals and helpers handed over
// as workflowmanage.Deps, under the old constructor names.

func workflowManageDeps(cfg *globalConfig) workflowmanage.Deps {
	return workflowmanage.Deps{
		JSONOutput:    func() bool { return cfg.JSONOutput },
		ProfileID:     func() string { return cfg.ProfileID },
		OpenDB:        func() (*storage.Database, error) { return initDB(cfg) },
		WorkflowStore: openWorkflowStore,
		OwnedWorkflow: ownedWorkflow,
		LoadWorkflow: func(ctx context.Context, id string) (*workflow.Workflow, error) {
			return loadWorkflowDefinition(ctx, cfg, id)
		},
		CreateWorkflow: createOrOverwriteWorkflowAtomically,
		BuildEngine: func(allowAllProfiles bool) (*workflow.WorkflowEngine, func(), error) {
			return buildEngine(cfg, allowAllProfiles)
		},
		CheckOrgReferences: func(ctx context.Context, workflowID string, force bool) error {
			return checkWorkflowOrgReferences(ctx, cfg, workflowID, force)
		},
		NotFound:     errNotFound,
		InvalidInput: errInvalidInput,
	}
}

// versionRow is one line of `workflow versions <id>`.
type versionRow = workflowmanage.VersionRow

func newWorkflowListCmd(cfg *globalConfig) *cobra.Command {
	return workflowManageDeps(cfg).NewListCmd()
}

func newWorkflowGetCmd(cfg *globalConfig) *cobra.Command {
	return workflowManageDeps(cfg).NewGetCmd()
}

func newWorkflowCreateCmd(cfg *globalConfig) *cobra.Command {
	return workflowManageDeps(cfg).NewCreateCmd()
}

func newWorkflowActivateCmd(cfg *globalConfig) *cobra.Command {
	return workflowManageDeps(cfg).NewActivateCmd()
}

func newWorkflowDeactivateCmd(cfg *globalConfig) *cobra.Command {
	return workflowManageDeps(cfg).NewDeactivateCmd()
}

func newWorkflowDeleteCmd(cfg *globalConfig) *cobra.Command {
	return workflowManageDeps(cfg).NewDeleteCmd()
}

func newWorkflowExecutionsCmd(cfg *globalConfig) *cobra.Command {
	return workflowManageDeps(cfg).NewExecutionsCmd()
}

func newWorkflowInputsCmd(cfg *globalConfig) *cobra.Command {
	return workflowManageDeps(cfg).NewInputsCmd()
}

func newWorkflowVersionsCmd(cfg *globalConfig) *cobra.Command {
	return workflowManageDeps(cfg).NewVersionsCmd()
}
