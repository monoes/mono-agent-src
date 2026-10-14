// Package summarycli is `monoagentcli summary`: the at-a-glance roll-up (the sections are
// internal/summary's). Everything the command needs from the CLI around it comes in through
// Deps, so it can be tested in isolation.
package summarycli

import (
	"io"
	"os"
	"path/filepath"
	"strings"
	"unicode/utf8"

	"github.com/spf13/cobra"

	"github.com/monoes/mono-agent/internal/automation"
	"github.com/monoes/mono-agent/internal/storage"
)

// Deps is what the command takes from the CLI around it.
type Deps struct {
	// JSONOutput reports the global --json flag; it is read when the command runs.
	JSONOutput func() bool
	// ProfileID is the profile the command runs in once OpenDB has resolved it; it is read when
	// the command runs.
	ProfileID func() string
	// OpenDB opens (and migrates) the database the command is pointed at.
	OpenDB func() (*storage.Database, error)
	// InvalidInput builds the CLI's invalid-input error (exit code 3).
	InvalidInput func(format string, a ...interface{}) error
	// WriteJSON writes the --json document.
	WriteJSON func(w io.Writer, v any) error
	// ProbeAddrs lists where the extension bridge may answer (the CLI's extensionProbeAddrs).
	ProbeAddrs func() []string
	// OpenAutomationRegistry opens the installed automation packages.
	OpenAutomationRegistry func() (*automation.Registry, error)
}

// NewCommand returns the `summary` command.
func NewCommand(d Deps) *cobra.Command { return d.newSummaryCmd() }

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

// taskCut shortens s to at most n characters, ending in an ellipsis (the CLI's taskCut).
func taskCut(s string, n int) string {
	if n < 1 {
		return ""
	}
	if utf8.RuneCountInString(s) <= n {
		return s
	}
	return string([]rune(s)[:n-1]) + "…"
}
