// Package aitask is a goal handed to a coder conversation, with a result the
// agent hands back: the model behind `monoagentcli aitask`. The package holds
// the task rows, the kind registry, the result schema check, the context
// folder and the first-turn prompt; the commands and the process launching
// live in internal/cli/aitask.
package aitask

import "fmt"

// Error codes a caller can branch on (Contract A).
const (
	CodeCoderDisabled      = "coder_disabled"
	CodeUnknownKind        = "unknown_kind"
	CodeUnattendedFull     = "unattended_full_access_refused"
	CodeSchemaInvalid      = "schema_invalid"
	CodeNotFound           = "not_found"
	CodeInvalidInput       = "invalid_input"
	CodeBadToken           = "bad_token"
	CodeTaskClosed         = "task_closed"
	CodeContextInvalid     = "context_invalid"
	CodeWaitTimeout        = "wait_timeout"
	CodeNotQueued          = "not_queued"
	CodeTaskNotOnYourBoard = "not_your_task"
	CodeContextSource      = "context_source_refused"
	CodeUnattendedNoJail   = "unattended_not_confined"
	CodeNoBudget           = "no_budget"
	CodeContextChanged     = "context_changed"
	CodeCapsChanged        = "capabilities_changed"
	CodeAccessRaised       = "access_above_kind"
	CodeAccessUnenforced   = "access_unenforceable"
)

// Error is a refusal with a machine-readable code. Fields are added to the
// --json error document.
type Error struct {
	Code    string
	Message string
	Fields  map[string]any
}

func (e *Error) Error() string { return e.Message }

func errf(code, format string, a ...any) *Error {
	return &Error{Code: code, Message: fmt.Sprintf(format, a...)}
}

// BudgetHint ends every no_budget refusal at start: where a budget is set.
const BudgetHint = "set a budget in Settings > AI task budgets or run: monoagentcli aitask budget set --scope global --usd USD (operator only)"
