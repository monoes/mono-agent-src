package main

import (
	"log/slog"

	"github.com/spf13/cobra"

	"github.com/monoes/mono-agent/internal/cli/workflowrun"
	"github.com/monoes/mono-agent/internal/logging"
	"github.com/monoes/mono-agent/internal/storage"
	"github.com/monoes/mono-agent/internal/workflow"
)

// `workflow run`, `workflow templates run`, `workflow execution` and `workflow cancel` live in
// internal/cli/workflowrun; this is their adapter: the CLI's globals and helpers handed over as
// workflowrun.Deps, under the old names.

func workflowRunDeps(cfg *globalConfig) workflowrun.Deps {
	return workflowrun.Deps{
		JSONOutput: func() bool { return cfg.JSONOutput },
		ProfileID:  func() string { return cfg.ProfileID },
		OpenDB:     func() (*storage.Database, error) { return initDB(cfg) },
		BuildEngine: func() (*workflow.WorkflowEngine, func(), error) {
			quietRunFailureLogs(cfg)
			return buildEngine(cfg, false)
		},
		NodeTypes: func() *workflow.NodeTypeRegistry { return buildNodeRegistry(cfg.Verbose, nil) },
		DryRun: func(cmd *cobra.Command, workflowID string) error {
			return runWorkflowDryRun(cfg, cmd, workflowID)
		},
	}
}

// oneShotRunLogs are engine lines a one-shot run's terminal does without: its summary reports
// a failed node or run (and `workflow status` each node's error), and webhooks are served by
// the daemon holding the port, not by an engine that exits after one run.
var oneShotRunLogs = []string{
	"node execution failed",
	"node emitted error output with no edge wired from the 'error' handle — failing node (wire an error edge or set on_error=continue/skip to tolerate)",
	"engine: execution finished with error",
	"engine: execution finished with non-fatal node failures",
	"engine: execution cancelled",
	"engine: webhook port already in use by another monoagent process — this engine will not serve webhooks yet, retrying every 30s, everything else runs normally",
}

// quietRunFailureLogs keeps oneShotRunLogs off the terminal of a human-mode run, so the
// summary is not preceded by raw log lines repeating it. They drop to INFO; a level chosen by
// --log-level or MONOAGENT_LOG_LEVEL, --verbose, --json, or --log-file (the lines then go to
// the file, not the terminal) leaves them as they are. The daemon
// and serve build their engines elsewhere and keep these lines.
// It returns the undo (a no-op when nothing was demoted).
func quietRunFailureLogs(cfg *globalConfig) (undo func()) {
	if cfg.JSONOutput || cfg.Verbose || cfg.LogFile != "" || logging.LevelChosen() {
		return func() {}
	}
	return logging.Demote(slog.LevelInfo, oneShotRunLogs...)
}

func newWorkflowRunCmd(cfg *globalConfig) *cobra.Command {
	return workflowRunDeps(cfg).NewRunCmd()
}

func newWorkflowTemplatesRunCmd(cfg *globalConfig) *cobra.Command {
	return workflowRunDeps(cfg).NewTemplatesRunCmd()
}

func newWorkflowExecutionCmd(cfg *globalConfig) *cobra.Command {
	return workflowRunDeps(cfg).NewExecutionCmd()
}

func newWorkflowCancelCmd(cfg *globalConfig) *cobra.Command {
	return workflowRunDeps(cfg).NewCancelCmd()
}

// checkTemplateNodeTypes refuses a template whose node types are not installed.
var checkTemplateNodeTypes = workflowrun.CheckTemplateNodeTypes
