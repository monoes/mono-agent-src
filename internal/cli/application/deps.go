// Package application is `monoagentcli application`: tracking job and tender applications (the
// store is internal/applications'). Everything the commands need from the CLI around them comes in
// through Deps, so they can be tested in isolation.
package application

import (
	"io"

	"github.com/olekukonko/tablewriter"

	browserpkg "github.com/monoes/mono-agent/internal/browser"
	"github.com/monoes/mono-agent/internal/storage"
)

// Deps is what the commands take from the CLI around them.
type Deps struct {
	// JSONOutput reports the global --json flag; it is read when a command runs.
	JSONOutput func() bool
	// ProfileID is the active profile (--profile, resolved); it is read when a command runs.
	ProfileID func() string
	// OpenDB opens (and migrates) the database the command is pointed at.
	OpenDB func() (*storage.Database, error)
	// NotFound builds the CLI's not-found error (exit code 2).
	NotFound func(format string, a ...interface{}) error
	// InvalidInput builds the CLI's invalid-input error (exit code 3).
	InvalidInput func(format string, a ...interface{}) error
	// NewTable is the CLI's borderless table with these headers.
	NewTable func(w io.Writer, headers []string) *tablewriter.Table
	// Bridge connects the MonoAgent extension that `apply` opens the job posting through.
	Bridge func(profileID string) (browserpkg.ExtensionBridge, error)
	// Confirm asks a y/N question before `apply` in confirm mode; true only on an explicit yes.
	Confirm func(prompt string) bool
}

// truncate shortens s to n bytes for a table cell, ending in "..." when it cuts (the CLI's
// truncateStr).
func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	if n <= 3 {
		return s[:n]
	}
	return s[:n-3] + "..."
}
