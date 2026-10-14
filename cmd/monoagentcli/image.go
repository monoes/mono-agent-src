package main

import (
	"database/sql"
	"io"

	"github.com/olekukonko/tablewriter"
	"github.com/spf13/cobra"

	imagecli "github.com/monoes/mono-agent/internal/cli/image"
	"github.com/monoes/mono-agent/internal/storage"
	"github.com/monoes/mono-agent/internal/vault"
)

// `image` lives in internal/cli/image; this is its adapter: the CLI's globals and helpers
// handed over as imagecli.Deps.

func imageDeps(cfg *globalConfig) imagecli.Deps {
	return imagecli.Deps{
		JSONOutput:   func() bool { return cfg.JSONOutput },
		ProfileID:    func() string { return cfg.ProfileID },
		OpenDB:       func() (*storage.Database, error) { return initDB(cfg) },
		NotFound:     errNotFound,
		InvalidInput: errInvalidInput,
		WriteJSON:    writeJSONTo,
		NewTable: func(w io.Writer, headers []string) *tablewriter.Table {
			return newPlainTable(w, headers, nil)
		},
		PrintFolderSync: func(cmd *cobra.Command, noun, profileID, root string, scanned int, r vault.ReconcileResult) error {
			return printFolderSync(cmd, cfg, noun, newFolderSyncResult(profileID, root, scanned, r))
		},
	}
}

func newImageCmd(cfg *globalConfig) *cobra.Command {
	return imagecli.NewCommand(imageDeps(cfg))
}

// withImageDB opens the database and runs fn on it (hil and publication share it).
func withImageDB(cfg *globalConfig, fn func(db *sql.DB) error) error {
	return imageDeps(cfg).WithDB(fn)
}
