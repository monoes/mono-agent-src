package main

import (
	"context"
	"os"

	"github.com/spf13/cobra"

	taskcli "github.com/monoes/mono-agent/internal/cli/task"
	"github.com/monoes/mono-agent/internal/storage"
	"github.com/monoes/mono-agent/internal/tasks"
)

// `task` lives in internal/cli/task; this is its adapter: the CLI's globals and helpers handed
// over as taskcli.Deps. `task os` (task_os.go) stays here and is added to the group.

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
	return d
}

// newTaskCmd groups the task board of a profile (spec:
// docs/mastermind/specs/2026-10-05-task-board-design.md).
func newTaskCmd(cfg *globalConfig) *cobra.Command {
	cmd := taskcli.NewCommand(taskDeps(cfg))
	cmd.AddCommand(newTaskOSCmd(cfg))
	return cmd
}

// taskCaller says who runs a command: the operator, or an AI agent.
type taskCaller struct{ taskcli.Caller }

// callerFor says who runs a command, from the --as value flagAs gave.
func callerFor(as string) taskCaller { return taskCaller{taskCallerDeps.CallerFor(as)} }

// operator returns the operator's actor, or the refusal an agent-driven caller gets.
func (c taskCaller) operator(what string) (tasks.Actor, error) { return c.Operator(what) }

// flagAs reads the --as flag of the task group.
func flagAs(cmd *cobra.Command) string { return taskcli.FlagAs(cmd) }

// withTasks opens the database and hands fn the store and the resolved profile.
func withTasks(cfg *globalConfig, cmd *cobra.Command, fn func(ctx context.Context, store *tasks.Store, p tasks.Profile) error) error {
	return taskDeps(cfg).WithTasks(cmd, fn)
}

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

// cutArg is s cut for an error message that repeats it.
func cutArg(s string) string { return taskcli.CutArg(s) }
