// Package capturecli is `monoagentcli capture` (list, classify, export, import): the browser
// captures in the monomind inbox (the store is internal/capture's), and the capture helpers the
// extension bridge shares. Everything the commands need from the CLI around them comes in
// through Deps, so they can be tested in isolation. `capture page` (the extension bridge) and
// `capture task` (the org boards) stay in the CLI, which adds them to the group NewCommand
// returns.
package capturecli

import (
	"database/sql"
	"io"
	"os"
	"path/filepath"
	"strings"

	"github.com/olekukonko/tablewriter"
	"github.com/spf13/cobra"

	"github.com/monoes/mono-agent/internal/storage"
)

// Deps is what the commands take from the CLI around them.
type Deps struct {
	// JSONOutput reports the global --json flag; it is read when a command runs.
	JSONOutput func() bool
	// ProfileID is the global --profile flag (an id or a name), else the active profile once
	// OpenDB has resolved it; it is read when a command runs.
	ProfileID func() string
	// DBPath is the global --db-path flag.
	DBPath func() string
	// OpenDB opens (and migrates) the database the command is pointed at.
	OpenDB func() (*storage.Database, error)
	// NotFound builds the CLI's not-found error (exit code 2).
	NotFound func(format string, a ...interface{}) error
	// InvalidInput builds the CLI's invalid-input error (exit code 3).
	InvalidInput func(format string, a ...interface{}) error
	// NewTable is the CLI's borderless table with these headers.
	NewTable func(w io.Writer, headers []string) *tablewriter.Table
}

// NewCommand returns the `capture` command group, without `page` and `task`.
func NewCommand(d Deps) *cobra.Command { return d.newCaptureCmd() }

// DefaultDBPath mirrors the --db-path flag default.
const DefaultDBPath = defaultDBPath

// Profile is a profile a capture command was pointed at, with its inbox.
type Profile = captureProfile

// ResolveProfile turns what the user typed after --profile into a profile and its inbox; an
// unknown one is d.InvalidInput.
func (d Deps) ResolveProfile(db *sql.DB, idOrName string) (Profile, error) {
	return d.resolveCaptureProfile(db, idOrName)
}

// The helpers the rest of the CLI shares.
var (
	OpenProfileDB          = openProfileDB
	ExtensionProfileSource = extensionProfileSource
	NewClassifier          = newCaptureClassifier
	HumanBytes             = captureHumanBytes
	TruncateCell           = truncateCaptureCell
	SlicesContains         = slicesContains
)

// expandPath expands a leading "~/" to the home directory (the CLI's expandPath).
func expandPath(path string) string {
	if strings.HasPrefix(path, "~/") {
		home, err := os.UserHomeDir()
		if err != nil {
			return path
		}
		return filepath.Join(home, path[2:])
	}
	return path
}
