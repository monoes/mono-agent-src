package main

import (
	"context"
	"fmt"

	"github.com/spf13/cobra"

	"github.com/monoes/mono-agent/internal/cli/workflowvalidate"
	"github.com/monoes/mono-agent/internal/workflow"
)

// `workflow validate` and the `workflow run --dry-run` plan live in internal/cli/workflowvalidate;
// this is their adapter: the CLI's globals and helpers handed over as workflowvalidate.Deps,
// under the old names.

func workflowValidateDeps(cfg *globalConfig) workflowvalidate.Deps {
	return workflowvalidate.Deps{
		JSONOutput: func() bool { return cfg.JSONOutput },
		LoadWorkflow: func(ctx context.Context, id string) (*workflow.Workflow, error) {
			return loadWorkflowDefinition(ctx, cfg, id)
		},
		ParseWorkflow: parseWorkflowDefinition,
		NodeTypes:     func() *workflow.NodeTypeRegistry { return buildNodeRegistry(false, nil) },
	}
}

// validationJSON is the `workflow validate --json` payload.
type validationJSON = workflowvalidate.ValidationJSON

// dryRunSteps validates a workflow for activation and computes its execution order.
var dryRunSteps = workflowvalidate.DryRunSteps

func newWorkflowValidateCmd(cfg *globalConfig) *cobra.Command {
	return workflowvalidate.NewCommand(workflowValidateDeps(cfg))
}

// runWorkflowDryRun backs `workflow run --dry-run`.
func runWorkflowDryRun(cfg *globalConfig, cmd *cobra.Command, workflowID string) error {
	return workflowValidateDeps(cfg).DryRun(cmd, workflowID)
}

// loadWorkflowDefinition fetches a workflow by id from the canonical hybrid
// store. Returns an errNotFound sentinel error when it does not exist.
func loadWorkflowDefinition(ctx context.Context, cfg *globalConfig, workflowID string) (*workflow.Workflow, error) {
	db, err := initDB(cfg)
	if err != nil {
		return nil, fmt.Errorf("open database: %w", err)
	}
	defer db.Close()

	store := openWorkflowStore(db)
	wf, err := store.GetWorkflow(ctx, workflowID)
	if err != nil {
		return nil, fmt.Errorf("get workflow: %w", err)
	}
	if wf == nil {
		return nil, errNotFound("workflow %q not found", workflowID)
	}
	return wf, nil
}
