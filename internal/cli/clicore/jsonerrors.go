package clicore

import (
	"encoding/json"
	"errors"
	"fmt"

	"github.com/spf13/cobra"

	"github.com/monoes/mono-agent/internal/automation"
	"github.com/monoes/mono-agent/internal/monomind"
)

// WithJSONErrors makes a failing command also print {"error": "…"} on
// stdout when --json is set (contracts §5), with the extra fields of an
// install result. main still prints the error to stderr and exits non-zero.
func WithJSONErrors(jsonOutput func() bool, cmd *cobra.Command) {
	run := cmd.RunE
	if run == nil {
		return
	}
	cmd.RunE = func(c *cobra.Command, args []string) error {
		err := run(c, args)
		var rep Reported
		if err != nil && jsonOutput() && !errors.As(err, &rep) {
			body := map[string]any{"error": err.Error()}
			if code := JSONErrorCode(err); code != "" {
				body["code"] = code
			}
			var re *InstallResultError
			if errors.As(err, &re) {
				body["issues"], body["result"] = re.Res.Issues, re.Res
			}
			// An error that knows its machine-readable form (a code, the
			// keys involved) adds it, so callers need not match the text.
			var fields JSONErrorFields
			if errors.As(err, &fields) {
				for k, v := range fields.JSONErrorFields() {
					if k != "error" {
						body[k] = v
					}
				}
			}
			b, _ := json.Marshal(body)
			fmt.Fprintln(c.OutOrStdout(), string(b))
			// Printed: main adds no second {"error"} for a Reported.
			return Reported{err}
		}
		return err
	}
}

// JSONErrorFields is implemented by errors that carry machine-readable
// detail for `--json` (e.g. recordanalyze.SelectorConflictError: code
// "selector_conflict" and the conflicting keys).
type JSONErrorFields interface {
	JSONErrorFields() map[string]any
}

// JSONErrorCode names a classified error for {"error","code"}:
// agent_not_setup (the AI agent is not installed or not logged in; its
// exit code is unchanged), else its exit-code class — not_found (2),
// invalid_input (3), auth_or_connection (4); "" for a plain error (exit 1),
// which prints {"error"} alone.
func JSONErrorCode(err error) string {
	if monomind.IsAgentNotSetup(err) {
		return monomind.AgentNotSetupCode
	}
	switch ExitCodeFor(err) {
	case 2:
		return "not_found"
	case 3:
		return "invalid_input"
	case 4:
		return "auth_or_connection"
	}
	return ""
}

// Reported is a failure whose details the command already printed
// (e.g. `automation test` results): it sets the exit code, and --json
// prints no extra {"error"} object after the output.
type Reported struct{ Err error }

func (e Reported) Error() string { return e.Err.Error() }
func (e Reported) Unwrap() error { return e.Err }

// Shown is a failure the command's human-mode output already showed (a run's
// "Status: FAILED / Error: …" summary): main exits with its code but does
// not print it again. With --json it is reported as any other error.
type Shown struct{ Err error }

func (e Shown) Error() string { return e.Err.Error() }
func (e Shown) Unwrap() error { return e.Err }

// InstallResultError is an Install/AddAction failure that still carries the
// result (review + issues). With --json the error object adds "issues" and
// "result"; without, the issues are printed on stderr.
type InstallResultError struct {
	Err error
	Res *automation.InstallResult
}

func (e *InstallResultError) Error() string { return e.Err.Error() }
func (e *InstallResultError) Unwrap() error { return e.Err }

// WithInstallResult wraps err so the failed result is not lost.
func WithInstallResult(err error, res *automation.InstallResult) error {
	if err == nil || res == nil {
		return err
	}
	return &InstallResultError{Err: err, Res: res}
}
