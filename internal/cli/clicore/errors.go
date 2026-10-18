// Package clicore holds the helpers every monoagentcli command shares: the
// exit-code errors, the --json error envelope, JSON and table output, and the
// automation registry. cmd/monoagentcli keeps its old names for them as thin
// wrappers, and the internal/cli packages can import them directly.
package clicore

import (
	"errors"
	"fmt"

	"github.com/monoes/mono-agent/internal/account"
	"github.com/monoes/mono-agent/internal/monomind"
	"github.com/monoes/mono-agent/internal/workflow"
)

// Exit codes (see the exit-code table in AGENTS.md):
//
//	0 ok · 1 general error · 2 not-found · 3 invalid input / validation · 4 auth/connection
//
// Commands return sentinel-tagged errors; main.go maps them to the process
// exit code via ExitCodeFor so agents can branch on the code instead of
// parsing stderr.

// Error is an error carrying the process exit code it should map to.
type Error struct {
	Code int
	Msg  string
}

func (e *Error) Error() string { return e.Msg }

// Sentinel errors — wrap or construct with the NotFound / InvalidInput /
// AuthConnection helpers below, or test with errors.As(&Error).
var (
	ErrNotFound       = &Error{Code: 2, Msg: "not found"}
	ErrInvalidInput   = &Error{Code: 3, Msg: "invalid input"}
	ErrAuthConnection = &Error{Code: 4, Msg: "auth or connection failure"}
)

// NotFound is an exit-2 error.
func NotFound(format string, a ...interface{}) error {
	return &Error{Code: 2, Msg: fmt.Sprintf(format, a...)}
}

// InvalidInput is an exit-3 error.
func InvalidInput(format string, a ...interface{}) error {
	return &Error{Code: 3, Msg: fmt.Sprintf(format, a...)}
}

// operatorOnlyError is OperatorOnly's error: exit 3 (its *Error), and the
// code operator_only in the --json error document.
type operatorOnlyError struct{ err *Error }

func (e operatorOnlyError) Error() string { return e.err.Msg }
func (e operatorOnlyError) Unwrap() error { return e.err }

func (operatorOnlyError) JSONErrorFields() map[string]any {
	return map[string]any{"code": "operator_only"}
}

// OperatorOnly is the refusal of what only the operator may do: exit 3, and
// the code operator_only in the --json error document, so a caller tells it
// from invalid input.
func OperatorOnly(format string, a ...interface{}) error {
	return operatorOnlyError{err: &Error{Code: 3, Msg: fmt.Sprintf(format, a...)}}
}

// AuthConnection is an exit-4 error.
func AuthConnection(format string, a ...interface{}) error {
	return &Error{Code: 4, Msg: fmt.Sprintf(format, a...)}
}

// ExitCodeFor maps a command error to the process exit code. Error-based
// sentinels win; a few well-known internal/workflow sentinels are mapped so
// engine-returned errors (e.g. `workflow run` on an unknown id) classify
// without re-wrapping at every call site.
func ExitCodeFor(err error) int {
	if err == nil {
		return 0
	}
	var ce *Error
	if errors.As(err, &ce) {
		return ce.Code
	}
	// A layer-2 refusal that comes out of a command (the engine, an agent turn,
	// a browser action) is the same login_required failure the CLI gate reports.
	var lr *account.LoginRequiredError
	if errors.As(err, &lr) {
		return 4
	}
	// A chat turn's terminal *monomind.ProtocolError carries the specific
	// protocol/process exit code (e.g. 124 timeout, 130 cancelled) that
	// `chat`'s RunE now returns instead of calling os.Exit directly, so that
	// path's deferred cleanup (closeDB, temp-file removal, history save)
	// still runs. Falling through to the default case here would collapse
	// every one of those distinct codes to a generic 1.
	var pe *monomind.ProtocolError
	if errors.As(err, &pe) {
		return pe.ExitCode
	}
	switch {
	case errors.Is(err, workflow.ErrWorkflowNotFound),
		errors.Is(err, workflow.ErrExecutionNotFound):
		return 2
	case errors.Is(err, workflow.ErrNoTriggerNode),
		errors.Is(err, workflow.ErrCycleDetected),
		errors.Is(err, workflow.ErrNodeTypeUnknown),
		errors.Is(err, workflow.ErrInvalidConfig),
		errors.Is(err, workflow.ErrDanglingConnection):
		return 3
	default:
		return 1
	}
}
