// Package agent is `monoagentcli agent`: detect, test, install and validate the local AI agent
// runtimes through the monomind engine, and keep the roster of the models that answered.
// Everything the commands need from the CLI around them comes in through Deps, so they can be
// tested in isolation. The daemon's automatic re-validation scheduler stays in the CLI and uses
// AutoRosterPick and LockValidation.
package agent

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"strings"
	"time"

	"github.com/olekukonko/tablewriter"
	"github.com/olekukonko/tablewriter/tw"
	"github.com/spf13/cobra"

	"github.com/monoes/mono-agent/internal/agentinstall"
	"github.com/monoes/mono-agent/internal/agentroster"
	"github.com/monoes/mono-agent/internal/monomind"
	"github.com/monoes/mono-agent/internal/storage"
)

// Deps is what the commands take from the CLI around them.
type Deps struct {
	// JSONOutput reports the global --json flag; it is read when a command runs.
	JSONOutput func() bool
	// OpenDB opens (and migrates) the database the command is pointed at.
	OpenDB func() (*storage.Database, error)
	// NotFound and InvalidInput build the CLI's errors (exit codes 2 and 3).
	NotFound     func(format string, a ...interface{}) error
	InvalidInput func(format string, a ...interface{}) error
	// NewTable is the CLI's borderless table with these headers and, optionally, one alignment
	// per column.
	NewTable func(w io.Writer, headers []string, perColumnAlign []tw.Align) *tablewriter.Table
	// ProgressWriter prints one progress line of a streamed command: indented text, or an
	// NDJSON line event with --json (as `doctor fix` streams).
	ProgressWriter func(w io.Writer, asJSON bool) func(string)
	// FinishStreamed ends a streamed command: the done or error event with --json, else a ✓
	// line or the error.
	FinishStreamed func(out io.Writer, asJSON bool, err error, okMsg string) error
	// StdinIsTerminal says whether standard input is a terminal, where a command may ask.
	StdinIsTerminal func() bool
}

// NewCommand returns the `agent` command group.
func NewCommand(d Deps) *cobra.Command { return d.newAgentCmd() }

// NewTestCommand returns `agent test` on its own.
func NewTestCommand(d Deps) *cobra.Command { return d.newAgentTestCmd() }

// RuntimeMachine is what InstallRuntime needs from this machine.
type RuntimeMachine = runtimeMachine

// RealRuntimeMachine is the machine InstallRuntime runs on outside tests.
func RealRuntimeMachine() RuntimeMachine { return realRuntimeMachine() }

// InstallRuntime installs one runtime by its `agent scan` id and verifies it with a re-scan;
// confirmScript gates vendor install scripts. It returns the message to report.
func (d Deps) InstallRuntime(ctx context.Context, m RuntimeMachine, id string, force bool, confirmScript func(agentinstall.Recipe) bool,
	progress func(string)) (string, error) {
	return d.installRuntime(ctx, m, id, force, confirmScript, progress)
}

// ShellQuote quotes s for a POSIX shell when it needs it.
func ShellQuote(s string) string { return shellQuote(s) }

// AutoRosterPick builds the roster (with scan, for version staleness; nil when there is none)
// and picks the next runtime's stale models, at most maxModels.
func AutoRosterPick(ctx context.Context, db *sql.DB, scan *monomind.ScanResult, maxModels int) (*agentroster.AutoPlan, error) {
	return autoRosterPick(ctx, db, scan, maxModels)
}

// LockValidation takes the lock every validation run takes, so a manual `agent validate` and
// the daemon's automatic one never overlap.
func LockValidation() (release func(), err error) { return lockValidation() }

// parseDurationFlag reads a Go duration or a number of seconds ("" is 0) (the CLI's
// parseDurationFlag).
func parseDurationFlag(s string) (time.Duration, error) {
	s = strings.TrimSpace(s)
	if s == "" {
		return 0, nil
	}
	if d, err := time.ParseDuration(s); err == nil {
		return d, nil
	}
	var secs float64
	if _, err := fmt.Sscanf(s, "%f", &secs); err == nil {
		return time.Duration(secs * float64(time.Second)), nil
	}
	return 0, fmt.Errorf("invalid duration %q (use e.g. 90s, 10m, 2h)", s)
}

// printJSON writes v to standard output as indented JSON (the CLI's printJSON).
func printJSON(v interface{}) error {
	enc := json.NewEncoder(os.Stdout)
	enc.SetIndent("", "  ")
	return enc.Encode(v)
}

// containsString reports whether list holds s.
func containsString(list []string, s string) bool {
	for _, v := range list {
		if v == s {
			return true
		}
	}
	return false
}
