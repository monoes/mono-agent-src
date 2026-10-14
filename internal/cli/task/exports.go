package task

import (
	"context"

	"github.com/spf13/cobra"

	"github.com/monoes/mono-agent/internal/tasks"
)

// What the rest of the CLI shares with the task board: `task os`, which stays in the CLI, works
// on the board with the same caller and the same errors, and other commands refuse an agent
// what only the operator may do in the same words.

// MaxStdinBytes is how much of standard input `task add --stdin` reads.
const MaxStdinBytes = maxStdinBytes

// FlagAs reads the --as flag of the task group (blank but given is not "no --as").
func FlagAs(cmd *cobra.Command) string { return flagAs(cmd) }

// CallerFor says who runs a command, from the --as value FlagAs gave ("" for none).
func (d Deps) CallerFor(as string) Caller { return d.callerFor(as) }

// Operator returns the operator's actor, or the refusal an agent-driven caller gets: exit 3,
// code operator_only.
func (c Caller) Operator(what string) (tasks.Actor, error) { return c.operator(what) }

// WithTasks opens the database and hands fn the store and the resolved profile.
func (d Deps) WithTasks(cmd *cobra.Command, fn func(ctx context.Context, store *tasks.Store, p tasks.Profile) error) error {
	return d.withTasks(cmd, fn)
}

// TaskErr maps a store error to the CLI's exit codes (2 not found, 3 invalid or refused) and to
// the code of the --json error document.
func (d Deps) TaskErr(err error) error { return d.taskErr(err) }

// OperatorOnlyError is the refusal of what only the operator may do: exit 3, and the code
// operator_only in the --json error document.
func (d Deps) OperatorOnlyError(format string, a ...any) error {
	return d.operatorOnlyError(format, a...)
}

// Cut shortens s to at most n characters, ending in an ellipsis, as the board cuts a title.
func Cut(s string, n int) string { return taskCut(s, n) }

// CutArg is s cut for an error message that repeats it.
func CutArg(s string) string { return cutArg(s) }
