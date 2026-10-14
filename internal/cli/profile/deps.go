// Package profile is `monoagentcli profile`: profiles, their folders and their documents.
// Everything the commands need from the CLI around them comes in through Deps, so they can be
// tested in isolation.
package profile

import (
	"database/sql"
	"io"

	"github.com/olekukonko/tablewriter"
	"github.com/spf13/cobra"

	"github.com/monoes/mono-agent/internal/storage"
)

// Deps is what the commands take from the CLI around them.
type Deps struct {
	// JSONOutput reports the global --json flag; it is read when a command runs.
	JSONOutput func() bool
	// ProfileID is the profile the command runs in once OpenDB has resolved it; it is read when
	// a command runs.
	ProfileID func() string
	// OpenDB opens (and migrates) the database the command is pointed at.
	OpenDB func() (*storage.Database, error)
	// NotFound builds the CLI's not-found error (exit code 2).
	NotFound func(format string, a ...interface{}) error
	// InvalidInput builds the CLI's invalid-input error (exit code 3).
	InvalidInput func(format string, a ...interface{}) error
	// NewTable is the CLI's borderless table with these headers.
	NewTable func(w io.Writer, headers []string) *tablewriter.Table
	// WithJSONErrors makes a command print its error as the CLI's --json error document.
	WithJSONErrors func(cmd *cobra.Command)
	// Reported marks err as already reported on stdout: the CLI exits 1 without printing it.
	Reported func(err error) error
}

// NewCommand returns the `profile` command group.
func NewCommand(d Deps) *cobra.Command { return d.newProfileCmd() }

// Profile is one profile as `profile list/get/create --json` print it.
type Profile = profileJSON

// LookupProfile resolves nameOrID to a profile: an exact id wins over a name. An unknown one
// is d.NotFound.
func (d Deps) LookupProfile(db *sql.DB, nameOrID string) (*Profile, error) {
	return d.lookupProfile(db, nameOrID)
}

// FolderSyncResult is the --json shape of `profile documents sync` and `image sync`.
type FolderSyncResult = folderSyncResult

// NewFolderSyncResult is what one reconcile of a profile folder changed.
var NewFolderSyncResult = newFolderSyncResult

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
