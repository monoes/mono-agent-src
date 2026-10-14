package main

import (
	"database/sql"
	"encoding/json"
	"io"
	"os"

	"github.com/olekukonko/tablewriter"
	"github.com/spf13/cobra"

	profilecli "github.com/monoes/mono-agent/internal/cli/profile"
	"github.com/monoes/mono-agent/internal/storage"
)

// `profile` lives in internal/cli/profile; this is its adapter: the CLI's globals and helpers
// handed over as profilecli.Deps.

// printJSON writes v to stdout as indented JSON. Small shared helper for the
// commands that honor the global --json flag.
func printJSON(v interface{}) error {
	enc := json.NewEncoder(os.Stdout)
	enc.SetIndent("", "  ")
	return enc.Encode(v)
}

// profileErrDeps is the part of profileDeps that needs no globalConfig: the CLI's errors.
var profileErrDeps = profilecli.Deps{NotFound: errNotFound, InvalidInput: errInvalidInput}

func profileDeps(cfg *globalConfig) profilecli.Deps {
	d := profileErrDeps
	d.JSONOutput = func() bool { return cfg.JSONOutput }
	d.ProfileID = func() string { return cfg.ProfileID }
	d.OpenDB = func() (*storage.Database, error) { return initDB(cfg) }
	d.NewTable = func(w io.Writer, headers []string) *tablewriter.Table {
		return newPlainTable(w, headers, nil)
	}
	d.WithJSONErrors = func(cmd *cobra.Command) { withJSONErrors(cfg, cmd) }
	d.Reported = func(err error) error { return reportedError{err} }
	return d
}

func newProfileCmd(cfg *globalConfig) *cobra.Command {
	return profilecli.NewCommand(profileDeps(cfg))
}

// lookupProfile resolves nameOrID to a profile row. An exact id wins over a
// name, so a profile whose name happens to be another's id can't shadow it.
func lookupProfile(db *sql.DB, nameOrID string) (*profilecli.Profile, error) {
	return profileErrDeps.LookupProfile(db, nameOrID)
}

// folderSyncResult is the --json shape of `profile documents sync` and `image sync`.
type folderSyncResult = profilecli.FolderSyncResult

var newFolderSyncResult = profilecli.NewFolderSyncResult

// printFolderSync writes res as JSON or one summary line (`image sync` shares it).
func printFolderSync(cmd *cobra.Command, cfg *globalConfig, noun string, res folderSyncResult) error {
	return profilecli.PrintFolderSync(cmd, cfg.JSONOutput, noun, res)
}
