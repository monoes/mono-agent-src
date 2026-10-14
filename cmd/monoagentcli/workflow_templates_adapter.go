package main

import (
	"github.com/spf13/cobra"

	"github.com/monoes/mono-agent/internal/cli/workflowtemplates"
	"github.com/monoes/mono-agent/internal/storage"
	"github.com/monoes/mono-agent/internal/workflow"
)

// `workflow templates list|show|use` and `workflow search` live in
// internal/cli/workflowtemplates; this is their adapter: the CLI's globals and helpers handed
// over as workflowtemplates.Deps, under the old names. `templates run` stays here with
// `workflow run`.

func workflowTemplatesDeps(cfg *globalConfig) workflowtemplates.Deps {
	return workflowtemplates.Deps{
		JSONOutput:     func() bool { return cfg.JSONOutput },
		ProfileID:      func() string { return cfg.ProfileID },
		OpenDB:         func() (*storage.Database, error) { return initDB(cfg) },
		WorkflowStore:  openWorkflowStore,
		CreateWorkflow: createOrOverwriteWorkflowAtomically,
		CheckNodeTypes: func(id string, tmpl workflow.WorkflowFile) error {
			return checkTemplateNodeTypes(id, tmpl, buildNodeRegistry(cfg.Verbose, nil).Has)
		},
		RunCmd: func() *cobra.Command { return newWorkflowTemplatesRunCmd(cfg) },
	}
}

func newWorkflowTemplatesCmd(cfg *globalConfig) *cobra.Command {
	return workflowTemplatesDeps(cfg).NewTemplatesCmd()
}

func newWorkflowSearchCmd(cfg *globalConfig) *cobra.Command {
	return workflowTemplatesDeps(cfg).NewSearchCmd()
}

var (
	instantiateTemplate = workflowtemplates.InstantiateTemplate
	inputKeyList        = workflowtemplates.InputKeyList
	exampleInputJSON    = workflowtemplates.ExampleInputJSON
)
