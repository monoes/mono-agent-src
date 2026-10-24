package main

import (
	"github.com/spf13/cobra"

	"github.com/monoes/mono-agent/internal/cli/workflowaitask"
	"github.com/monoes/mono-agent/internal/storage"
)

// `workflow apply-fix` and `workflow save-draft` live in internal/cli/workflowaitask; this is their
// adapter. Both are the operator's: the agent of the AI task only proposed.
func newWorkflowAitaskCmds(cfg *globalConfig) []*cobra.Command {
	return workflowaitask.New(workflowaitask.Deps{
		JSONOutput:      func() bool { return cfg.JSONOutput },
		ProfileID:       func() string { return cfg.ProfileID },
		OpenDB:          func() (*storage.Database, error) { return initDB(cfg) },
		RequireOperator: requireOperator,
		WorkflowStore:   openWorkflowStore,
		Wire:            func() { wireAitaskKinds(cfg) },
	})
}
