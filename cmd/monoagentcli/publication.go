package main

import (
	"io"

	"github.com/olekukonko/tablewriter"
	"github.com/spf13/cobra"

	publicationcli "github.com/monoes/mono-agent/internal/cli/publication"
	"github.com/monoes/mono-agent/internal/storage"
)

// `publication` lives in internal/cli/publication; this is its adapter: the CLI's globals and
// helpers handed over as publicationcli.Deps.
func newPublicationCmd(cfg *globalConfig) *cobra.Command {
	return publicationcli.NewCommand(publicationcli.Deps{
		JSONOutput:   func() bool { return cfg.JSONOutput },
		ProfileID:    func() string { return cfg.ProfileID },
		OpenDB:       func() (*storage.Database, error) { return initDB(cfg) },
		NotFound:     errNotFound,
		InvalidInput: errInvalidInput,
		WriteJSON:    writeJSONTo,
		NewTable: func(w io.Writer, headers []string) *tablewriter.Table {
			return newPlainTable(w, headers, nil)
		},
		Operator: func(what string) error {
			_, err := callerFor("").operator(what)
			return err
		},
		RequireApproval: func(approvalID string) error { return requirePublicationApproval(cfg, approvalID) },
	})
}
