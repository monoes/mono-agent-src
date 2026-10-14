// Package automation is most of `monoagentcli automation`: list, show, new, test, doctor,
// trust and rerecord. validate, pack, install, export and the lifecycle commands stay in the
// CLI, which shares their install pipeline with `action`, `library` and workflow bundles.
// Everything these commands need from the CLI around them comes in through Deps, so they can
// be tested in isolation.
package automation

import (
	"encoding/json"
	"fmt"
	"io"
	"os"
	"strings"
	"time"

	"github.com/olekukonko/tablewriter"
	"github.com/olekukonko/tablewriter/tw"
	"github.com/rs/zerolog"
	"github.com/spf13/cobra"

	"github.com/monoes/mono-agent/internal/automation"
	browserpkg "github.com/monoes/mono-agent/internal/browser"
	"github.com/monoes/mono-agent/internal/storage"
)

// Deps is what the commands take from the CLI around them.
type Deps struct {
	// JSONOutput reports the global --json flag; it is read when a command runs.
	JSONOutput func() bool
	// ProfileID is the profile the command runs in once OpenDB has resolved it; it is read when
	// a command runs.
	ProfileID func() string
	// Verbose reports the global --verbose flag.
	Verbose func() bool
	// DBPath is the database file the command is pointed at, ~ expanded. Commands that only
	// read login state and selector health skip the database when it does not exist yet.
	DBPath func() string
	// OpenDB opens (and migrates) that database.
	OpenDB func() (*storage.Database, error)
	// OpenRegistry opens the automation registry (the CLI's openAutomationRegistry).
	OpenRegistry func() (*automation.Registry, error)
	// NewTable is the CLI's borderless table with these headers and per-column alignment.
	NewTable func(w io.Writer, headers []string, perColumnAlign []tw.Align) *tablewriter.Table
	// WithJSONErrors makes a command print its error as the CLI's --json error document.
	WithJSONErrors func(cmd *cobra.Command)
	// Reported marks err as already reported on stdout: the CLI exits 1 without printing it.
	Reported func(err error) error
	// WithInstallResult keeps a failed install's result on its error for --json.
	WithInstallResult func(err error, res *automation.InstallResult) error
	// SetupBridge is the browser extension bridge for a profile (the CLI's setupProfileBridge).
	SetupBridge func(profileID string, logger zerolog.Logger, wait time.Duration) browserpkg.ExtensionBridge
	// EnsureConnected waits up to timeout for the bridge's browser (the CLI's
	// ensureExtensionConnected).
	EnsureConnected func(bridge browserpkg.ExtensionBridge, timeout time.Duration) error
}

// Commands returns the `automation` subcommands this package owns.
func Commands(d Deps) []*cobra.Command {
	return []*cobra.Command{
		d.newAutomationListCmd(),
		d.newAutomationShowCmd(),
		d.newAutomationNewCmd(),
		d.newAutomationTestCmd(),
		d.newAutomationDoctorCmd(), // doctor.go (health builder)
		d.newAutomationTrustCmd(),
		d.newAutomationRerecordCmd(),
	}
}

// PrintIssues prints validation issues (`automation validate` and `install` share it).
var PrintIssues = printIssues

// browserProfile is the profile whose browser commands drive: the active one unless
// --profile names another (the CLI's browserProfile).
func (d Deps) browserProfile() string {
	db, err := d.OpenDB()
	if err != nil {
		return d.ProfileID()
	}
	db.Close()
	return d.ProfileID()
}

// writeJSONTo writes v as indented JSON to w (the CLI's writeJSONTo).
func writeJSONTo(w io.Writer, v any) error {
	enc := json.NewEncoder(w)
	enc.SetIndent("", "  ")
	return enc.Encode(v)
}

// stderrf prints a note on stderr (kept off stdout so --json stays parseable).
func stderrf(format string, a ...any) {
	fmt.Fprintf(os.Stderr, format, a...)
}

// truncateStr shortens s to at most n characters, appending "..." if truncated (the CLI's
// truncateStr).
func truncateStr(s string, n int) string {
	if len(s) <= n {
		return s
	}
	if n <= 3 {
		return s[:n]
	}
	return s[:n-3] + "..."
}

// safeName reports whether s is usable as one path element (the CLI's safeName).
func safeName(s string) bool {
	return s != "" && !strings.ContainsAny(s, `/\`) && !strings.Contains(s, "..") && !strings.HasPrefix(s, ".")
}

// The --json shapes the GUI parses (contracts §5), and helpers the CLI's end-to-end tests use.
type (
	// ListRow is one automation in `automation list --json`.
	ListRow = automationListRow
	// ActionJSON is one action in `automation show --json`.
	ActionJSON = automationActionJSON
	// FixtureResult is one result in `automation test --json`.
	FixtureResult = fixtureResult
)

// CountProblems counts the issues that are errors or warnings (info notes are not problems).
var CountProblems = countProblems

// TemplatesRoot is data/automation-templates, the templates of `automation new`.
var TemplatesRoot = automationTemplatesRoot

// TemplateNames lists the templates under root.
var TemplateNames = automationTemplateNames
