package main

import (
	"io"

	"github.com/olekukonko/tablewriter"
	"github.com/spf13/cobra"

	secretcli "github.com/monoes/mono-agent/internal/cli/secret"
	"github.com/monoes/mono-agent/internal/storage"
)

// `secret` lives in internal/cli/secret; this is its adapter: the CLI's globals and helpers
// handed over as secretcli.Deps.

func newSecretCmd(cfg *globalConfig) *cobra.Command {
	return secretcli.NewCommand(secretcli.Deps{
		JSONOutput: func() bool { return cfg.JSONOutput },
		ProfileID:  func() string { return cfg.ProfileID },
		OpenDB:     func() (*storage.Database, error) { return initDB(cfg) },
		NotFound:   errNotFound,
		WriteJSON:  writeJSONTo,
		NewTable: func(w io.Writer, headers []string) *tablewriter.Table {
			return newPlainTable(w, headers, nil)
		},
	})
}

// readSecretValue reads a secret value from stdin up to the first newline (`connect` and
// `jev key set` share it).
func readSecretValue(r io.Reader) (string, error) { return secretcli.ReadValue(r) }

// The hooks `secret import` hands secrets.Import.
var (
	rematerializeConnection = secretcli.RematerializeConnection
	rematerializeSession    = secretcli.RematerializeSession
)
