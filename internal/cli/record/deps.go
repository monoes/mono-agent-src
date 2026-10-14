// Package record is `monoagentcli record`: the browser activity recordings the extension makes
// (list, show, delete, api, replay) and the drafts made from them (analyze, verify, save).
// Everything the commands need from the CLI around them comes in through Deps, so they can be
// tested in isolation; the browser connection stays in the CLI and comes in as a seam.
package record

import (
	"bufio"
	"context"
	"fmt"
	"io"
	"strings"
	"time"

	"github.com/rs/zerolog"
	"github.com/spf13/cobra"

	"github.com/monoes/mono-agent/internal/action"
	"github.com/monoes/mono-agent/internal/automation"
	browserpkg "github.com/monoes/mono-agent/internal/browser"
	"github.com/monoes/mono-agent/internal/recordanalyze"
	"github.com/monoes/mono-agent/internal/storage"
	"github.com/monoes/mono-agent/internal/workflow"
)

// Deps is what the commands take from the CLI around them.
type Deps struct {
	// JSONOutput reports the global --json flag; it is read when a command runs.
	JSONOutput func() bool
	// Verbose reports the global --verbose flag.
	Verbose func() bool
	// ProfileID is the --profile value as given (an id, a name or "").
	ProfileID func() string
	// BrowserProfile is the profile whose browser a replay or verify drives.
	BrowserProfile func() string
	// ResolveProfile turns a --profile id or name into the profile's id. When the name cannot
	// be resolved (no database, no such profile) it returns idOrName unchanged.
	ResolveProfile func(idOrName string) string
	// OpenDB opens (and migrates) the database the command is pointed at.
	OpenDB func() (*storage.Database, error)
	// StoreWorkflow persists a new workflow exactly as `workflow import` does.
	StoreWorkflow func(ctx context.Context, db *storage.Database, wf *workflow.Workflow) error
	// OpenRegistry opens the installed-automation registry.
	OpenRegistry func() (*automation.Registry, error)
	// InvalidInput builds the CLI's invalid-input error (exit code 3).
	InvalidInput func(format string, a ...interface{}) error
	// Reported marks an error whose details the command already printed, so the CLI adds no
	// second --json error document (exit code 1).
	Reported func(err error) error
	// WriteJSON writes the --json document.
	WriteJSON func(w io.Writer, v any) error
	// WithJSONErrors makes a command print its error as the CLI's --json error document.
	WithJSONErrors func(cmd *cobra.Command)
	// StdinIsTerminal says whether a person can be asked (replay's confirmation).
	StdinIsTerminal func() bool

	// AnalyzeRunner is the AI runner of analyze (production: ExecRunner).
	AnalyzeRunner func(runtime, model string, timeout time.Duration) recordanalyze.Runner
	// SecretLookup returns the vault lookup for an automation's secrets and its release, or a
	// nil lookup when the vault is unavailable (production: Deps.VaultSecretLookup).
	SecretLookup func(ctx context.Context, automationID string) (func(string) (string, bool), func())
	// SelfHeal returns the hook that installs the Jev element picker for replay --self-heal
	// (production: Deps.JevSelfHeal).
	SelfHeal func(ctx context.Context) (func(*action.ActionExecutor), func(), error)
	// VerifyExec returns the replay function for verify, or an error when no browser is
	// reachable.
	VerifyExec func(ctx context.Context, automationID string, verbose, keepOpen bool, secrets func(string) (string, bool)) (recordanalyze.ExecFunc, error)
	// ReplayPage opens the tab a replay drives.
	ReplayPage func(ctx context.Context, verbose bool) (browserpkg.PageInterface, zerolog.Logger, error)
}

// splitCSV splits a comma-separated flag value, dropping empty items (the CLI's splitCSV).
func splitCSV(s string) []string {
	var out []string
	for _, p := range strings.Split(s, ",") {
		if p = strings.TrimSpace(p); p != "" {
			out = append(out, p)
		}
	}
	return out
}

// confirmYes asks a y/N question on in; anything but y/yes is a no (the CLI's confirmYes).
func confirmYes(in io.Reader, out io.Writer, question string) bool {
	fmt.Fprintf(out, "%s [y/N]: ", question)
	line, _ := bufio.NewReader(in).ReadString('\n')
	switch strings.ToLower(strings.TrimSpace(line)) {
	case "y", "yes":
		return true
	}
	return false
}

// truncateCell keeps one table cell from wrapping the whole row (the CLI's truncateCaptureCell).
func truncateCell(s string, max int) string {
	r := []rune(s)
	if len(r) <= max {
		return s
	}
	return string(r[:max-1]) + "…"
}

// nonNilStrings is s, or an empty list for nil (a JSON [] rather than null).
func nonNilStrings(s []string) []string {
	if s == nil {
		return []string{}
	}
	return s
}
