package main

import (
	"io"

	"github.com/olekukonko/tablewriter"
	"github.com/spf13/cobra"

	publicationcli "github.com/monoes/mono-agent/internal/cli/publication"
	"github.com/monoes/mono-agent/internal/publication"
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
		Operator: cfg.requireOperator,
		RequireApproval: func(approvalID string, e publication.Entry) (func(), error) {
			return requirePublicationApproval(cfg, approvalID, e)
		},
	})
}
