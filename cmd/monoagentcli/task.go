package main

import (
	"os"

	"github.com/spf13/cobra"

	taskcli "github.com/monoes/mono-agent/internal/cli/task"
	"github.com/monoes/mono-agent/internal/storage"
	"github.com/monoes/mono-agent/internal/tasks"
)

// `task` (with `task os`, the macOS Services menu) lives in internal/cli/task; this is its
// adapter: the CLI's globals and helpers handed over as taskcli.Deps.

// maxStdinBytes is how much of standard input `task add --stdin` reads.
const maxStdinBytes = taskcli.MaxStdinBytes

// agentsAllowStdinIsTerminal says whether `task agents allow` runs at a terminal.
var agentsAllowStdinIsTerminal = stdinIsTerminal

// taskCallerDeps is the part of taskDeps that needs no globalConfig: who the caller is (from
// this process's environment) and the CLI's errors.
var taskCallerDeps = taskcli.Deps{Getenv: os.Getenv, NotFound: errNotFound, InvalidInput: errInvalidInput}

func taskDeps(cfg *globalConfig) taskcli.Deps {
	d := taskCallerDeps
	d.JSONOutput = func() bool { return cfg.JSONOutput }
	d.ProfileID = func() string { return cfg.ProfileID }
	d.OpenDB = func() (*storage.Database, error) { return initDB(cfg) }
	d.WriteJSON = writeJSONTo
	d.WithJSONErrors = func(cmd *cobra.Command) { withJSONErrors(cfg, cmd) }
	d.StdinIsTerminal = func() bool { return agentsAllowStdinIsTerminal() }
	d.DBPath = func() string { return cfg.DBPath }
	d.OpenDBActive = func() (*storage.Database, string, error) {
		open := &globalConfig{DBPath: cfg.DBPath}
		db, err := initDB(open)
		return db, open.ProfileID, err
	}
	return d
}

// newTaskCmd groups the task board of a profile (spec:
// docs/mastermind/specs/2026-10-05-task-board-design.md).
func newTaskCmd(cfg *globalConfig) *cobra.Command {
	return taskcli.NewCommand(taskDeps(cfg))
}

// taskCaller says who runs a command: the operator, or an AI agent.
type taskCaller struct{ taskcli.Caller }

// callerFor says who runs a command, from the --as value flagAs gave.
func callerFor(as string) taskCaller { return taskCaller{taskCallerDeps.CallerFor(as)} }

// operator returns the operator's actor, or the refusal an agent-driven caller gets.
func (c taskCaller) operator(what string) (tasks.Actor, error) { return c.Operator(what) }

// operatorOnlyError is the refusal of what only the operator may do: exit 3, and
// the code operator_only in the --json error document.
func operatorOnlyError(format string, a ...any) error {
	return taskCallerDeps.OperatorOnlyError(format, a...)
}

// taskErr maps a store error to the CLI's exit codes (2 not found, 3 invalid
// or refused) and to the code of the --json error document.
func taskErr(err error) error { return taskCallerDeps.TaskErr(err) }

// taskCut shortens s to at most n characters, ending in an ellipsis.
func taskCut(s string, n int) string { return taskcli.Cut(s, n) }
