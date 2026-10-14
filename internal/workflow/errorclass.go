package workflow

import (
	"context"
	"errors"
	"regexp"
	"strconv"
	"strings"

	"github.com/monoes/mono-agent/internal/account"
	"github.com/monoes/mono-agent/internal/monomind"
)

// ErrorClass names the kind of failure a node or execution ended with, so a
// person (or an error workflow) can tell "fix the config" from "try again
// later" without reading the message. Stored as error_class on
// workflow_execution_nodes and workflow_executions.
type ErrorClass string

const (
	ClassInvalidConfig      ErrorClass = "invalid_config"
	ClassNotSetup           ErrorClass = "not_setup"
	ClassBrowserUnavailable ErrorClass = "browser_unavailable"
	ClassSelectorMissing    ErrorClass = "selector_missing"
	ClassAuth               ErrorClass = "auth"
	ClassRateLimited        ErrorClass = "rate_limited"
	ClassUpstream5xx        ErrorClass = "upstream_5xx"
	ClassTimeout            ErrorClass = "timeout"
	ClassRejected           ErrorClass = "rejected"
	ClassCancelled          ErrorClass = "cancelled"
	ClassUnknown            ErrorClass = "unknown"
)

// AllErrorClasses lists every class ClassifyError can return.
var AllErrorClasses = []ErrorClass{
	ClassInvalidConfig, ClassNotSetup, ClassBrowserUnavailable, ClassSelectorMissing,
	ClassAuth, ClassRateLimited, ClassUpstream5xx, ClassTimeout, ClassRejected,
	ClassCancelled, ClassUnknown,
}

// ErrRejected is wrapped by a node whose item a person turned down
// (core.human_in_loop with no `rejected` edge wired). The engine finishes
// such a run as REJECTED, not FAILED. Its text stays the message a
// rejection always had ("human_in_loop: item rejected by human reviewer").
var ErrRejected = errors.New("item rejected by human reviewer")

// StatusRejected is the terminal status of a run a human reviewer stopped.
const StatusRejected = "REJECTED"

var errorHints = map[ErrorClass]string{
	ClassInvalidConfig:      "Fix the node's configuration: `monoagentcli workflow validate` checks the workflow and `monoagentcli node schema <type>` lists the fields.",
	ClassNotSetup:           "Something the node needs is not installed or connected yet. Run `monoagentcli doctor` and follow what it reports.",
	ClassBrowserUnavailable: "No browser was available. Connect the Chrome extension (or start the browser bridge) and run again.",
	ClassSelectorMissing:    "The page no longer has the element the step looks for; the site layout probably changed. Update the action's selectors.",
	ClassAuth:               "The service refused the credentials or session. Log in again or update the credential or secret the node uses.",
	ClassRateLimited:        "The service is rate limiting requests. Wait before retrying, lower the batch size, or add a retry_policy with backoff.",
	ClassUpstream5xx:        "The remote service had a server error. This is usually temporary: retry later or add a retry_policy.",
	ClassTimeout:            "The step took too long. Raise the node's timeout, reduce its input, or check that the service is reachable.",
	ClassRejected:           "A reviewer rejected the item. Wire the Human-in-Loop node's `rejected` output to handle rejections without failing the run.",
	ClassCancelled:          "The run was cancelled before it finished.",
	ClassUnknown:            "Read the error message and the node's input; `monoagentcli workflow execution <execution-id>` shows the whole run.",
}

// ErrorHint returns a one-line suggestion for fixing a failure of class c,
// or "" for the empty class.
func ErrorHint(c ErrorClass) string { return errorHints[c] }

// ClassifyError maps a node or execution error to its ErrorClass, checking
// the engine's and other packages' typed errors first and the message
// (HTTP status, well-known wording) after. nil classifies as "".
func ClassifyError(err error) ErrorClass {
	if err == nil {
		return ""
	}
	switch {
	case errors.Is(err, ErrRejected):
		return ClassRejected
	case errors.Is(err, ErrExecutionTimeout):
		// Before the cancel check: a run its executionTimeout ended is both.
		return ClassTimeout
	case errors.Is(err, ErrExecutionCancelled), errors.Is(err, context.Canceled):
		return ClassCancelled
	case errors.Is(err, ErrInvalidConfig), errors.Is(err, ErrNodeTypeUnknown),
		errors.Is(err, ErrDanglingConnection), errors.Is(err, ErrCycleDetected),
		errors.Is(err, ErrNoTriggerNode):
		return ClassInvalidConfig
	case monomind.IsAgentNotSetup(err):
		return ClassNotSetup
	case account.IsLoginRequired(err):
		return ClassAuth
	case errors.Is(err, context.DeadlineExceeded):
		return ClassTimeout
	}
	return ClassifyMessage(err.Error())
}

// httpStatusPattern finds an HTTP status in an error message: "status 502",
// "status code: 403", "HTTP 401", "HTTP/1.1 429", "(503)", "429 Too Many…".
var httpStatusPattern = regexp.MustCompile(`(?i)(?:status(?:\s+code)?|http(?:/[\d.]+)?)\s*[:=]?\s*([1-5]\d\d)\b|\(([45]\d\d)\)|\b([45]\d\d)\s+(?:too many|unauthori|forbidden|bad gateway|service unavailable|gateway time|internal server)`)

// messageRules are tried in order on the lower-cased message.
var messageRules = []struct {
	class ErrorClass
	words []string
}{
	{ClassRejected, []string{strings.ToLower(ErrRejected.Error())}},
	{ClassNotSetup, []string{monomind.AgentNotSetupMarker, "not set up", "not installed", "no credential", "credential not found"}},
	{ClassInvalidConfig, []string{strings.ToLower(ErrInvalidConfig.Error()), "invalid config", "unknown node type"}},
	{ClassCancelled, []string{"cancelled", "context canceled"}},
	{ClassAuth, []string{strings.ToLower(account.LoginRequiredMessage), "login_required", "unauthorized", "unauthorised", "forbidden",
		"not logged in", "login required", "invalid api key", "authentication failed", "invalid credentials", "session expired"}},
	{ClassBrowserUnavailable, []string{"extension not connected", "browser unavailable", "browser not available", "no browser",
		"chrome not found", "failed to launch browser", "could not launch browser"}},
	{ClassSelectorMissing, []string{"selector", "element not found", "element could not be located", "no such element"}},
	{ClassRateLimited, []string{"rate limit", "rate-limit", "too many requests", "quota exceeded"}},
	{ClassUpstream5xx, []string{"bad gateway", "service unavailable", "internal server error", "gateway timeout"}},
	{ClassTimeout, []string{"timed out", "timeout", "deadline exceeded"}},
}

// ClassifyMessage classifies a stored error message — all an execution row
// keeps of its error. "" classifies as "", anything unrecognised as unknown.
func ClassifyMessage(msg string) ErrorClass {
	if msg == "" {
		return ""
	}
	lower := strings.ToLower(msg)
	// The markers the engine itself writes win over a status number that
	// happens to appear in the text.
	for _, r := range messageRules[:3] {
		if containsAny(lower, r.words) {
			return r.class
		}
	}
	if c := classifyHTTPStatus(msg); c != "" {
		return c
	}
	for _, r := range messageRules[3:] {
		if containsAny(lower, r.words) {
			return r.class
		}
	}
	return ClassUnknown
}

func classifyHTTPStatus(msg string) ErrorClass {
	m := httpStatusPattern.FindStringSubmatch(msg)
	if m == nil {
		return ""
	}
	var code int
	for _, g := range m[1:] {
		if g != "" {
			code, _ = strconv.Atoi(g)
			break
		}
	}
	switch {
	case code == 429:
		return ClassRateLimited
	case code == 401 || code == 403 || code == 407:
		return ClassAuth
	case code == 408:
		return ClassTimeout
	case code >= 500:
		return ClassUpstream5xx
	}
	return ""
}

func containsAny(s string, words []string) bool {
	for _, w := range words {
		if strings.Contains(s, strings.ToLower(w)) {
			return true
		}
	}
	return false
}

// failureStatus is the terminal status of a run that ended with err: REJECTED
// when a reviewer turned an item down (the error wraps ErrRejected; text
// alone does not count), FAILED otherwise.
func failureStatus(err error) string {
	if errors.Is(err, ErrRejected) {
		return StatusRejected
	}
	return "FAILED"
}
