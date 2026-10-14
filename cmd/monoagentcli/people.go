package main

import (
	"context"
	"database/sql"

	"github.com/spf13/cobra"

	peoplecli "github.com/monoes/mono-agent/internal/cli/people"
	"github.com/monoes/mono-agent/internal/storage"
	"github.com/monoes/mono-agent/internal/workflow"
)

// `people` lives in internal/cli/people; this is its adapter: the CLI's globals and helpers
// handed over as peoplecli.Deps.

func peopleDeps(cfg *globalConfig) peoplecli.Deps {
	return peoplecli.Deps{
		JSONOutput:     func() bool { return cfg.JSONOutput },
		ProfileID:      func() string { return cfg.ProfileID },
		OpenDB:         func() (*storage.Database, error) { return initDB(cfg) },
		NotFound:       errNotFound,
		InvalidInput:   errInvalidInput,
		AuthConnection: errAuthConnection,
		NewTable:       newPlainTable,
		WorkflowStore:  openWorkflowStore,
		RunWorkflow: func(ctx context.Context, args []string) error {
			run := newWorkflowRunCmd(cfg)
			run.SetArgs(args)
			run.SetContext(ctx)
			return run.Execute()
		},
		NodeRegistry: func(db *sql.DB) *workflow.NodeTypeRegistry {
			return buildNodeRegistry(cfg.Verbose, db)
		},
		ResolveCredential: resolveCredentialData,
	}
}

func newPeopleCmd(cfg *globalConfig) *cobra.Command {
	return peoplecli.NewCommand(peopleDeps(cfg))
}

// printReviewJSON writes v to stdout as indented JSON (`login names` shares it).
func printReviewJSON(v interface{}) error { return peoplecli.PrintJSON(v) }
