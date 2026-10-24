// Package doctor is `monoagentcli doctor` (health checks over everything monoagent needs,
// with their fixes) and `monoagentcli setup` (the guided first setup built on them). The
// checks live in internal/health; the real machine they run against (the health.Env) is built
// by the CLI around the commands and comes in through Deps, as does everything else it owns.
package doctor

import (
	"io"

	"github.com/spf13/cobra"

	"github.com/monoes/mono-agent/internal/health"
)

// Deps is what the commands take from the CLI around them.
type Deps struct {
	// JSONOutput reports the global --json flag; it is read when a command runs.
	JSONOutput func() bool
	// NewEnv builds the real-machine environment the checks and fixes run against, for the
	// monomind projects named by --project (nil: all of them), and its cleanup.
	NewEnv func(projects []string) (*health.Env, func())
	// FinishStreamed ends a command whose progress was streamed with ProgressWriter.
	FinishStreamed func(out io.Writer, asJSON bool, err error, okMsg string) error

	// projects is doctor's --project.
	projects []string
	// agentAccount is doctor's --agent-account.
	agentAccount string
}

// NewDoctorCommand is `monoagentcli doctor`.
func NewDoctorCommand(d Deps) *cobra.Command { return newDoctorCmd(&d) }

// NewSetupCommand is `monoagentcli setup`.
func NewSetupCommand(d Deps) *cobra.Command { return newSetupCmd(&d) }

// FixEvent is one NDJSON progress event of a streamed fix (doctor fix, setup, agent install,
// nodejs install).
type FixEvent = fixEvent

// WriteFixEvent writes ev as one NDJSON line.
func WriteFixEvent(w io.Writer, ev FixEvent) { writeFixEvent(w, ev) }

// ProgressWriter streams fix output: NDJSON line events in JSON mode, indented text otherwise.
func ProgressWriter(w io.Writer, asJSON bool) func(string) { return progressWriter(w, asJSON) }

// CaptureStdLog sends the standard logger's output to progress until restore is called.
func CaptureStdLog(progress func(string)) (restore func()) { return captureStdLog(progress) }

// Report is the `doctor --json` document (schema v1): the report plus, with --fix, what
// happened to each fix.
type Report = doctorJSON

// SetupReport is the `setup --json` document.
type SetupReport = setupJSON

// FixOutcome is what happened to one fix.
type FixOutcome = fixOutcome
